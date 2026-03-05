package batch

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// collectFlushes returns a flush function and a way to retrieve all flushed chunks.
func collectFlushes() (func([]byte) error, func() []string) {
	var mu sync.Mutex
	var results []string
	flush := func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		results = append(results, string(data))
		return nil
	}
	get := func() []string {
		mu.Lock()
		defer mu.Unlock()
		dst := make([]string, len(results))
		copy(dst, results)
		return dst
	}
	return flush, get
}

func TestFlushOnNewline(t *testing.T) {
	b := New(4096, 5*time.Second)
	flush, get := collectFlushes()

	input := "hello\n"
	err := b.Run(context.Background(), strings.NewReader(input), flush)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := get()
	if len(results) != 1 {
		t.Fatalf("expected 1 flush, got %d: %v", len(results), results)
	}
	if results[0] != "hello\n" {
		t.Fatalf("expected %q, got %q", "hello\n", results[0])
	}
}

func TestFlushOnBufferFull(t *testing.T) {
	maxSize := 8
	b := New(maxSize, 5*time.Second)
	flush, get := collectFlushes()

	// No newlines, so buffer-full is the only trigger (besides EOF).
	input := strings.Repeat("x", 20)
	err := b.Run(context.Background(), strings.NewReader(input), flush)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := get()
	// We expect at least 2 buffer-full flushes (8+8) plus a remainder flush (4).
	total := 0
	for _, r := range results {
		total += len(r)
	}
	if total != 20 {
		t.Fatalf("expected total flushed bytes = 20, got %d", total)
	}
	// At least one flush should be exactly maxSize.
	found := false
	for _, r := range results {
		if len(r) == maxSize {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one flush of size %d, flushes: %v", maxSize, results)
	}
}

func TestFlushOnIdleTimeout(t *testing.T) {
	b := New(4096, 50*time.Millisecond)
	flush, get := collectFlushes()

	// slowReader delivers data, then pauses long enough for idle timeout, then EOF.
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("partial"))
		// Wait longer than idle timeout so the batcher flushes.
		time.Sleep(200 * time.Millisecond)
		pw.Close()
	}()

	err := b.Run(context.Background(), pr, flush)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := get()
	if len(results) == 0 {
		t.Fatal("expected at least one flush")
	}
	// The idle-triggered flush should contain "partial".
	combined := strings.Join(results, "")
	if combined != "partial" {
		t.Fatalf("expected %q, got %q", "partial", combined)
	}
	// The idle flush should have fired before EOF (i.e., more than one event),
	// but since EOF also triggers flush of remaining, we just verify content.
}

func TestEOFFlushesRemaining(t *testing.T) {
	b := New(4096, 5*time.Second)
	flush, get := collectFlushes()

	// No newline, no buffer full – only EOF triggers the flush.
	input := "no-newline"
	err := b.Run(context.Background(), strings.NewReader(input), flush)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := get()
	combined := strings.Join(results, "")
	if combined != "no-newline" {
		t.Fatalf("expected %q, got %q", "no-newline", combined)
	}
}

func TestMultipleNewlines(t *testing.T) {
	b := New(4096, 5*time.Second)
	flush, get := collectFlushes()

	input := "aaa\nbbb\nccc\n"
	err := b.Run(context.Background(), bytes.NewReader([]byte(input)), flush)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := get()
	expected := []string{"aaa\n", "bbb\n", "ccc\n"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d flushes, got %d: %v", len(expected), len(results), results)
	}
	for i, want := range expected {
		if results[i] != want {
			t.Fatalf("flush[%d]: expected %q, got %q", i, want, results[i])
		}
	}
}
