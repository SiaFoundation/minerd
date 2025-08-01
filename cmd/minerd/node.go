package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/minerd/api"
	wAPI "go.sia.tech/walletd/v2/api"
	"go.sia.tech/walletd/v2/build"
	"go.sia.tech/walletd/v2/persist/sqlite"
	"go.sia.tech/walletd/v2/wallet"
	"go.sia.tech/web/walletd"
	"go.uber.org/zap"
	"lukechampine.com/upnp"
)

func tryConfigPaths() []string {
	if str := os.Getenv(configFileEnvVar); str != "" {
		return []string{str}
	}

	paths := []string{
		"minerd.yml",
	}
	if str := os.Getenv(dataDirEnvVar); str != "" {
		paths = append(paths, filepath.Join(str, "minerd.yml"))
	}

	switch runtime.GOOS {
	case "windows":
		paths = append(paths, filepath.Join(os.Getenv("APPDATA"), "minerd", "minerd.yml"))
	case "darwin":
		paths = append(paths, filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "minerd", "minerd.yml"))
	case "linux", "freebsd", "openbsd":
		paths = append(paths,
			filepath.Join(string(filepath.Separator), "etc", "minerd", "minerd.yml"),
			filepath.Join(string(filepath.Separator), "var", "lib", "minerd", "minerd.yml"), // old default for the Linux service
		)
	}
	return paths
}

func defaultDataDirectory(fp string) string {
	// use the provided path if it's not empty
	if fp != "" {
		return fp
	}

	// check for databases in the current directory
	if _, err := os.Stat("minerd.db"); err == nil {
		return "."
	} else if _, err := os.Stat("minerd.sqlite3"); err == nil {
		return "."
	}

	// default to the operating system's application directory
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "minerd")
	case "darwin":
		return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "minerd")
	case "linux", "freebsd", "openbsd":
		return filepath.Join(string(filepath.Separator), "var", "lib", "minerd")
	default:
		return "."
	}
}

func setupUPNP(ctx context.Context, port uint16, log *zap.Logger) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d, err := upnp.Discover(ctx)
	if err != nil {
		return "", fmt.Errorf("couldn't discover UPnP router: %w", err)
	} else if !d.IsForwarded(port, "TCP") {
		if err := d.Forward(uint16(port), "TCP", "minerd"); err != nil {
			log.Debug("couldn't forward port", zap.Error(err))
		} else {
			log.Debug("upnp: forwarded p2p port", zap.Uint16("port", port))
		}
	}
	return d.ExternalIP()
}

func loadCustomNetwork(fp string) (*consensus.Network, types.Block, error) {
	f, err := os.Open(fp)
	if err != nil {
		return nil, types.Block{}, fmt.Errorf("failed to open network file: %w", err)
	}
	defer f.Close()

	var network struct {
		Network consensus.Network `json:"network" yaml:"network"`
		Genesis types.Block       `json:"genesis" yaml:"genesis"`
	}

	if err := json.NewDecoder(f).Decode(&network); err != nil {
		return nil, types.Block{}, fmt.Errorf("failed to decode JSON network file: %w", err)
	}
	return &network.Network, network.Genesis, nil
}

