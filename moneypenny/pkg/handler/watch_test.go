package handler

import (
	"testing"
	"time"
)

func TestWatchRegistryEpochExpiryAndIdempotentClose(t *testing.T) {
	r := newWatchRegistry()
	now := time.Now()
	r.now = func() time.Time { return now }
	w, err := r.open(sessionWatch{WatchID: "w", ConnectionID: "c", Epoch: 1, SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.renew("w", "c", 2); err == nil {
		t.Fatal("stale renewal succeeded")
	}
	if !r.close("w", "c", 1) || r.close("w", "c", 1) {
		t.Fatal("close was not idempotent")
	}
	w, err = r.open(sessionWatch{WatchID: "w", ConnectionID: "c", Epoch: 2, SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	now = w.ExpiresAt.Add(time.Second)
	r.mu.Lock()
	r.expireLocked(now)
	r.mu.Unlock()
	if _, err := r.renew("w", "c", 2); err == nil {
		t.Fatal("expired lease renewed")
	}
}
