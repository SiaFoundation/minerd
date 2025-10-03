package api_test

import (
	"context"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/minerd/api"
	"go.sia.tech/minerd/internal/testutil"
	walletdAPI "go.sia.tech/walletd/v2/api"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func startMinerServer(tb testing.TB, cn *testutil.ConsensusNode, log *zap.Logger, opts ...api.ServerOption) *api.Client {
	tb.Helper()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		tb.Fatal("failed to listen:", err)
	}
	tb.Cleanup(func() { l.Close() })

	wm, err := wallet.NewManager(cn.Chain, cn.Store)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { wm.Close() })

	addrKey := types.GeneratePrivateKey()
	uc := types.StandardUnlockConditions(addrKey.PublicKey())
	payoutAddr := uc.UnlockHash()

	opts = append(opts, api.WithLogger(log))
	minerAPI := api.NewServer(cn.Chain, cn.Syncer, payoutAddr, opts...)
	wAPI := walletdAPI.NewServer(cn.Chain, cn.Syncer, wm)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// serve mining API
			if strings.HasPrefix(r.URL.Path, "/mining") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/mining")
				minerAPI.ServeHTTP(w, r)
				return
			}
			// serve walletd API
			wAPI.ServeHTTP(w, r)
		}),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	tb.Cleanup(func() { server.Close() })

	go server.Serve(l)

	client := api.NewClient("http://"+l.Addr().String(), "password")
	payouts, err := client.AddWallet(walletdAPI.WalletUpdateRequest{Name: "payouts"})
	if err != nil {
		tb.Fatal(err)
	}
	payoutsClient := client.Wallet(payouts.ID)
	err = payoutsClient.AddAddress(wallet.Address{
		Address: payoutAddr,
		SpendPolicy: &types.SpendPolicy{
			Type: types.PolicyTypeUnlockConditions(uc),
		},
	})
	if err != nil {
		tb.Fatal(err)
	}
	return client
}

