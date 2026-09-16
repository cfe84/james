package web

import (
	"sync"
	"testing"
	"time"
)

type watchHem struct {
	mu   sync.Mutex
	reqs []*Request
}

func (h *watchHem) Send(req *Request) (*Response, error) {
	h.mu.Lock()
	h.reqs = append(h.reqs, req)
	h.mu.Unlock()
	return &Response{Status: "ok"}, nil
}

func TestWatchHubAggregatesConsumersAndRejectsStaleEpoch(t *testing.T) {
	hem := &watchHem{}
	h := &watchHub{hem: hem, groups: make(map[string]*qewWatchGroup), byID: make(map[string]string), now: time.Now}
	first, err := h.open("tab-a", 1, "session")
	if err != nil {
		t.Fatal(err)
	}

	second, err := h.open("tab-b", 1, "session")
	if err != nil {
		t.Fatal(err)
	}
	hem.mu.Lock()
	if len(hem.reqs) != 1 {
		t.Fatalf("upstream opens = %d, want 1", len(hem.reqs))
	}
	hem.mu.Unlock()
	if h.close(first.id, "tab-a", 1) != true {
		t.Fatal("first close failed")
	}
	hem.mu.Lock()
	if len(hem.reqs) != 1 {
		t.Fatalf("early close unsubscribed upstream: %d requests", len(hem.reqs))
	}
	hem.mu.Unlock()
	if _, err := h.renew(second.id, "tab-b", 2); err == nil {
		t.Fatal("stale epoch renewal succeeded")
	}
	if h.close(second.id, "tab-b", 1) != true {
		t.Fatal("last close failed")
	}
	time.Sleep(10 * time.Millisecond)
	hem.mu.Lock()
	defer hem.mu.Unlock()
	if len(hem.reqs) != 2 || hem.reqs[1].Verb != "unwatch" {
		t.Fatalf("upstream close requests = %+v", hem.reqs)
	}
}

func TestWatchHubCloseLastAndImmediateOpenKeepsRenewalGeneration(t *testing.T) {
	hem := &watchHem{}
	h := &watchHub{
		hem: hem, groups: make(map[string]*qewWatchGroup), byID: make(map[string]string),
		now: time.Now, renewalInterval: time.Millisecond, leaseDuration: time.Second,
	}
	first, err := h.open("tab-a", 1, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if !h.close(first.id, "tab-a", 1) {
		t.Fatal("initial close failed")
	}
	second, err := h.open("tab-b", 2, "session-b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.close(second.id, "tab-b", 2)
		h.closeHub()
	})
	time.Sleep(10 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.renewing || len(h.groups) != 1 || len(h.groups["session-b"].consumers) != 1 {
		t.Fatalf("renewal generation lost after close/open: renewing=%v groups=%d", h.renewing, len(h.groups))
	}
}
