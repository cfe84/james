package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"james/hem/pkg/protocol"
)

// MI6Listener accepts commands over an MI6 session, dispatches them,
// and writes responses back through the same session.
type MI6Listener struct {
	addr       string // MI6 address: host/session_id
	keyPath    string
	dispatcher Dispatcher
	vlog       *log.Logger

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  interface{ Write([]byte) (int, error) }
	stopCh chan struct{}
}

// NewMI6Listener creates a listener that accepts Hem commands over MI6.
func NewMI6Listener(addr, keyPath string, dispatcher Dispatcher, vlog *log.Logger) *MI6Listener {
	return &MI6Listener{
		addr:       addr,
		keyPath:    keyPath,
		dispatcher: dispatcher,
		vlog:       vlog,
		stopCh:     make(chan struct{}),
	}
}

// Run starts the MI6 listener with automatic reconnection. Blocks until Stop() is called.
func (m *MI6Listener) Run() {
	retryDelay := 5 * time.Second
	maxDelay := 10 * time.Second

	for {
		select {
		case <-m.stopCh:
			return
		default:
		}

		m.vlog.Printf("MI6 control: connecting to %s", m.addr)
		err := m.runOnce()

		select {
		case <-m.stopCh:
			return
		default:
		}

		if err != nil {
			m.vlog.Printf("MI6 control: connection lost: %v", err)
		} else {
			m.vlog.Printf("MI6 control: connection closed")
		}

		m.vlog.Printf("MI6 control: reconnecting in %v...", retryDelay)
		select {
		case <-time.After(retryDelay):
		case <-m.stopCh:
			return
		}

		if retryDelay < maxDelay {
			retryDelay = maxDelay
		}
	}
}

// WriteBroadcast sends an unsolicited message to the MI6 control channel.
// Messages without a RequestID are treated as broadcasts by the client's readLoop.
func (m *MI6Listener) WriteBroadcast(resp *protocol.Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stdin == nil {
		return fmt.Errorf("MI6 control: not connected")
	}
	_, err = m.stdin.Write(b)
	return err
}

// Stop shuts down the MI6 listener.
func (m *MI6Listener) Stop() {
	close(m.stopCh)
	m.mu.Lock()
	if m.cmd != nil && m.cmd.Process != nil {
		m.cmd.Process.Kill()
	}
	m.mu.Unlock()
}

func (m *MI6Listener) runOnce() error {
	mi6Client, err := findMI6Client()
	if err != nil {
		return fmt.Errorf("mi6-client not found: %w", err)
	}

	cmd := exec.Command(mi6Client, "--key", m.keyPath, m.addr)
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

	m.mu.Lock()
	m.cmd = cmd
	m.stdin = stdin
	m.mu.Unlock()

	m.vlog.Printf("MI6 control: connected")

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		select {
		case <-m.stopCh:
			stdin.Close()
			cmd.Wait()
			return nil
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		m.vlog.Printf("MI6 control recv: %s", line)

		var req protocol.Request
		if err := json.Unmarshal(line, &req); err != nil {
			m.vlog.Printf("MI6 control: invalid request: %v", err)
			continue
		}

		resp := m.dispatcher.Dispatch(req.Verb, req.Noun, req.Args)
		resp.RequestID = req.RequestID
		resp.Verb = req.Verb
		resp.Noun = req.Noun

		b, err := json.Marshal(resp)
		if err != nil {
			m.vlog.Printf("MI6 control: marshal error: %v", err)
			continue
		}
		b = append(b, '\n')

		m.mu.Lock()
		_, writeErr := m.stdin.Write(b)
		m.mu.Unlock()

		if writeErr != nil {
			m.vlog.Printf("MI6 control: write error: %v", writeErr)
			break
		}

		m.vlog.Printf("MI6 control send: %s", b[:len(b)-1])
	}

	stdin.Close()
	return cmd.Wait()
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