func TestMineGetBlockTemplate(t *testing.T) {
	log := zaptest.NewLogger(t)

	test := func(t *testing.T, n *consensus.Network, genesisBlock types.Block) {
		t.Helper()

		cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
		c := startMinerServer(t, cn, log)

		// mine a few blocks to avoid starting at 0 to a premine wallet
		premineWallet, err := c.AddWallet(walletdAPI.WalletUpdateRequest{
			Name: "premine",
		})
		if err != nil {
			t.Fatal(err)
		}
		premineKey := types.GeneratePrivateKey()
		premineUC := types.StandardUnlockConditions(premineKey.PublicKey())
		err = c.Wallet(premineWallet.ID).AddAddress(wallet.Address{
			Address: premineUC.UnlockHash(),
			SpendPolicy: &types.SpendPolicy{
				Type: types.PolicyTypeUnlockConditions(premineUC),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		cn.MineBlocks(t, premineUC.UnlockHash(), 10)

		// send a transaction to make sure we mine a block with transaction
		if cn.Chain.Tip().Height < n.HardforkV2.AllowHeight {
			// V1 transaction
			resp, err := c.Wallet(premineWallet.ID).Construct([]types.SiacoinOutput{
				{
					Address: premineUC.UnlockHash(),
					Value:   types.Siacoins(100),
				},
			}, nil, premineUC.UnlockHash())
			if err != nil {
				t.Fatal(err)
			}
			txn := resp.Transaction
			for i, txnSig := range txn.Signatures {
				sigHash := cn.Chain.TipState().WholeSigHash(txn, txnSig.ParentID, 0, 0, nil)
				sig := premineKey.SignHash(sigHash)
				txn.Signatures[i].Signature = sig[:]
			}
			if _, err := c.TxpoolBroadcast(resp.Basis, []types.Transaction{txn}, nil); err != nil {
				t.Fatal(err)
			}
		} else {
			// V2 transaction
			resp, err := c.Wallet(premineWallet.ID).ConstructV2([]types.SiacoinOutput{
				{
					Address: premineUC.UnlockHash(),
					Value:   types.Siacoins(100),
				},
			}, nil, premineUC.UnlockHash())
			if err != nil {
				t.Fatal(err)
			}
			txn := resp.Transaction
			sigHash := cn.Chain.TipState().InputSigHash(txn)
			for i := range txn.SiacoinInputs {
				txn.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{premineKey.SignHash(sigHash)}
			}

			// broadcast the transaction
			if _, err := c.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{txn}); err != nil {
				t.Fatal(err)
			}
		}

		// get block template
		resp, err := c.MiningGetBlockTemplate(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}

		var parentID types.BlockID
		if err := parentID.UnmarshalText([]byte(resp.PreviousBlockHash)); err != nil {
			t.Fatal(err)
		}

		rawMinerPayout, err := hex.DecodeString(resp.MinerPayout[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		dec := types.NewBufDecoder(rawMinerPayout)

		var minerPayout types.SiacoinOutput
		switch resp.Version {
		case 1:
			(*types.V1SiacoinOutput)(&minerPayout).DecodeFrom(dec)
		case 2:
			(*types.V2SiacoinOutput)(&minerPayout).DecodeFrom(dec)
		default:
			t.Fatal("unknown version", resp.Version)
		}
		if err := dec.Err(); err != nil {
			t.Fatal(err)
		}

		var txns []types.Transaction
		var v2Txns []types.V2Transaction
		for _, templateTxn := range resp.Transactions {
			rawTxn, err := hex.DecodeString(templateTxn.Data)
			if err != nil {
				t.Fatal(err)
			}

			dec := types.NewBufDecoder(rawTxn)
			switch templateTxn.TxType {
			case "1":
				var txn types.Transaction
				txn.DecodeFrom(dec)
				if err := dec.Err(); err != nil {
					t.Fatal(err)
				}
				txns = append(txns, txn)
			case "2":
				var txn types.V2Transaction
				txn.DecodeFrom(dec)
				if err := dec.Err(); err != nil {
					t.Fatal(err)
				}
				v2Txns = append(v2Txns, txn)
			default:
				t.Fatal("unknown type", templateTxn.TxType)
			}
		}

		var v2BlockData *types.V2BlockData
		if resp.Version == 2 {
			v2BlockData = &types.V2BlockData{
				Height:       uint64(resp.Height),
				Transactions: v2Txns,
			}

			cs, err := c.ConsensusTipState()
			if err != nil {
				t.Fatal(err)
			}
			v2BlockData.Commitment = cs.Commitment(minerPayout.Address, txns, v2Txns)
		}

		// construct block
		b := types.Block{
			ParentID:     parentID,
			Timestamp:    time.Unix(int64(resp.Timestamp), 0),
			MinerPayouts: []types.SiacoinOutput{minerPayout},
			V2:           v2BlockData,
			Transactions: txns,
		}

		// sanity check commitment
		if b.Header().Commitment != resp.Commitment {
			t.Fatalf("expected commitment %v, got %v", b.Header().Commitment, resp.Commitment)
		}

		var target types.BlockID
		if err := target.UnmarshalText([]byte(resp.Target)); err != nil {
			t.Fatal(err)
		}

		// make sure the target is correct
		cs, err := c.ConsensusTipState()
		if err != nil {
			t.Fatal(err)
		} else if target != cs.PoWTarget() {
			t.Fatalf("expected target %v, got %v", cs.PoWTarget(), target)
		}

		// mine block
		mineBlock := func(b *types.Block, target types.BlockID) {
			cs, err := c.ConsensusTipState()
			if err != nil {
				t.Fatal(err)
			} else if !coreutils.FindBlockNonce(cs, b, 10*time.Second) {
				t.Fatal("failed to find nonce")
			}
		}
		mineBlock(&b, target)

		// submit block
		if err := c.MiningSubmitBlock(context.Background(), b); err != nil {
			t.Fatal(err)
		}

		// the block should be the new tip
		tip, err := c.ConsensusTip()
		if err != nil {
			t.Fatal(err)
		} else if tip.ID != b.ID() {
			t.Fatalf("expected tip to be %v, got %v", b.ID(), tip.ID)
		}
	}

	t.Run("v1", func(t *testing.T) {
		network, genesisBlock := testutil.V1Network()
		test(t, network, genesisBlock)
	})

	t.Run("v2", func(t *testing.T) {
		network, genesisBlock := testutil.V2Network()
		network.HardforkV2.FinalCutHeight = network.HardforkV2.RequireHeight + 1000
		test(t, network, genesisBlock)
	})

	t.Run("v2-final-cut", func(t *testing.T) {
		network, genesisBlock := testutil.V2Network()
		test(t, network, genesisBlock)
	})
}

func TestMineGetBlockTemplateLongpolling(t *testing.T) {
	log := zaptest.NewLogger(t)

	t.Helper()

	network, genesisBlock := testutil.V1Network()
	cn := testutil.NewConsensusNode(t, network, genesisBlock, log)
	c := startMinerServer(t, cn, log)

	// get block template
	resp, err := c.MiningGetBlockTemplate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// get block template again with same id, this should block
	done := make(chan struct{})
	go func(longpollid string) {
		defer close(done)

		_, err := c.MiningGetBlockTemplate(context.Background(), longpollid)
		if err != nil {
			t.Error(err)
		}
	}(resp.LongPollID)

	select {
	case <-done:
		t.Fatal("expected longpolling to block")
	case <-time.After(time.Second):
	}

	// mine a block to unblock API
	cn.MineBlocks(t, types.VoidAddress, 1)
	<-done
}

func TestMineGetBlockTemplateMaxAge(t *testing.T) {
	log := zaptest.NewLogger(t)

	t.Helper()

	network, genesisBlock := testutil.V1Network()
	cn := testutil.NewConsensusNode(t, network, genesisBlock, log)
	c := startMinerServer(t, cn, log, api.WithMaxTemplateAge(time.Second))

	// get block template
	resp, err := c.MiningGetBlockTemplate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// get block template again with same id, this should not return immediately
	// and also not block for much more than 1s
	start := time.Now()
	_, err = c.MiningGetBlockTemplate(context.Background(), resp.LongPollID)
	if err != nil {
		t.Error(err)
	}
	if time.Since(start) < 500*time.Millisecond || time.Since(start) > 2*time.Second {
		t.Fatalf("expected MiningGetBlockTemplate to return after ~1s, got %v", time.Since(start))
	}
}
