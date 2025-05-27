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
