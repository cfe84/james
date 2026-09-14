package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Command is the JSON envelope sent from hem to moneypenny over FIFO or MI6.
// This is the hem-server ↔ moneypenny protocol.
//
// Note: This is similar to moneypenny/pkg/envelope.Command but kept separate to avoid
// cross-module dependencies between hem and moneypenny. Each module defines its own
// protocol types for its architectural layer.
type Command struct {
	Type      string      `json:"type"`
	Method    string      `json:"method"`
	RequestID string      `json:"request_id"`
	Data      interface{} `json:"data"`
}

// Response is the JSON envelope received from moneypenny.
// Nearly identical to moneypenny/pkg/envelope.Response by design (same wire format),
// but defined separately for module isolation.
type Response struct {
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	RequestID string          `json:"request_id"`
	ErrorCode string          `json:"error_code,omitempty"`
	Data      json.RawMessage `json:"data"`
	Event     string          `json:"event,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
}

// EventSubscriber receives unsolicited Moneypenny notifications from a
// persistent MI6 connection. Events are hints; callers must re-read state.
type EventSubscriber struct {
	ch     chan *Response
	closed sync.Once
}

// Client communicates with a moneypenny instance.
type Client struct {
	transportType        string     // "fifo" or "mi6"
	fifoIn               string     // write commands here (moneypenny reads from it)
	fifoOut              string     // read responses here (moneypenny writes to it)
	fifoMu               sync.Mutex // serialise FIFO requests (no concurrent writes)
	mi6Addr              string     // for mi6 transport
	mi6KeyPath           string     // SSH key for mi6
	mi6ServerFingerprint string     // expected MI6 server fingerprint
	mi6Mu                sync.Mutex
	mi6Conn              *mi6Connection
	mi6StartOnce         sync.Once
	mi6Started           atomic.Bool
	eventMu              sync.RWMutex
	eventSubs            map[*EventSubscriber]struct{}
}

type mi6Connection struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	pending  map[string]chan *Response
	eventsMu sync.RWMutex
	events   map[*EventSubscriber]struct{}
	done     chan struct{}
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

// NewMI6Client creates a client that communicates via MI6.
func NewMI6Client(mi6Addr, keyPath, serverFingerprint string) *Client {
	return &Client{
		transportType:        "mi6",
		mi6Addr:              mi6Addr,
		mi6KeyPath:           keyPath,
		mi6ServerFingerprint: serverFingerprint,
		eventSubs:            make(map[*EventSubscriber]struct{}),
	}
}

// Start begins the reconnecting MI6 reader. It is safe to call more than once.
func (c *Client) Start(ctx context.Context) {
	if c.transportType != "mi6" {
		return
	}
	c.mi6Started.Store(true)
	c.mi6StartOnce.Do(func() {
		go c.mi6Run(ctx)
	})
}

// Subscribe registers a bounded notification consumer. Unsubscribe is
// idempotent and closes only this consumer's channel.
func (c *Client) Subscribe() (<-chan *Response, func()) {
	sub := &EventSubscriber{ch: make(chan *Response, 32)}
	c.eventMu.Lock()
	c.eventSubs[sub] = struct{}{}
	c.eventMu.Unlock()
	c.mi6Mu.Lock()
	conn := c.mi6Conn
	c.mi6Mu.Unlock()
	if conn != nil {
		conn.eventsMu.Lock()
		conn.events[sub] = struct{}{}
		conn.eventsMu.Unlock()
	}
	return sub.ch, func() {
		sub.closed.Do(func() {
			c.eventMu.Lock()
			delete(c.eventSubs, sub)
			c.eventMu.Unlock()
			if conn != nil {
				conn.eventsMu.Lock()
				delete(conn.events, sub)
				conn.eventsMu.Unlock()
			}
			close(sub.ch)
		})
	}
}

// SendCommand sends a command with the given method and data to moneypenny.
// It applies default timeout, builds the command envelope, and handles the response.
func (c *Client) SendCommand(ctx context.Context, method string, data interface{}) (*Response, error) {
	// Apply a default 60-second timeout if the caller didn't set a deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}

	// Build command envelope.
	cmd := &Command{
		Type:      "request",
		Method:    method,
		RequestID: generateRequestID(),
		Data:      data,
	}

	return c.Send(ctx, cmd)
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

var requestSequence uint64

// generateRequestID generates a unique request ID for command tracking.
func generateRequestID() string {
	return fmt.Sprintf("%d-%d-%d", time.Now().UnixNano(), os.Getpid(), atomic.AddUint64(&requestSequence, 1))
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
	if cmd.RequestID == "" {
		return nil, fmt.Errorf("MI6 command requires a request ID")
	}
	if !c.mi6Started.Load() {
		return c.sendMI6Once(ctx, cmd)
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	for {
		c.mi6Mu.Lock()
		conn := c.mi6Conn
		if conn != nil && conn.stdin != nil {
			ch := make(chan *Response, 1)
			conn.pending[cmd.RequestID] = ch
			_, writeErr := conn.stdin.Write(data)
			c.mi6Mu.Unlock()
			if writeErr != nil {
				c.failMI6(conn, writeErr)
			} else {
				select {
				case resp := <-ch:
					return resp, nil
				case <-ctx.Done():
					c.mi6Mu.Lock()
					delete(conn.pending, cmd.RequestID)
					c.mi6Mu.Unlock()
					return nil, ctx.Err()
				case <-conn.done:
					return nil, fmt.Errorf("MI6 connection lost")
				}
			}

		} else {
			c.mi6Mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *Client) sendMI6Once(ctx context.Context, cmd *Command) (*Response, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	mi6Client, err := findMI6Client()
	if err != nil {
		return nil, err
	}
	proc := exec.CommandContext(ctx, mi6Client, "--line-mode", "--key", c.mi6KeyPath, "--server-fingerprint", c.mi6ServerFingerprint, c.mi6Addr)
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
	if _, err := stdin.Write(data); err != nil {
		_ = stdin.Close()
		_ = proc.Process.Kill()
		_ = proc.Wait()
		return nil, fmt.Errorf("writing MI6 command: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var resp Response
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			_ = stdin.Close()
			_ = proc.Process.Kill()
			_ = proc.Wait()
			return nil, fmt.Errorf("parsing response: %w", err)
		}
		if resp.Type == "response" && resp.RequestID == cmd.RequestID {
			_ = stdin.Close()
			_ = proc.Wait()
			return &resp, nil
		}
	}
	_ = stdin.Close()
	waitErr := proc.Wait()
	detail := "no matching response from moneypenny via MI6"
	if se := scanner.Err(); se != nil {
		detail += fmt.Sprintf("; scan: %v", se)
	}
	if waitErr != nil {
		detail += fmt.Sprintf("; exit: %v", waitErr)
	}
	if stderr := strings.TrimSpace(stderrBuf.String()); stderr != "" {
		detail += fmt.Sprintf("; stderr: %s", stderr)
	}
	return nil, fmt.Errorf("%s", detail)
}

func (c *Client) mi6Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := c.connectMI6()
		if err != nil {
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		c.readMI6(conn)
		c.failMI6(conn, fmt.Errorf("MI6 connection closed"))
	}
}

func (c *Client) connectMI6() (*mi6Connection, error) {
	mi6Client, err := findMI6Client()
	if err != nil {
		return nil, err
	}
	proc := exec.Command(mi6Client, "--line-mode", "--key", c.mi6KeyPath, "--server-fingerprint", c.mi6ServerFingerprint, c.mi6Addr)
	stdin, err := proc.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := proc.StdoutPipe()
	if err != nil {
		return nil, err
	}
	proc.Stderr = os.Stderr
	if err := proc.Start(); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	conn := &mi6Connection{cmd: proc, stdin: stdin, scanner: scanner, pending: make(map[string]chan *Response), events: make(map[*EventSubscriber]struct{}), done: make(chan struct{})}
	c.eventMu.RLock()
	for sub := range c.eventSubs {
		conn.events[sub] = struct{}{}
	}
	c.eventMu.RUnlock()
	c.mi6Mu.Lock()
	c.mi6Conn = conn
	c.mi6Mu.Unlock()
	return conn, nil
}

func (c *Client) readMI6(conn *mi6Connection) {
	for conn.scanner.Scan() {
		var resp Response
		if err := json.Unmarshal(conn.scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.Type == "response" && resp.RequestID != "" {
			c.mi6Mu.Lock()
			ch := conn.pending[resp.RequestID]
			if ch != nil {
				delete(conn.pending, resp.RequestID)
			}
			c.mi6Mu.Unlock()
			if ch != nil {
				select {
				case ch <- &resp:
				default:
				}
			}
			continue
		}
		if resp.Type != "notification" {
			continue
		}
		conn.eventsMu.RLock()
		for sub := range conn.events {
			select {
			case sub.ch <- &resp:
			default:
				// Overflow is a recoverable hint loss; the consumer's
				// polling/resync path remains authoritative.
			}
		}
		conn.eventsMu.RUnlock()
	}
}

func (c *Client) failMI6(conn *mi6Connection, cause error) {
	c.mi6Mu.Lock()
	if c.mi6Conn != conn {
		c.mi6Mu.Unlock()
		return
	}
	conn.stdin = nil
	close(conn.done)
	for id := range conn.pending {
		delete(conn.pending, id)
	}
	c.mi6Conn = nil
	c.mi6Mu.Unlock()
	_ = cause
	if conn.cmd != nil && conn.cmd.Process != nil {
		_ = conn.cmd.Process.Kill()
		_ = conn.cmd.Wait()
	}
}

// TestMI6 tests connectivity to an MI6 server by spawning mi6-client and
// verifying it can connect and join the session. Returns nil on success.
func TestMI6(ctx context.Context, mi6Addr, keyPath, serverFingerprint string) error {
	mi6Client, err := findMI6Client()
	if err != nil {
		return err
	}

	proc := exec.CommandContext(ctx, mi6Client, "--key", keyPath, "--server-fingerprint", serverFingerprint, mi6Addr)
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

// MI6AdminCommand sends a single admin command to an MI6 server and returns the raw response.
// It connects via mi6-client with --admin-command (joining the __admin__ session).
func MI6AdminCommand(ctx context.Context, mi6Addr, keyPath, serverFingerprint, commandJSON string) ([]byte, error) {
	mi6Client, err := findMI6Client()
	if err != nil {
		return nil, err
	}

	// mi6-client HOST:PORT --key KEY --admin-command JSON
	// The server address needs to be passed as a positional arg with a dummy session
	// or via flags. Since --admin-command sets session to __admin__ internally,
	// we pass the host directly with a dummy session in the positional arg.
	addr := mi6Addr
	if !strings.Contains(addr, ":") {
		addr = addr + ":7007"
	}

	proc := exec.CommandContext(ctx, mi6Client, "--server", addr, "--session-id", "__admin__", "--key", keyPath, "--server-fingerprint", serverFingerprint, "--admin-command", commandJSON)
	var stdoutBuf, stderrBuf bytes.Buffer
	proc.Stdout = &stdoutBuf
	proc.Stderr = &stderrBuf

	if err := proc.Run(); err != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return nil, fmt.Errorf("mi6 admin command failed: %s", stderr)
		}
		return nil, fmt.Errorf("mi6 admin command failed: %w", err)
	}

	return stdoutBuf.Bytes(), nil
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
