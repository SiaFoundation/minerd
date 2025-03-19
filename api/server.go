package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"go.sia.tech/jape"
	"go.uber.org/zap"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
)

// A ServerOption sets an optional parameter for the server.
type ServerOption func(*server)

// WithLogger sets the logger used by the server.
func WithLogger(log *zap.Logger) ServerOption {
	return func(s *server) {
		s.log = log
	}
}

// WithBasicAuth sets the password for basic authentication.
func WithBasicAuth(password string) ServerOption {
	return func(s *server) {
		s.password = password
	}
}

type (
	// A ChainManager manages blockchain and txpool state.
	ChainManager interface {
		UpdatesSince(types.ChainIndex, int) ([]chain.RevertUpdate, []chain.ApplyUpdate, error)

		Tip() types.ChainIndex
		BestIndex(height uint64) (types.ChainIndex, bool)
		Block(id types.BlockID) (types.Block, bool)
		TipState() consensus.State
		AddBlocks([]types.Block) error
		RecommendedFee() types.Currency
		PoolTransactions() []types.Transaction
		V2PoolTransactions() []types.V2Transaction
		AddPoolTransactions(txns []types.Transaction) (bool, error)
		AddV2PoolTransactions(index types.ChainIndex, txns []types.V2Transaction) (bool, error)
		UnconfirmedParents(txn types.Transaction) []types.Transaction
		UpdateV2TransactionSet(txns []types.V2Transaction, from types.ChainIndex, to types.ChainIndex) ([]types.V2Transaction, error)
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []*syncer.Peer
		PeerInfo(addr string) (syncer.PeerInfo, error)
		Connect(ctx context.Context, addr string) (*syncer.Peer, error)
		BroadcastHeader(types.BlockHeader)
		BroadcastTransactionSet(txns []types.Transaction)
		BroadcastV2TransactionSet(index types.ChainIndex, txns []types.V2Transaction)
		BroadcastV2BlockOutline(bo gateway.V2BlockOutline)
	}
)

type server struct {
	startTime       time.Time
	debugEnabled    bool
	publicEndpoints bool
	password        string

	log *zap.Logger
	cm  ChainManager
	s   Syncer
}

func (s *server) miningGetBlockTemplateHandler(jc jape.Context) {
	var req MiningGetBlockTemplateRequest
	if jc.Decode(&req) != nil {
		return
	} else if req.PayoutAddress == types.VoidAddress {
		jc.Error(errors.New("payout address can't be empty"), http.StatusBadRequest)
		return
	}

	// TODO: add polling

	template, err := generateBlockTemplate(s.cm, req.PayoutAddress)
	if jc.Check("failed to generate block template", err) != nil {
		return
	}
	jc.Encode(template)
}

func (s *server) miningSubmitBlockTemplateHandler(jc jape.Context) {
	var req MiningSubmitBlockRequest
	if jc.Decode(&req) != nil {
		return
	} else if len(req.Params) < 1 {
		jc.Error(errors.New("expected block hex in request params array"), http.StatusBadRequest)
		return
	}
	rawBlock, err := hex.DecodeString(req.Params[0])
	if jc.Check("couldn't decode block hex", err) != nil {
		return
	}

	// decode block
	var block types.Block
	isV2 := s.cm.Tip().Height >= s.cm.TipState().Network.HardforkV2.AllowHeight
	dec := types.NewBufDecoder(rawBlock)
	if !isV2 {
		(*types.V1Block)(&block).DecodeFrom(dec)
	} else {
		(*types.V2Block)(&block).DecodeFrom(dec)
	}
	if jc.Check("couldn't decode block", dec.Err()) != nil {
		return
	}

	// verify and broadcast block
	if jc.Check("failed to add block to chain manager", s.cm.AddBlocks([]types.Block{block})) != nil {
		return
	}
	if !isV2 {
		s.s.BroadcastHeader(block.Header())
	} else {
		s.s.BroadcastV2BlockOutline(gateway.OutlineBlock(block, s.cm.PoolTransactions(), s.cm.V2PoolTransactions()))
	}
	jc.EmptyResonse()
}

// NewServer returns an HTTP handler that serves the minerd API.
func NewServer(cm ChainManager, s Syncer, opts ...ServerOption) http.Handler {
	srv := server{
		log:             zap.NewNop(),
		debugEnabled:    false,
		publicEndpoints: false,
		startTime:       time.Now(),

		cm: cm,
		s:  s,
	}
	for _, opt := range opts {
		opt(&srv)
	}

	// checkAuth checks the request for basic authentication.
	checkAuth := func(jc jape.Context) bool {
		if srv.password == "" {
			// unset password is equivalent to no auth
			return true
		}

		// verify auth header
		_, pass, ok := jc.Request.BasicAuth()
		if ok && pass == srv.password {
			return true
		}

		jc.Error(errors.New("unauthorized"), http.StatusUnauthorized)
		return false
	}

	// wrapAuthHandler wraps a jape handler with an authentication check.
	wrapAuthHandler := func(h jape.Handler) jape.Handler {
		return func(jc jape.Context) {
			if !checkAuth(jc) {
				return
			}
			h(jc)
		}
	}

	handlers := map[string]jape.Handler{
		"POST /getblocktemplate": wrapAuthHandler(srv.miningGetBlockTemplateHandler),
		"POST /submitblock":      wrapAuthHandler(srv.miningSubmitBlockTemplateHandler),
	}
	return jape.Mux(handlers)
}
