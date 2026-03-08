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

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	stderrBuf bytes.Buffer
	waitCh    chan struct{} // closed when mi6-client process exits
	waitErr   error
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

	// Wait for mi6-client to either connect or fail early.
	s.waitCh = make(chan struct{})
	go func() {
		s.waitErr = cmd.Wait()
		close(s.waitCh)
	}()

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

func (s *MI6Sender) Send(req *protocol.Request) (*protocol.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stdin == nil {
		return nil, fmt.Errorf("MI6 not connected")
	}

	// Assign a unique request ID so we can match the response.
	id := fmt.Sprintf("hem-%d-%d", os.Getpid(), atomic.AddUint64(&reqCounter, 1))
	req.RequestID = id

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	data = append(data, '\n')

	if _, err := s.stdin.Write(data); err != nil {
		// Connection lost — try to reconnect once.
		s.closeInternal()
		if reconnErr := s.connectInternal(); reconnErr != nil {
			return nil, fmt.Errorf("MI6 connection lost, reconnect failed: %w", reconnErr)
		}
		// Re-serialize with same request ID.
		if _, err := s.stdin.Write(data); err != nil {
			return nil, fmt.Errorf("writing to MI6 after reconnect: %w", err)
		}
	}

	// Read lines until we find a response matching our request ID.
	for {
		if !s.scanner.Scan() {
			// Check if process died.
			select {
			case <-s.waitCh:
				errMsg := strings.TrimSpace(s.stderrBuf.String())
				if errMsg != "" {
					return nil, fmt.Errorf("mi6-client died: %s", errMsg)
				}
				return nil, fmt.Errorf("mi6-client died: %v", s.waitErr)
			default:
			}
			return nil, fmt.Errorf("no response from MI6")
		}

		var resp protocol.Response
		if err := json.Unmarshal(s.scanner.Bytes(), &resp); err != nil {
			// Not a valid response (could be a request from another client); skip.
			continue
		}

		// Skip messages without a matching request ID (responses to other clients).
		if resp.RequestID != id {
			continue
		}

		return &resp, nil
	}
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
