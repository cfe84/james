package session

import (
	"sync"
	"testing"
	"time"
)

func TestJoinCreatesSessionAndReturnsClient(t *testing.T) {
	m := NewManager()
	c, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.ID == "" {
		t.Fatal("expected non-empty client ID")
	}
	if c.WriteCh == nil {
		t.Fatal("expected non-nil WriteCh")
	}
	if cap(c.WriteCh) != 256 {
		t.Fatalf("expected WriteCh buffer size 256, got %d", cap(c.WriteCh))
	}
	if m.ClientCount("s1") != 1 {
		t.Fatalf("expected 1 client, got %d", m.ClientCount("s1"))
	}
}

func TestTwoClientsJoinSameSession(t *testing.T) {
	m := NewManager()
	c1, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	c2, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if c1.ID == c2.ID {
		t.Fatal("expected different client IDs")
	}
	if m.ClientCount("s1") != 2 {
		t.Fatalf("expected 2 clients, got %d", m.ClientCount("s1"))
	}
}

func TestBroadcastDeliversToOthersNotSender(t *testing.T) {
	m := NewManager()
	c1, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	c2, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	c3, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}

	msg := []byte("hello")
	m.Broadcast("s1", c1.ID, msg)

	// c2 and c3 should receive
	select {
	case got := <-c2.WriteCh:
		if string(got) != "hello" {
			t.Fatalf("c2 got %q, want %q", got, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("c2 did not receive message")
	}

	select {
	case got := <-c3.WriteCh:
		if string(got) != "hello" {
			t.Fatalf("c3 got %q, want %q", got, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("c3 did not receive message")
	}

	// c1 (sender) should NOT receive
	select {
	case <-c1.WriteCh:
		t.Fatal("sender should not receive its own broadcast")
	default:
		// good
	}
}

func TestLeaveRemovesClientAndClosesWriteCh(t *testing.T) {
	m := NewManager()
	c1, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	c2, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}

	m.Leave("s1", c1.ID)

	// c1's WriteCh should be closed
	_, ok := <-c1.WriteCh
	if ok {
		t.Fatal("expected WriteCh to be closed")
	}

	if m.ClientCount("s1") != 1 {
		t.Fatalf("expected 1 client, got %d", m.ClientCount("s1"))
	}

	// c2 should still be there
	_ = c2
}

func TestLeaveLastClientDeletesSession(t *testing.T) {
	m := NewManager()
	c, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	m.Leave("s1", c.ID)

	if m.ClientCount("s1") != 0 {
		t.Fatalf("expected 0 clients, got %d", m.ClientCount("s1"))
	}

	// Verify session is actually deleted
	m.mu.RLock()
	_, exists := m.sessions["s1"]
	m.mu.RUnlock()
	if exists {
		t.Fatal("expected session to be deleted")
	}
}

func TestBroadcastSkipsFullChannels(t *testing.T) {
	m := NewManager()
	sender, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	slow, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}

	// Fill up slow consumer's channel
	for i := 0; i < 256; i++ {
		slow.WriteCh <- []byte("filler")
	}

	// Broadcast should not block
	done := make(chan struct{})
	go func() {
		m.Broadcast("s1", sender.ID, []byte("overflow"))
		close(done)
	}()

	select {
	case <-done:
		// good, did not block
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked on full channel")
	}
}

func TestClientCountReturnsCorrectValues(t *testing.T) {
	m := NewManager()

	if m.ClientCount("nonexistent") != 0 {
		t.Fatal("expected 0 for nonexistent session")
	}

	c1, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if m.ClientCount("s1") != 1 {
		t.Fatalf("expected 1, got %d", m.ClientCount("s1"))
	}

	c2, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if m.ClientCount("s1") != 2 {
		t.Fatalf("expected 2, got %d", m.ClientCount("s1"))
	}

	m.Leave("s1", c1.ID)
	if m.ClientCount("s1") != 1 {
		t.Fatalf("expected 1, got %d", m.ClientCount("s1"))
	}

	m.Leave("s1", c2.ID)
	if m.ClientCount("s1") != 0 {
		t.Fatalf("expected 0, got %d", m.ClientCount("s1"))
	}
}

func TestConcurrentJoinLeaveBroadcast(t *testing.T) {
	m := NewManager()
	const goroutines = 50
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			c, err := m.Join("s1")
			if err != nil {
				t.Fatalf("Join failed: %v", err)
			}
			m.Broadcast("s1", c.ID, []byte("data"))
			m.Leave("s1", c.ID)
		}()
	}

	wg.Wait()

	if m.ClientCount("s1") != 0 {
		t.Fatalf("expected 0 clients after all left, got %d", m.ClientCount("s1"))
	}
}

func TestExclusiveClientMustBeAlone(t *testing.T) {
	m := NewManager()

	// Exclusive client joins empty session - should succeed
	c1, err := m.JoinExclusive("s1")
	if err != nil {
		t.Fatalf("Expected exclusive join to succeed on empty session, got: %v", err)
	}
	if !c1.Exclusive {
		t.Fatal("Expected client to be marked as exclusive")
	}
	if m.ClientCount("s1") != 1 {
		t.Fatalf("Expected 1 client, got %d", m.ClientCount("s1"))
	}

	// Second exclusive client tries to join - should fail
	_, err = m.JoinExclusive("s1")
	if err == nil {
		t.Fatal("Expected exclusive join to fail when another client exists")
	}
	if err != ErrExclusiveConflict {
		t.Fatalf("Expected ErrExclusiveConflict, got %v", err)
	}

	// Non-exclusive client tries to join - should fail
	_, err = m.Join("s1")
	if err == nil {
		t.Fatal("Expected non-exclusive join to fail when exclusive client exists")
	}
	if err != ErrExclusiveConflict {
		t.Fatalf("Expected ErrExclusiveConflict, got %v", err)
	}

	// After exclusive client leaves, new client can join
	m.Leave("s1", c1.ID)
	c2, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Expected join to succeed after exclusive left, got: %v", err)
	}
	if c2.Exclusive {
		t.Fatal("Expected non-exclusive client")
	}
}

func TestNonExclusiveCannotJoinWhenExclusivePresent(t *testing.T) {
	m := NewManager()

	// Non-exclusive clients join first
	c1, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	c2, err := m.Join("s1")
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if m.ClientCount("s1") != 2 {
		t.Fatalf("Expected 2 clients, got %d", m.ClientCount("s1"))
	}

	// Exclusive client tries to join - should fail
	_, err = m.JoinExclusive("s1")
	if err == nil {
		t.Fatal("Expected exclusive join to fail when other clients exist")
	}
	if err != ErrExclusiveConflict {
		t.Fatalf("Expected ErrExclusiveConflict, got %v", err)
	}

	// After all non-exclusive clients leave, exclusive can join
	m.Leave("s1", c1.ID)
	m.Leave("s1", c2.ID)

	c3, err := m.JoinExclusive("s1")
	if err != nil {
		t.Fatalf("Expected exclusive join to succeed after all left, got: %v", err)
	}
	if !c3.Exclusive {
		t.Fatal("Expected exclusive client")
	}
}
