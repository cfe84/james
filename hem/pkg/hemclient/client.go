package hemclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"

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

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
}

// Connect establishes the MI6 connection. Must be called before Send.
func (s *MI6Sender) Connect() error {
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
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mi6-client: %w", err)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.scanner = bufio.NewScanner(stdout)
	s.scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	return nil
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
		return nil, fmt.Errorf("writing to MI6: %w", err)
	}

	// Read lines until we find a response matching our request ID.
	for {
		if !s.scanner.Scan() {
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
	if s.stdin != nil {
		s.stdin.Close()
	}
	if s.cmd != nil {
		s.cmd.Wait()
	}
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
