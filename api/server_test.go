package api

import (
	"testing"
	"time"

	"go.sia.tech/core/types"
)

func TestShouldPoolChangeInvalidateTemplate(t *testing.T) {
	srv := newServer(nil, nil, types.VoidAddress)
	if srv.poolInvalidationTimeout == 0 {
		t.Fatal("expected poolInvalidationTimeout to be set")
	}

	// first call should return true and second one should return false
	if !srv.shouldPoolChangeInvalidateTemplate() {
		t.Fatal("expected shouldPoolChangeInvalidateTemplate to return true")
	} else if srv.shouldPoolChangeInvalidateTemplate() {
		t.Fatal("expected shouldPoolChangeInvalidateTemplate to return false after timeout")
	}

	// wait for the timeout to expire and check again
	time.Sleep(srv.poolInvalidationTimeout)
	if !srv.shouldPoolChangeInvalidateTemplate() {
		t.Fatal("expected shouldPoolChangeInvalidateTemplate to return true")
	}
}

func TestShouldRegenerateTemplate(t *testing.T) {
	// no max age set
	srv := newServer(nil, nil, types.VoidAddress)
	if !srv.shouldRegenerateTemplate() {
		t.Fatal("expected shouldRegenerateTemplate to return true when no template cached")
	}
	srv.cachedTemplate = &MiningGetBlockTemplateResponse{Timestamp: int32(time.Now().Add(-time.Hour).Unix())}
	if srv.shouldRegenerateTemplate() {
		t.Fatal("expected shouldRegenerateTemplate to return false when template cached")
	}

	// with max age set
	srv = newServer(nil, nil, types.VoidAddress, WithMaxTemplateAge(time.Hour))
	if !srv.shouldRegenerateTemplate() {
		t.Fatal("expected shouldRegenerateTemplate to return true when no template cached")
	}
	srv.cachedTemplate = &MiningGetBlockTemplateResponse{Timestamp: int32(time.Now().Add(-59 * time.Minute).Unix())}
	if srv.shouldRegenerateTemplate() {
		t.Fatal("expected shouldRegenerateTemplate to return false when template cached and within max age")
	}
	srv.cachedTemplate = &MiningGetBlockTemplateResponse{Timestamp: int32(time.Now().Add(-61 * time.Minute).Unix())}
	if !srv.shouldRegenerateTemplate() {
		t.Fatal("expected shouldRegenerateTemplate to return true when template cached and beyond max age")
	}
}
