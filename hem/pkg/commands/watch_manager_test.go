package commands

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchManagerLeaseRefcountsAndEpochs(t *testing.T) {
	w := NewWatchManager()
	now := time.Now()
	var nowNanos atomic.Int64
	nowNanos.Store(now.UnixNano())
	w.now = func() time.Time { return time.Unix(0, nowNanos.Load()) }
	one, first, err := w.OpenLease("a", "conn", 1, "session")
	if err != nil || !first {
		t.Fatalf("open first = %+v, %v, %v", one, first, err)
	}
	_, first, err = w.OpenLease("b", "conn2", 2, "session")
	if err != nil || first || w.RefCount("session") != 2 {
		t.Fatalf("open second = first %v err %v refs %d", first, err, w.RefCount("session"))
	}
	if _, err := w.RenewLease("a", "conn", 2); err == nil {
		t.Fatal("stale epoch renewal succeeded")
	}
	if _, last, _ := w.CloseLease("a", "conn", 1); last {
		t.Fatal("first close removed shared upstream")
	}
	if _, last, _ := w.CloseLease("a", "conn", 1); last {
		t.Fatal("duplicate close removed shared upstream")
	}
	nowNanos.Store(now.Add(WatchLeaseDuration + time.Second).UnixNano())
	expired := w.Expire()
	if len(expired) != 1 || expired[0].SessionID != "session" || w.RefCount("session") != 0 {
		t.Fatalf("expiry = %v refs=%d", expired, w.RefCount("session"))
	}
	_ = one
}

func TestWatchManagerExpiryReturnsAndCallbacksExactAggregate(t *testing.T) {
	w := NewWatchManager()
	t.Cleanup(w.Close)
	w.expireInterval = 5 * time.Millisecond
	now := time.Now()
	var nowNanos atomic.Int64
	nowNanos.Store(now.UnixNano())
	w.now = func() time.Time { return time.Unix(0, nowNanos.Load()) }
	var got chan WatchLease = make(chan WatchLease, 1)
	w.SetExpireCallback(func(lease WatchLease) { got <- lease })
	_, _, err := w.OpenLease("consumer", "browser", 7, "session")
	if err != nil {
		t.Fatal(err)
	}
	upstream, ok := w.Aggregate("session")
	if !ok {
		t.Fatal("aggregate missing")
	}
	nowNanos.Store(now.Add(WatchLeaseDuration + time.Second).UnixNano())
	select {
	case gotLease := <-got:
		if gotLease != upstream {
			t.Fatalf("callback aggregate = %+v, want %+v", gotLease, upstream)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry callback not delivered")
	}
	if w.RefCount("session") != 0 {
		t.Fatalf("refcount = %d, want zero", w.RefCount("session"))
	}
	if _, ok := w.Aggregate("session"); ok {
		t.Fatal("aggregate survived final expiry")
	}
	select {
	case <-got:
		t.Fatal("expiry callback delivered twice")
	default:
	}
}

func TestWatchManagerCloseUsesStoredAggregate(t *testing.T) {
	w := NewWatchManager()
	t.Cleanup(w.Close)
	_, _, err := w.OpenLease("consumer", "browser", 7, "actual-session")
	if err != nil {
		t.Fatal(err)
	}
	upstream, ok := w.Aggregate("actual-session")
	if !ok {
		t.Fatal("aggregate missing")
	}
	session, last, removed := w.CloseLease("consumer", "browser", 7)
	if session != "actual-session" || !last || removed != upstream {
		t.Fatalf("close = %q, %v, %+v; want stored aggregate %+v", session, last, removed, upstream)
	}
}

func TestWatchManagerAbruptConnectionKeepsOtherConsumers(t *testing.T) {
	w := NewWatchManager()
	t.Cleanup(w.Close)
	first, _, err := w.OpenLease("first", "connection-a", 1, "session")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = w.OpenLease("second", "connection-b", 2, "session")
	if err != nil {
		t.Fatal(err)
	}
	upstream, ok := w.Aggregate("session")
	if !ok {
		t.Fatal("aggregate missing")
	}

	removed := w.CloseConnection("connection-a", 1)
	if len(removed) != 0 || w.RefCount("session") != 1 {
		t.Fatalf("first connection cleanup = %+v refs=%d, want no upstream removal and one ref", removed, w.RefCount("session"))
	}
	if _, ok := w.Aggregate("session"); !ok {
		t.Fatal("aggregate removed while another consumer remained")
	}

	removed = w.CloseConnection("connection-b", 2)
	if len(removed) != 1 || removed[0] != upstream || w.RefCount("session") != 0 {
		t.Fatalf("final connection cleanup = %+v refs=%d, want %+v and zero refs", removed, w.RefCount("session"), upstream)
	}
	if _, last, _ := w.CloseLease(first.WatchID, "connection-a", 1); last {
		t.Fatal("stale close removed a lease after connection cleanup")
	}
}
