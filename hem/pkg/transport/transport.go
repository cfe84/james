package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Command is the JSON envelope sent to moneypenny.
type Command struct {
	Type      string      `json:"type"`
	Method    string      `json:"method"`
	RequestID string      `json:"request_id"`
	Data      interface{} `json:"data"`
}

// Response is the JSON envelope received from moneypenny.
type Response struct {
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	RequestID string          `json:"request_id"`
	ErrorCode string          `json:"error_code,omitempty"`
	Data      json.RawMessage `json:"data"`
}

// Client communicates with a moneypenny instance.
type Client struct {
	transportType string // "fifo" or "mi6"
	fifoIn        string // write commands here (moneypenny reads from it)
	fifoOut       string // read responses here (moneypenny writes to it)
	fifoMu        sync.Mutex // serialise FIFO requests (no concurrent writes)
	mi6Addr       string     // for mi6 transport
	mi6KeyPath    string     // SSH key for mi6
	mi6Mu         sync.Mutex // serialise MI6 requests (concurrent clients cause response mixing)
}

// NewFIFOClient creates a client that communicates via named pipes.
// fifoIn is the path to moneypenny's input FIFO (we write to it).
// fifoOut is the path to moneypenny's output FIFO (we read from it).
func NewFIFOClient(fifoIn, fifoOut string) *Client {
	return &Client{
		transportType: "fifo",
		fifoIn:        fifoIn,
		fifoOut:       fifoOut,
	}
}

// NewFIFOClientFromFolder creates a FIFO client using standard names in a folder.
func NewFIFOClientFromFolder(folder string) *Client {
	return NewFIFOClient(
		filepath.Join(folder, "moneypenny-in"),
		filepath.Join(folder, "moneypenny-out"),
	)
}

// NewMI6Client creates a client that communicates via MI6.
func NewMI6Client(mi6Addr, keyPath string) *Client {
	return &Client{
		transportType: "mi6",
		mi6Addr:       mi6Addr,
		mi6KeyPath:    keyPath,
	}
}

// Send sends a command to moneypenny and returns the response.
func (c *Client) Send(ctx context.Context, cmd *Command) (*Response, error) {
	switch c.transportType {
	case "fifo":
		return c.sendFIFO(ctx, cmd)
	case "mi6":
		return c.sendMI6(ctx, cmd)
	default:
		return nil, fmt.Errorf("unknown transport: %s", c.transportType)
	}
}

func (c *Client) sendFIFO(ctx context.Context, cmd *Command) (*Response, error) {
	// Serialise FIFO access — concurrent writes can interleave and corrupt the
	// line-based protocol (especially for messages larger than PIPE_BUF).
	c.fifoMu.Lock()
	defer c.fifoMu.Unlock()

	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	// Try to open the write FIFO with O_NONBLOCK first to detect if moneypenny
	// is running. O_WRONLY|O_NONBLOCK returns ENXIO if no reader is connected.
	inFile, err := os.OpenFile(c.fifoIn, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if isENXIO(err) {
			return nil, fmt.Errorf("moneypenny is not running (no reader on FIFO)")
		}
		return nil, fmt.Errorf("opening fifo-in: %w", err)
	}

	// Open the output FIFO with O_NONBLOCK to avoid hanging forever if
	// moneypenny dies between the write-open and the read-open. On macOS/Linux,
	// O_RDONLY|O_NONBLOCK on a FIFO succeeds immediately without waiting for a
	// writer. We then clear nonblock so reads block normally. If moneypenny is
	// gone, the scanner will get EOF instead of hanging forever.
	outFile, err := os.OpenFile(c.fifoOut, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		inFile.Close()
		return nil, fmt.Errorf("opening fifo-out: %w", err)
	}
	defer outFile.Close()
	// Clear O_NONBLOCK so reads block normally waiting for data.
	clearNonBlock(int(outFile.Fd()))

	// Write command.
	if _, err := inFile.Write(data); err != nil {
		inFile.Close()
		outFile.Close()
		return nil, fmt.Errorf("writing to fifo-in: %w", err)
	}
	inFile.Close()

	// Read response with context deadline.
	scanCh := make(chan *Response, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(outFile)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // up to 16MB responses
		if !scanner.Scan() {
			errCh <- fmt.Errorf("no response from moneypenny")
			return
		}
		var resp Response
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			errCh <- fmt.Errorf("parsing response: %w", err)
			return
		}
		scanCh <- &resp
	}()

	select {
	case resp := <-scanCh:
		return resp, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		// Close outFile to unblock the scanner goroutine.
		outFile.Close()
		return nil, fmt.Errorf("timed out waiting for moneypenny response")
	}
}

