package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	maxLogLines         = 10_000
	logLinesToDiscard   = 1_000
	logRotationInterval = time.Minute
)

// startLogRotation checks immediately and then periodically. It trims the
// existing file in place so stdout/stderr append handles remain valid.
func startLogRotation(ctx context.Context, path string, reportError func(string, ...interface{})) {
	if path == "" {
		return
	}
	if err := pruneLogFile(path); err != nil {
		reportError("log rotation: %v", err)
	}

	go func() {
		ticker := time.NewTicker(logRotationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := pruneLogFile(path); err != nil {
					reportError("log rotation: %v", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// pruneLogFile removes the oldest logLinesToDiscard lines when path contains
// at least maxLogLines lines. Keeping the file inode avoids breaking the
// stdout/stderr file handles inherited by child agent processes.
func pruneLogFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	lineCount := bytes.Count(content, []byte{'\n'})
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lineCount++
	}
	if lineCount < maxLogLines {
		return nil
	}

	offset := 0
	for discarded := 0; discarded < logLinesToDiscard; discarded++ {
		next := bytes.IndexByte(content[offset:], '\n')
		if next < 0 {
			return nil
		}
		offset += next + 1
	}
	remaining := content[offset:]
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate %s: %w", path, err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek %s: %w", path, err)
	}
	if n, err := f.Write(remaining); err != nil {
		return fmt.Errorf("rewrite %s: %w", path, err)
	} else if n != len(remaining) {
		return fmt.Errorf("rewrite %s: %w", path, io.ErrShortWrite)
	}
	return nil
}
