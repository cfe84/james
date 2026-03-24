package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var qewReqCounter uint64

// Request matches the hem server protocol.
type Request struct {
	Verb      string   `json:"verb"`
	Noun      string   `json:"noun"`
	Args      []string `json:"args"`
	RequestID string   `json:"request_id,omitempty"`
}

// Response matches the hem server protocol.
type Response struct {
	Status    string          `json:"status"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Verb      string          `json:"verb,omitempty"` // for broadcast identification
	Noun      string          `json:"noun,omitempty"` // for broadcast identification
}

// HemClient sends requests to a Hem server and returns responses.
type HemClient interface {
	Send(req *Request) (*Response, error)
}

// BroadcastHemClient extends HemClient with broadcast subscription support.
type BroadcastHemClient interface {
	HemClient
	Subscribe() (<-chan *Response, func()) // returns broadcast channel and unsubscribe function
}

// SocketClient connects to a Hem server via Unix domain socket (one connection per request).
type SocketClient struct {
	SockPath string
}

func (c *SocketClient) Send(req *Request) (*Response, error) {
	conn, err := net.Dial("unix", c.SockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to hem: %w", err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("writing request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from hem server")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &resp, nil
}

// MI6Client connects to a Hem server via MI6 and provides request/response communication.
type MI6Client struct {
	addr    string
	keyPath string
	vlog    *log.Logger

	mu           sync.Mutex
	cmd          *exec.Cmd
	stdin        *json.Encoder
	scanner      *bufio.Scanner
	pending      map[string]chan *Response // request_id -> response channel
	ready        chan struct{}
	broadcastCh  chan *Response            // broadcasts to all WebSocket clients
	broadcastMu  sync.RWMutex
	subscribers  map[*websocketSubscriber]struct{} // WebSocket clients listening for broadcasts
}

type websocketSubscriber struct {
	ch chan *Response
}

// NewMI6Client creates a client that talks to Hem over MI6.
func NewMI6Client(addr, keyPath string, vlog *log.Logger) *MI6Client {
	return &MI6Client{
		addr:        addr,
		keyPath:     keyPath,
		vlog:        vlog,
		pending:     make(map[string]chan *Response),
		ready:       make(chan struct{}),
		broadcastCh: make(chan *Response, 100),
		subscribers: make(map[*websocketSubscriber]struct{}),
	}
}

// Start connects to MI6 and begins reading responses. Reconnects automatically.
func (c *MI6Client) Start() error {
	go c.runLoop()
	// Wait for first connection.
	select {
	case <-c.ready:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout connecting to MI6 at %s", c.addr)
	}
}

func (c *MI6Client) runLoop() {
	firstConnect := true
	for {
		err := c.connect()
		if err != nil {
			c.vlog.Printf("MI6 connect error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if firstConnect {
			close(c.ready)
			firstConnect = false
		}
		c.readResponses()
		c.vlog.Printf("MI6 connection lost, reconnecting...")
		time.Sleep(2 * time.Second)
	}
}

func (c *MI6Client) connect() error {
	mi6Client, err := findMI6Client()
	if err != nil {
		return err
	}

	cmd := exec.Command(mi6Client, "--key", c.keyPath, c.addr)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mi6-client: %w", err)
	}

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = json.NewEncoder(stdin)
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	c.mu.Unlock()

	c.vlog.Printf("MI6 connected to %s", c.addr)
	return nil
}

func (c *MI6Client) readResponses() {
	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// Not a valid response (e.g. request from another client); skip.
			continue
		}

		// Route message based on RequestID.
		if resp.RequestID != "" {
			// Check if it's a response to one of our pending requests.
			c.mu.Lock()
			ch, ok := c.pending[resp.RequestID]
			c.mu.Unlock()
			if ok {
				ch <- &resp
				continue
			}
		}

		// No pending request or no RequestID → treat as broadcast.
		// Forward to all WebSocket subscribers.
		c.broadcastMu.RLock()
		for sub := range c.subscribers {
			select {
			case sub.ch <- &resp:
			default:
				// Subscriber channel full, drop message.
			}
		}
		c.broadcastMu.RUnlock()
	}
}

// Subscribe creates a subscription for broadcast messages.
// Returns a channel that receives broadcasts and an unsubscribe function.
func (c *MI6Client) Subscribe() (<-chan *Response, func()) {
	sub := &websocketSubscriber{
		ch: make(chan *Response, 50),
	}

	c.broadcastMu.Lock()
	c.subscribers[sub] = struct{}{}
	c.broadcastMu.Unlock()

	unsubscribe := func() {
		c.broadcastMu.Lock()
		delete(c.subscribers, sub)
		close(sub.ch)
		c.broadcastMu.Unlock()
	}

	return sub.ch, unsubscribe
}

// Send sends a request to Hem via MI6 and waits for a response.
func (c *MI6Client) Send(req *Request) (*Response, error) {
	id := fmt.Sprintf("qew-%d-%d", os.Getpid(), atomic.AddUint64(&qewReqCounter, 1))
	req.RequestID = id

	ch := make(chan *Response, 1)
	c.mu.Lock()
	if c.stdin == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("not connected")
	}
	c.pending[id] = ch
	err := c.stdin.Encode(req)
	c.mu.Unlock()

	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("sending request: %w", err)
	}

	select {
	case resp := <-ch:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return resp, nil
	case <-time.After(30 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for response")
	}
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
