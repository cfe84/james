// Package channel connects moneypenny sessions to external communication
// channels (Teams chats today; email/whatsapp later). It provides a small
// JSON-RPC-over-stdio MCP client used to drive provider tools (e.g. the Teams
// tools exposed by `agency mcp teams`) and the Provider abstraction the handler
// polls and replies through.
package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// mcpClient is a minimal Model Context Protocol client speaking JSON-RPC 2.0
// over a subprocess's stdio. It is intentionally dependency-free (moneypenny
// avoids heavy SDKs) and supports just what channel providers need:
// initialize + tools/call. The subprocess is (re)spawned lazily and restarted
// with backoff after a failure.
type mcpClient struct {
	command string   // e.g. "agency"
	args    []string // e.g. ["mcp", "teams"]

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	nextID   int
	ready    bool
	lastErr  error
	backoff  time.Time // don't respawn before this time
	callWait time.Duration
}

func newMCPClient(command string, args ...string) *mcpClient {
	return &mcpClient{command: command, args: args, callWait: 60 * time.Second}
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// ensureStarted spawns the subprocess and performs the initialize handshake if
// not already connected. Callers must hold c.mu.
func (c *mcpClient) ensureStarted() error {
	if c.ready {
		return nil
	}
	if time.Now().Before(c.backoff) {
		if c.lastErr != nil {
			return c.lastErr
		}
		return fmt.Errorf("mcp client backing off")
	}

	cmd := exec.Command(c.command, c.args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return c.fail(fmt.Errorf("mcp stdin: %w", err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return c.fail(fmt.Errorf("mcp stdout: %w", err))
	}
	// Subprocess diagnostics on stderr are noisy; discard.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return c.fail(fmt.Errorf("start %q: %w", c.command, err))
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReaderSize(stdout, 1<<20)
	c.nextID = 0

	// initialize handshake.
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "moneypenny", "version": "1"},
	}
	if _, err := c.rpc("initialize", initParams); err != nil {
		c.stop()
		return c.fail(fmt.Errorf("mcp initialize: %w", err))
	}
	// notifications/initialized (no response expected).
	_ = c.notify("notifications/initialized", map[string]interface{}{})

	c.ready = true
	c.lastErr = nil
	return nil
}

// fail records an error, tears down any subprocess, and sets a backoff window.
// Callers must hold c.mu. Returns the error for convenience.
func (c *mcpClient) fail(err error) error {
	c.lastErr = err
	c.backoff = time.Now().Add(15 * time.Second)
	c.stop()
	return err
}

// stop terminates the subprocess. Callers must hold c.mu.
func (c *mcpClient) stop() {
	c.ready = false
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		go c.cmd.Wait() //nolint:errcheck // reap async
	}
	c.cmd = nil
	c.stdin = nil
	c.stdout = nil
}

// rpc sends a request and reads responses until it finds the matching id,
// skipping any interleaved notifications. Callers must hold c.mu.
func (c *mcpClient) rpc(method string, params interface{}) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("write rpc: %w", err)
	}

	// Capture the reader locally: on a timeout we tear down and respawn the
	// subprocess (assigning a fresh c.stdout), so a still-blocked reader
	// goroutine keeps reading the *old* reader and can never race the new one.
	reader := c.stdout
	deadline := time.Now().Add(c.callWait)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("mcp %s: timed out", method)
		}
		raw, err := readLineWithTimeout(reader, remaining)
		if err != nil {
			return nil, fmt.Errorf("read rpc: %w", err)
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			// Non-JSON or unrelated log line; skip.
			continue
		}
		if resp.ID != id {
			// Response to a different request or a notification; skip.
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// readLineWithTimeout reads a single newline-terminated line from r, bounding the
// blocking read so a silent/hung subprocess cannot deadlock the caller (which
// holds the client mutex). On timeout the orphaned goroutine unblocks naturally
// once the subprocess pipe is closed during teardown.
func readLineWithTimeout(r *bufio.Reader, timeout time.Duration) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := r.ReadString('\n')
		ch <- result{s, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.s, res.err
	case <-timer.C:
		return "", fmt.Errorf("read timed out")
	}
}

// notify sends a fire-and-forget JSON-RPC notification. Callers must hold c.mu.
func (c *mcpClient) notify(method string, params interface{}) error {
	msg := map[string]interface{}{"jsonrpc": "2.0", "method": method, "params": params}
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(line, '\n'))
	return err
}

// mcpContent is a single content block in a tools/call result.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError"`
}

// callTool invokes an MCP tool and returns its concatenated text content. If the
// tool reports an error (isError), the text is returned as an error so callers
// (and ultimately the user) see the provider's message verbatim — including
// auth prompts from `agency`.
func (c *mcpClient) callTool(_ context.Context, name string, args map[string]interface{}) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureStarted(); err != nil {
		return "", err
	}

	raw, err := c.rpc("tools/call", map[string]interface{}{"name": name, "arguments": args})
	if err != nil {
		// A transport error likely means the subprocess died; force a restart
		// on the next call and surface the error now.
		_ = c.fail(err)
		return "", err
	}

	var res toolCallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("mcp %s: bad result: %w", name, err)
	}
	var sb strings.Builder
	for _, ct := range res.Content {
		// agency appends a telemetry trailer content block
		// ("CorrelationId: ..., TimeStamp: ...") to tool results. It is not part
		// of the payload and would corrupt JSON parsing if concatenated with the
		// data block, so drop it.
		if strings.HasPrefix(strings.TrimSpace(ct.Text), "CorrelationId:") {
			continue
		}
		sb.WriteString(ct.Text)
	}
	text := sb.String()
	if res.IsError {
		msg := strings.TrimSpace(text)
		if msg == "" {
			msg = "unknown tool error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return text, nil
}

// Close tears down the subprocess.
func (c *mcpClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stop()
}