// migrateConsensusDB checks if the consensus database needs to be migrated
// to match the new v2 commitment.
func migrateConsensusDB(fp string, n *consensus.Network, genesis types.Block, log *zap.Logger) error {
	bdb, err := coreutils.OpenBoltChainDB(fp)
	if err != nil {
		return fmt.Errorf("failed to open consensus database: %w", err)
	}
	defer bdb.Close()

	dbstore, tipState, err := chain.NewDBStore(bdb, n, genesis, chain.NewZapMigrationLogger(log.Named("chaindb")))
	if err != nil {
		return fmt.Errorf("failed to create chain store: %w", err)
	} else if tipState.Index.Height < n.HardforkV2.AllowHeight {
		return nil // no migration needed, the chain is still on v1
	}

	log.Debug("checking for v2 commitment migration")
	b, _, ok := dbstore.Block(tipState.Index.ID)
	if !ok {
		return fmt.Errorf("failed to get tip block %q", tipState.Index)
	} else if b.V2 == nil {
		log.Debug("tip block is not a v2 block, skipping commitment migration")
		return nil
	}

	parentState, ok := dbstore.State(b.ParentID)
	if !ok {
		return fmt.Errorf("failed to get parent state for tip block %q", b.ParentID)
	}
	commitment := parentState.Commitment(b.MinerPayouts[0].Address, b.Transactions, b.V2Transactions())
	log = log.With(zap.Stringer("tip", b.ID()), zap.Stringer("commitment", b.V2.Commitment), zap.Stringer("expected", commitment))
	if b.V2.Commitment == commitment {
		log.Debug("tip block commitment matches parent state, no migration needed")
		return nil
	}
	// reset the database if the commitment is not a merkle root
	log.Debug("resetting consensus database for new v2 commitment")
	if err := bdb.Close(); err != nil {
		return fmt.Errorf("failed to close old consensus database: %w", err)
	} else if err := os.RemoveAll(fp); err != nil {
		return fmt.Errorf("failed to remove old consensus database: %w", err)
	}
	log.Debug("consensus database reset")
	return nil
}

