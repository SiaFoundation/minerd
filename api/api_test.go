package api_test

import (
	"context"
	"encoding/hex"
	"net"
	"net/http"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/minerd/api"
	"go.sia.tech/minerd/internal/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func startMinerServer(tb testing.TB, cn *testutil.ConsensusNode, log *zap.Logger) *api.Client {
	tb.Helper()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		tb.Fatal("failed to listen:", err)
	}
	tb.Cleanup(func() { l.Close() })

	server := &http.Server{
		Handler:      api.NewServer(cn.Chain, cn.Syncer, api.WithDebug(), api.WithLogger(log)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	tb.Cleanup(func() { server.Close() })

	go server.Serve(l)
	return api.NewClient("http://"+l.Addr().String(), "password")
}

func TestMineGetBlockTemplate(t *testing.T) {
	log := zaptest.NewLogger(t)

	test := func(n *consensus.Network, genesisBlock types.Block) {
		t.Helper()

		cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
		c := startMinerServer(t, cn, log)

		// mine a few blocks to avoid starting at 0
		cn.MineBlocks(t, types.Address{}, 10)

		// get block template
		minerAddr := types.Address{1, 2, 3}
		resp, err := c.MiningGetBlockTemplate(minerAddr, "")
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
			v2BlockData.Commitment = cs.Commitment(cs.TransactionsCommitment(txns, v2Txns), minerAddr)
		}

		// construct block
		b := types.Block{
			ParentID:     parentID,
			Timestamp:    time.Unix(int64(resp.Timestamp), 0),
			MinerPayouts: []types.SiacoinOutput{minerPayout},
			V2:           v2BlockData,
			Transactions: txns,
		}

		var target types.BlockID
		if err := target.UnmarshalText([]byte(resp.Target)); err != nil {
			t.Fatal(err)
		}

		// mine block
		mineBlock := func(b *types.Block, target types.BlockID) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			factor := 1009
			for b.ID().CmpWork(target) < 0 {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
				}
				b.Nonce += uint64(factor)
			}
		}
		mineBlock(&b, target)

		// submit block
		if err := c.MiningSubmitBlock(b); err != nil {
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
		test(network, genesisBlock)
	})

	t.Run("v2", func(t *testing.T) {
		network, genesisBlock := testutil.V2Network()
		test(network, genesisBlock)
	})
}
