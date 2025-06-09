package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
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

// WithMaxTemplateAge sets the maximum age of the cached block template before
// it gets invalidated.
func WithMaxTemplateAge(maxAge time.Duration) ServerOption {
	return func(s *server) {
		s.cachedTemplateMaxAge = maxAge
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
		OnPoolChange(fn func()) (cancel func())
		OnReorg(fn func(types.ChainIndex)) (cancel func())
		UnconfirmedParents(txn types.Transaction) []types.Transaction
		UpdateV2TransactionSet(txns []types.V2Transaction, from types.ChainIndex, to types.ChainIndex) ([]types.V2Transaction, error)
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []*syncer.Peer
		PeerInfo(addr string) (syncer.PeerInfo, error)
		Connect(ctx context.Context, addr string) (*syncer.Peer, error)
		BroadcastHeader(types.BlockHeader) error
		BroadcastV2BlockOutline(bo gateway.V2BlockOutline) error
	}
)

type server struct {
	startTime               time.Time
	debugEnabled            bool
	publicEndpoints         bool
	password                string
	payoutAddr              types.Address
	poolInvalidationTimeout time.Duration

	cachedTemplateMu          sync.Mutex
	cachedTemplate            *MiningGetBlockTemplateResponse // cached template, set to 'nil' when invalidated
	cachedTemplateMaxAge      time.Duration                   // maximum age of the cached template before it is invalidated
	cachedTemplateInvalidated chan struct{}                   // closed when the cached template is invalidated
	lastPoolInvalidate        time.Time                       // last time the template was invalidated due to a pool change

	log *zap.Logger
	cm  ChainManager
	s   Syncer
}

func (s *server) invalidateCachedTemplate() {
	s.cachedTemplateMu.Lock()
	s.cachedTemplate = nil
	if s.cachedTemplateInvalidated != nil {
		close(s.cachedTemplateInvalidated)
	}
	s.cachedTemplateInvalidated = make(chan struct{})
	s.cachedTemplateMu.Unlock()
}

func (s *server) miningGetBlockTemplateHandler(jc jape.Context) {
	if s.payoutAddr == types.VoidAddress {
		jc.Error(errors.New("can't use getblocktemplate without specifying a payout address"), http.StatusServiceUnavailable)
		return
	}

	var req MiningGetBlockTemplateRequest
	if jc.Decode(&req) != nil {
		return
	}

	for {
		// get template or generate new one
		template, invalidateChan, err := func() (MiningGetBlockTemplateResponse, <-chan struct{}, error) {
			s.cachedTemplateMu.Lock()
			defer s.cachedTemplateMu.Unlock()

			// generate new template if required
			if s.shouldRegenerateTemplate() {
				template, err := generateBlockTemplate(s.cm, s.payoutAddr)
				if err != nil {
					return MiningGetBlockTemplateResponse{}, nil, err
				}
				s.cachedTemplate = &template
			}
			return *s.cachedTemplate, s.cachedTemplateInvalidated, nil
		}()
		if jc.Check("failed to get template", err) != nil {
			return
		}

		// if we got a new template, return it
		if template.LongPollID != req.LongPollID {
			jc.Encode(s.cachedTemplate)
			return
		}

		// otherwise, wait until the template is invalidated again or the
		// template has reached its maximum age
		var maxAgeChan <-chan time.Time
		if s.cachedTemplateMaxAge > 0 {
			blockMaxTime := time.Unix(int64(template.Timestamp), 0).Add(s.cachedTemplateMaxAge)
			maxAgeChan = time.After(time.Until(blockMaxTime))
		}

		select {
		case <-jc.Request.Context().Done():
			return
		case <-invalidateChan:
			continue
		case <-maxAgeChan:
			continue
		}
	}
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
		if jc.Check("failed to broadcast header", s.s.BroadcastHeader(block.Header())) != nil {
			return
		}
	} else {
		if jc.Check("failed to broadcast block outline", s.s.BroadcastV2BlockOutline(gateway.OutlineBlock(block, s.cm.PoolTransactions(), s.cm.V2PoolTransactions()))) != nil {
			return
		}
	}
	jc.Encode(nil)
}

// shouldRegenerateTemplate checks if the cached block template should be
// regenerated. This happens if no valid one exists or if it has reached its
// maximum age and needs to be regenerated. Expects cachedTemplateMu to be
// locked.
func (s *server) shouldRegenerateTemplate() bool {
	if s.cachedTemplate == nil {
		return true // no template cached, needs to be generated
	} else if s.cachedTemplateMaxAge == 0 {
		return false // no max age set, template never expires
	}
	blockTime := time.Unix(int64(s.cachedTemplate.Timestamp), 0)
	return time.Since(blockTime) >= s.cachedTemplateMaxAge
}

func newServer(cm ChainManager, s Syncer, payoutAddr types.Address, opts ...ServerOption) *server {
	srv := &server{
		log:                     zap.NewNop(),
		debugEnabled:            false,
		payoutAddr:              payoutAddr,
		poolInvalidationTimeout: 200 * time.Millisecond,
		publicEndpoints:         false,
		startTime:               time.Now(),

		cachedTemplateInvalidated: make(chan struct{}, 1),

		cm: cm,
		s:  s,
	}
	for _, opt := range opts {
		opt(srv)
	}
	return srv
}

// NewServer returns an HTTP handler that serves the minerd API.
func NewServer(cm ChainManager, s Syncer, payoutAddr types.Address, opts ...ServerOption) http.Handler {
	srv := newServer(cm, s, payoutAddr, opts...)

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

	// invalidate cached template on pool change
	_ = cm.OnPoolChange(func() {
		if srv.shouldPoolChangeInvalidateTemplate() {
			srv.invalidateCachedTemplate()
		}
	})

	// invlaidate cached template on reorg
	_ = cm.OnReorg(func(_ types.ChainIndex) {
		srv.invalidateCachedTemplate()
	})

	handlers := map[string]jape.Handler{
		"POST /getblocktemplate": wrapAuthHandler(srv.miningGetBlockTemplateHandler),
		"POST /submitblock":      wrapAuthHandler(srv.miningSubmitBlockTemplateHandler),
	}
	return jape.Mux(handlers)
}

func (s *server) shouldPoolChangeInvalidateTemplate() bool {
	s.cachedTemplateMu.Lock()
	defer s.cachedTemplateMu.Unlock()
	if time.Since(s.lastPoolInvalidate) < s.poolInvalidationTimeout {
		// if the pool change happened too recently, don't invalidate the
		// template
		return false
	}
	s.lastPoolInvalidate = time.Now()
	return true
}
