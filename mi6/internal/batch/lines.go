package batch

import (
	"bufio"
	"context"
	"fmt"
	"io"
)

// RunLines emits each complete newline-delimited record in a single frame.
// Never flush partial records on idle, EOF, size overflow, or cancellation:
// another sender (or a newly joined reader) can otherwise splice the stream.
func RunLines(ctx context.Context, r io.Reader, maxSize int, flush func([]byte) error) error {
	if maxSize < 1 {
		return fmt.Errorf("line size limit must be positive")
	}
	reader := bufio.NewReaderSize(r, 4096)
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > maxSize {
			return fmt.Errorf("record exceeds %d-byte MI6 line limit", maxSize)
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF {
				if len(line) != 0 {
					return fmt.Errorf("incomplete record at EOF: missing newline")
				}
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := flush(line); err != nil {
			return err
		}
		line = nil
	}
}
