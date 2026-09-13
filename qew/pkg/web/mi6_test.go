package web

import (
	"io"
	"log"
	"testing"
)

func TestSubscribeUsesConnectionScopedIdentityAndIdempotentUnsubscribe(t *testing.T) {
	client := NewMI6Client("", "", "", log.New(io.Discard, "", 0))

	first, unsubscribeFirst := client.Subscribe()
	second, unsubscribeSecond := client.Subscribe()

	if len(client.subscribers) != 2 {
		t.Fatalf("subscriber count = %d, want 2", len(client.subscribers))
	}

	// Each socket gets a distinct subscription identity, even when it is
	// backed by the same MI6 client.
	var ids []uint64
	for sub := range client.subscribers {
		ids = append(ids, sub.id)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("subscription identities = %v, want two distinct IDs", ids)
	}

	unsubscribeFirst()
	unsubscribeFirst()
	if len(client.subscribers) != 1 {
		t.Fatalf("subscriber count after repeated unsubscribe = %d, want 1", len(client.subscribers))
	}
	select {
	case _, ok := <-first:
		if ok {
			t.Fatal("first subscription channel remains open")
		}
	default:
		t.Fatal("first subscription channel was not closed")
	}

	unsubscribeSecond()
	if len(client.subscribers) != 0 {
		t.Fatalf("subscriber count after cleanup = %d, want 0", len(client.subscribers))
	}
	select {
	case _, ok := <-second:
		if ok {
			t.Fatal("second subscription channel remains open")
		}
	default:
		t.Fatal("second subscription channel was not closed")
	}
}