// isENXIO checks if an error is an ENXIO syscall error (no reader/writer on FIFO).
func isENXIO(err error) bool {
	if pe, ok := err.(*os.PathError); ok {
		if errno, ok := pe.Err.(syscall.Errno); ok {
			return errno == syscall.ENXIO
		}
	}
	return false
}

func (c *Client) sendMI6(ctx context.Context, cmd *Command) (*Response, error) {
	// Serialise MI6 access — multiple mi6-client processes joining the same
	// MI6 session causes the relay to broadcast responses to all participants,
	// so concurrent requests would receive each other's responses.
	c.mi6Mu.Lock()
	defer c.mi6Mu.Unlock()

	mi6Client, err := findMI6Client()
	if err != nil {
		return nil, err
	}

	proc := exec.CommandContext(ctx, mi6Client, "--key", c.mi6KeyPath, c.mi6Addr)
	var stderrBuf bytes.Buffer
	proc.Stderr = &stderrBuf

	stdin, err := proc.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := proc.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := proc.Start(); err != nil {
		return nil, fmt.Errorf("starting mi6-client: %w", err)
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		proc.Wait()
		return nil, err
	}
	data = append(data, '\n')
	stdin.Write(data)

	// Don't close stdin yet — closing it triggers mi6-client shutdown
	// (stdin EOF → cancel → exit) before the response can arrive back
	// through the MI6 relay.

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // up to 16MB responses
	if !scanner.Scan() {
		stdin.Close()
		waitErr := proc.Wait()
		errParts := []string{"no response from moneypenny via MI6"}
		if se := scanner.Err(); se != nil {
			errParts = append(errParts, fmt.Sprintf("scan: %v", se))
		}
		if waitErr != nil {
			errParts = append(errParts, fmt.Sprintf("exit: %v", waitErr))
		}
		if stderr := strings.TrimSpace(stderrBuf.String()); stderr != "" {
			errParts = append(errParts, fmt.Sprintf("stderr: %s", stderr))
		}
		return nil, fmt.Errorf("%s", strings.Join(errParts, "; "))
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		stdin.Close()
		proc.Wait()
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	stdin.Close()
	proc.Wait()
	return &resp, nil
}

// TestMI6 tests connectivity to an MI6 server by spawning mi6-client and
// verifying it can connect and join the session. Returns nil on success.
func TestMI6(ctx context.Context, mi6Addr, keyPath string) error {
	mi6Client, err := findMI6Client()
	if err != nil {
		return err
	}

	proc := exec.CommandContext(ctx, mi6Client, "--key", keyPath, mi6Addr)
	proc.Stderr = os.Stderr

	stdin, err := proc.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	if err := proc.Start(); err != nil {
		return fmt.Errorf("starting mi6-client: %w", err)
	}

	// Close stdin immediately — mi6-client will connect, authenticate,
	// join the session, then exit when stdin closes.
	stdin.Close()

	if err := proc.Wait(); err != nil {
		return fmt.Errorf("mi6-client exited with error: %w", err)
	}

	return nil
}

func findMI6Client() (string, error) {
	if path, err := exec.LookPath("mi6-client"); err == nil {
		return path, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "mi6-client")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("mi6-client not found")
}
