package hemclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"james/hem/pkg/protocol"
)

var reqCounter uint64

// Sender sends requests to a hem server and returns responses.
type Sender interface {
	Send(req *protocol.Request) (*protocol.Response, error)
}

// BroadcastSender extends Sender with broadcast message support.
type BroadcastSender interface {
	Sender
	Broadcasts() <-chan *protocol.Response // read-only channel for broadcast messages
}

// SocketSender sends requests over a Unix domain socket (one connection per request).
type SocketSender struct {
	SockPath string
}

func (s *SocketSender) Send(req *protocol.Request) (*protocol.Response, error) {
	conn, err := net.Dial("unix", s.SockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to hem server at %s: %w (is the server running? start it with 'hem start server')", s.SockPath, err)
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
		return nil, fmt.Errorf("no response from server")
	}

	var resp protocol.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &resp, nil
}

// MI6Sender sends requests over an MI6 session (persistent connection).
type MI6Sender struct {
	Addr    string
	KeyPath string

	mu           sync.Mutex
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	scanner      *bufio.Scanner
	stderrBuf    bytes.Buffer
	waitCh       chan struct{}                // closed when mi6-client process exits
	waitErr      error
	broadcastCh  chan *protocol.Response      // channel for broadcasting messages to listeners
	pendingResps map[string]chan *protocol.Response // pending requests waiting for responses
}

// Connect establishes the MI6 connection. Must be called before Send.
func (s *MI6Sender) Connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectInternal()
}

// connectInternal starts mi6-client and sets up pipes. Caller must hold s.mu.
func (s *MI6Sender) connectInternal() error {
	mi6Client, err := findMI6Client()
	if err != nil {
		return err
	}

	cmd := exec.Command(mi6Client, "--key", s.KeyPath, s.Addr)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	s.stderrBuf.Reset()
	cmd.Stderr = io.MultiWriter(os.Stderr, &s.stderrBuf)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mi6-client: %w", err)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.scanner = bufio.NewScanner(stdout)
	s.scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	s.broadcastCh = make(chan *protocol.Response, 100)
	s.pendingResps = make(map[string]chan *protocol.Response)

	// Wait for mi6-client to either connect or fail early.
	s.waitCh = make(chan struct{})
	go func() {
		s.waitErr = cmd.Wait()
		close(s.waitCh)
	}()

	// Start background reader goroutine to route messages.
	go s.readLoop()

	select {
	case <-s.waitCh:
		// Process exited during startup — report the actual error.
		errMsg := strings.TrimSpace(s.stderrBuf.String())
		s.stdin = nil
		s.cmd = nil
		s.scanner = nil
		if errMsg != "" {
			return fmt.Errorf("mi6-client failed: %s", errMsg)
		}
		return fmt.Errorf("mi6-client exited unexpectedly: %v", s.waitErr)
	case <-time.After(2 * time.Second):
		// Still running after 2s — assume connected.
		return nil
	}
}

// readLoop runs in a background goroutine and routes incoming messages.
func (s *MI6Sender) readLoop() {
	for s.scanner.Scan() {
		var resp protocol.Response
		if err := json.Unmarshal(s.scanner.Bytes(), &resp); err != nil {
			// Not a valid response (could be a request from another client); skip.
			continue
		}

		s.mu.Lock()
		if resp.RequestID != "" {
			// This is a response to a specific request.
			if ch, ok := s.pendingResps[resp.RequestID]; ok {
				select {
				case ch <- &resp:
					delete(s.pendingResps, resp.RequestID)
				default:
					// Channel full or closed, skip.
				}
			} else {
				// No pending request for this ID — treat as broadcast.
				select {
				case s.broadcastCh <- &resp:
				default:
					// Broadcast channel full, drop message.
				}
			}
		} else {
			// No RequestID — this is a broadcast message.
			select {
			case s.broadcastCh <- &resp:
			default:
				// Broadcast channel full, drop message.
			}
		}
		s.mu.Unlock()
	}

	// Scanner stopped — connection died. Close broadcast channel.
	s.mu.Lock()
	if s.broadcastCh != nil {
		close(s.broadcastCh)
		s.broadcastCh = nil
	}
	// Close all pending response channels.
	for id, ch := range s.pendingResps {
		close(ch)
		delete(s.pendingResps, id)
	}
	s.mu.Unlock()
}

func (s *MI6Sender) Send(req *protocol.Request) (*protocol.Response, error) {
	s.mu.Lock()
	if s.stdin == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("MI6 not connected")
	}

	// Assign a unique request ID so we can match the response.
	id := fmt.Sprintf("hem-%d-%d", os.Getpid(), atomic.AddUint64(&reqCounter, 1))
	req.RequestID = id

	// Create a channel for this request's response.
	respCh := make(chan *protocol.Response, 1)
	s.pendingResps[id] = respCh

	data, err := json.Marshal(req)
	if err != nil {
		delete(s.pendingResps, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	data = append(data, '\n')

	if _, err := s.stdin.Write(data); err != nil {
		delete(s.pendingResps, id)
		// Connection lost — try to reconnect once.
		s.closeInternal()
		if reconnErr := s.connectInternal(); reconnErr != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("MI6 connection lost, reconnect failed: %w", reconnErr)
		}
		// Re-register the pending response channel after reconnect.
		s.pendingResps[id] = respCh
		// Re-serialize with same request ID.
		if _, err := s.stdin.Write(data); err != nil {
			delete(s.pendingResps, id)
			s.mu.Unlock()
			return nil, fmt.Errorf("writing to MI6 after reconnect: %w", err)
		}
	}
	s.mu.Unlock()

	// Wait for response or timeout.
	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("MI6 connection closed while waiting for response")
		}
		return resp, nil
	case <-s.waitCh:
		s.mu.Lock()
		errMsg := strings.TrimSpace(s.stderrBuf.String())
		s.mu.Unlock()
		if errMsg != "" {
			return nil, fmt.Errorf("mi6-client died: %s", errMsg)
		}
		return nil, fmt.Errorf("mi6-client died: %v", s.waitErr)
	case <-time.After(60 * time.Second):
		s.mu.Lock()
		delete(s.pendingResps, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for response")
	}
}

// Broadcasts returns a read-only channel for receiving broadcast messages.
func (s *MI6Sender) Broadcasts() <-chan *protocol.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broadcastCh
}

// Close shuts down the MI6 connection.
func (s *MI6Sender) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeInternal()
}

// closeInternal tears down the mi6-client process. Caller must hold s.mu.
func (s *MI6Sender) closeInternal() {
	if s.stdin != nil {
		s.stdin.Close()
		s.stdin = nil
	}
	if s.waitCh != nil {
		<-s.waitCh // wait for the goroutine that called cmd.Wait()
		s.waitCh = nil
	}
	// Note: broadcastCh and pendingResps are cleaned up by readLoop when scanner stops.
	s.cmd = nil
	s.scanner = nil
}

// Send sends a request over a Unix socket (backward-compatible convenience function).
func Send(sockPath string, req *protocol.Request) (*protocol.Response, error) {
	s := &SocketSender{SockPath: sockPath}
	return s.Send(req)
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
