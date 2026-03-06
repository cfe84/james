package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	mi6Addr       string // for mi6 transport
	mi6KeyPath    string // SSH key for mi6
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
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	// Open both FIFOs. Since moneypenny has them open, these should not block
	// long. Open out (for reading) in a goroutine to avoid blocking if
	// moneypenny is waiting for input before producing output.
	type readResult struct {
		file *os.File
		err  error
	}
	outCh := make(chan readResult, 1)
	go func() {
		f, err := os.OpenFile(c.fifoOut, os.O_RDONLY, 0)
		outCh <- readResult{f, err}
	}()

	// Write command to moneypenny-in.
	inFile, err := os.OpenFile(c.fifoIn, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("opening fifo-in: %w", err)
	}
	if _, err := inFile.Write(data); err != nil {
		inFile.Close()
		return nil, fmt.Errorf("writing to fifo-in: %w", err)
	}
	inFile.Close()

	// Read response from moneypenny-out.
	res := <-outCh
	if res.err != nil {
		return nil, fmt.Errorf("opening fifo-out: %w", res.err)
	}
	defer res.file.Close()

	scanner := bufio.NewScanner(res.file)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from moneypenny")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &resp, nil
}

func (c *Client) sendMI6(ctx context.Context, cmd *Command) (*Response, error) {
	mi6Client, err := findMI6Client()
	if err != nil {
		return nil, err
	}

	proc := exec.CommandContext(ctx, mi6Client, "--key", c.mi6KeyPath, c.mi6Addr)
	proc.Stderr = os.Stderr

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
	stdin.Close()

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		proc.Wait()
		return nil, fmt.Errorf("no response from moneypenny via MI6")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		proc.Wait()
		return nil, fmt.Errorf("parsing response: %w", err)
	}

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