func runNode(ctx context.Context, cfg Config, log *zap.Logger, enableDebug bool) error {
	var network *consensus.Network
	var genesisBlock types.Block
	var bootstrapPeers []string
	switch cfg.Consensus.Network {
	case "mainnet":
		network, genesisBlock = chain.Mainnet()
		bootstrapPeers = syncer.MainnetBootstrapPeers
	case "zen":
		network, genesisBlock = chain.TestnetZen()
		bootstrapPeers = syncer.ZenBootstrapPeers
	case "anagami":
		network, genesisBlock = chain.TestnetAnagami()
		bootstrapPeers = syncer.AnagamiBootstrapPeers
	default:
		var err error
		network, genesisBlock, err = loadCustomNetwork(cfg.Consensus.Network)
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("invalid network: must be one of 'mainnet', 'zen', or 'anagami'")
		} else if err != nil {
			return fmt.Errorf("failed to load custom network: %w", err)
		}
	}
	payoutAddr := types.VoidAddress
	if cfg.Mining.PayoutAddress != "" {
		if err := payoutAddr.UnmarshalText([]byte(cfg.Mining.PayoutAddress)); err != nil {
			return fmt.Errorf("failed to parse payout address: %w", err)
		}
	}

	consensusPath := filepath.Join(cfg.Directory, "consensus.db")
	if err := migrateConsensusDB(consensusPath, network, genesisBlock, log.Named("migrate")); err != nil {
		return fmt.Errorf("failed to open consensus database: %w", err)
	}

	bdb, err := coreutils.OpenBoltChainDB(consensusPath)
	if err != nil {
		return fmt.Errorf("failed to open consensus database: %w", err)
	}
	defer bdb.Close()

	dbstore, tipState, err := chain.NewDBStore(bdb, network, genesisBlock, chain.NewZapMigrationLogger(log.Named("chaindb")))
	if err != nil {
		return fmt.Errorf("failed to create chain store: %w", err)
	}
	cm := chain.NewManager(dbstore, tipState)

	syncerListener, err := net.Listen("tcp", cfg.Syncer.Address)
	if err != nil {
		return fmt.Errorf("failed to listen on %q: %w", cfg.Syncer.Address, err)
	}
	defer syncerListener.Close()

	httpListener, err := net.Listen("tcp", cfg.HTTP.Address)
	if err != nil {
		return fmt.Errorf("failed to listen on %q: %w", cfg.HTTP.Address, err)
	}
	defer httpListener.Close()

	syncerAddr := syncerListener.Addr().String()
	if cfg.Syncer.EnableUPnP {
		_, portStr, _ := net.SplitHostPort(cfg.Syncer.Address)
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			return fmt.Errorf("failed to parse syncer port: %w", err)
		}

		ip, err := setupUPNP(context.Background(), uint16(port), log)
		if err != nil {
			log.Warn("failed to set up UPnP", zap.Error(err))
		} else {
			syncerAddr = net.JoinHostPort(ip, portStr)
		}
	}

	// peers will reject us if our hostname is empty or unspecified, so use loopback
	host, port, _ := net.SplitHostPort(syncerAddr)
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		syncerAddr = net.JoinHostPort("127.0.0.1", port)
	}

	store, err := sqlite.OpenDatabase(filepath.Join(cfg.Directory, "minerd.sqlite3"), sqlite.WithLog(log.Named("sqlite3")))
	if err != nil {
		return fmt.Errorf("failed to open wallet database: %w", err)
	}
	defer store.Close()

	if cfg.Syncer.Bootstrap {
		for _, peer := range bootstrapPeers {
			if err := store.AddPeer(peer); err != nil {
				return fmt.Errorf("failed to add bootstrap peer %q: %w", peer, err)
			}
		}
		for _, peer := range cfg.Syncer.Peers {
			if err := store.AddPeer(peer); err != nil {
				return fmt.Errorf("failed to add peer %q: %w", peer, err)
			}
		}
	}

	ps, err := sqlite.NewPeerStore(store)
	if err != nil {
		return fmt.Errorf("failed to create peer store: %w", err)
	}

	header := gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerAddr,
	}

	s := syncer.New(syncerListener, cm, ps, header,
		syncer.WithLogger(log.Named("syncer")),
		syncer.WithMaxInboundPeers(1024),
		syncer.WithMaxInflightRPCs(1024))
	defer s.Close()
	go s.Run()

	wm, err := wallet.NewManager(cm, store, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(cfg.Index.Mode), wallet.WithSyncBatchSize(cfg.Index.BatchSize))
	if err != nil {
		return fmt.Errorf("failed to create wallet manager: %w", err)
	}
	defer wm.Close()

	walletdAPIOpts := []wAPI.ServerOption{
		wAPI.WithLogger(log.Named("api")),
		wAPI.WithPublicEndpoints(cfg.HTTP.PublicEndpoints),
		wAPI.WithBasicAuth(cfg.HTTP.Password),
	}
	if enableDebug {
		walletdAPIOpts = append(walletdAPIOpts, wAPI.WithDebug())
	}
	minerAPIOpts := []api.ServerOption{
		api.WithLogger(log.Named("api")),
		api.WithBasicAuth(cfg.HTTP.Password),
	}
	if cfg.Mining.MaxTemplateAge > 0 {
		minerAPIOpts = append(minerAPIOpts, api.WithMaxTemplateAge(cfg.Mining.MaxTemplateAge))
	}
	walletdAPI := wAPI.NewServer(cm, s, wm, walletdAPIOpts...)
	minerAPI := api.NewServer(cm, s, payoutAddr, minerAPIOpts...)
	web := walletd.Handler()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// serve mining API
			if strings.HasPrefix(r.URL.Path, "/api/mining") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/mining")
				minerAPI.ServeHTTP(w, r)
				return
			}
			// serve walletd API
			if strings.HasPrefix(r.URL.Path, "/api") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
				walletdAPI.ServeHTTP(w, r)
				return
			}
			web.ServeHTTP(w, r)
		}),
		ReadTimeout: 10 * time.Second,
	}
	defer server.Close()
	go server.Serve(httpListener)

	log.Info("node started", zap.String("network", network.Name), zap.Stringer("syncer", syncerListener.Addr()), zap.Stringer("http", httpListener.Addr()), zap.String("version", build.Version()), zap.String("commit", build.Commit()))
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
