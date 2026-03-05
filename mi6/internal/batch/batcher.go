package batch

import (
	"bytes"
	"context"
	"io"
	"time"
)

// Batcher reads from an io.Reader (stdin) and calls a flush function with accumulated data.
// It flushes on any of these triggers:
//  1. Newline detected in input
//  2. Buffer reaches MaxSize (default 4096 bytes)
//  3. Idle timeout since last byte received (default 100ms)
//
// The batcher runs until the reader returns EOF or an error.
type Batcher struct {
	MaxSize     int
	IdleTimeout time.Duration
}

// New creates a Batcher with the given max buffer size and idle timeout.
func New(maxSize int, idleTimeout time.Duration) *Batcher {
	return &Batcher{
		MaxSize:     maxSize,
		IdleTimeout: idleTimeout,
	}
}

type readResult struct {
	data []byte
	err  error
}

// Run reads from r and calls flush with batched data. It blocks until r
// returns EOF or ctx is cancelled.
func (b *Batcher) Run(ctx context.Context, r io.Reader, flush func([]byte) error) error {
	ch := make(chan readResult)

	// Reader goroutine: reads small chunks and sends them on ch.
	go func() {
		defer close(ch)
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case ch <- readResult{data: data}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case ch <- readResult{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	var buf []byte
	idleTimer := time.NewTimer(b.IdleTimeout)
	idleTimer.Stop()
	defer idleTimer.Stop()

	doFlush := func() error {
		if len(buf) == 0 {
			return nil
		}
		out := buf
		buf = nil
		return flush(out)
	}

	for {
		select {
		case <-ctx.Done():
			_ = doFlush()
			return ctx.Err()

		case res, ok := <-ch:
			if !ok {
				// Channel closed without an explicit error (shouldn't happen
				// given our goroutine, but handle defensively).
				return doFlush()
			}

			if res.err != nil {
				if res.err == io.EOF {
					return doFlush()
				}
				_ = doFlush()
				return res.err
			}

			buf = append(buf, res.data...)

			// Reset idle timer on every received chunk.
			idleTimer.Stop()
			// Drain if needed (non-blocking).
			select {
			case <-idleTimer.C:
			default:
			}
			idleTimer.Reset(b.IdleTimeout)

			// Check for newlines – flush up to and including each one.
			for {
				idx := bytes.IndexByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := buf[:idx+1]
				buf = buf[idx+1:]
				if err := flush(line); err != nil {
					return err
				}
			}

			// Check for buffer-full condition on remaining data.
			for len(buf) >= b.MaxSize {
				chunk := buf[:b.MaxSize]
				buf = buf[b.MaxSize:]
				if err := flush(chunk); err != nil {
					return err
				}
			}

		case <-idleTimer.C:
			if err := doFlush(); err != nil {
				return err
			}
		}
	}
}
