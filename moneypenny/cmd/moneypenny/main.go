package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/handler"
	"james/moneypenny/pkg/store"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	mi6Addr := flag.String("mi6", "", "connect via MI6 (host/session_id)")
	fifoDir := flag.String("fifo", "", "use named pipes in FOLDER for I/O (creates moneypenny-in and moneypenny-out)")
	local := flag.Bool("local", false, "run in local FIFO mode using default path (~/.config/james/moneypenny/fifo)")
	dataDir := flag.String("data-dir", defaultDataDir(), "directory for moneypenny data (db, keys)")
	showPubKey := flag.Bool("show-public-key", false, "output the public key and exit")
	verbose := flag.Bool("v", false, "verbose logging to stderr")
	flag.Parse()

	// --local is shorthand for --fifo with the default path.
	if *local && *fifoDir == "" {
		defaultFifo := filepath.Join(defaultDataDir(), "fifo")
		fifoDir = &defaultFifo
	}

	vlog := log.New(io.Discard, "[moneypenny] ", log.LstdFlags)
	if *verbose {
		vlog = log.New(os.Stderr, "[moneypenny] ", log.LstdFlags)
	}

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	keyPath := filepath.Join(*dataDir, "moneypenny_ecdsa")

	if *showPubKey {
		pubKey, err := loadOrCreatePublicKey(keyPath)
		if err != nil {
			log.Fatalf("failed to load public key: %v", err)
		}
		fmt.Print(string(ssh.MarshalAuthorizedKey(pubKey)))
		os.Exit(0)
	}

	// Open store.
	dbPath := filepath.Join(*dataDir, "moneypenny.db")
	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	log.Printf("moneypenny v%s", Version)

	runner := agent.New(vlog)
	h := handler.New(st, runner, Version)
	h.SetLogger(vlog.Printf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the scheduler for timed prompts.
	h.StartScheduler(ctx)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		// Give a moment for clean shutdown, then force exit.
		// This unblocks any stuck blocking syscalls (FIFO open/read).
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()

	if *mi6Addr != "" {
		runMI6(ctx, h, vlog, *mi6Addr, keyPath)
	} else if *fifoDir != "" {
		runFIFO(ctx, h, vlog, *fifoDir)
	} else {
		runStdio(ctx, h, vlog, os.Stdin, os.Stdout)
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/james/moneypenny"
	}
	return filepath.Join(home, ".config", "james", "moneypenny")
}

// truncateLog truncates a byte slice for logging, showing the first and last portions.
func truncateLog(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	tail := 50
	head := maxLen - tail - 3 // 3 for "..."
	if head < 10 {
		head = 10
	}
	return string(b[:head]) + "..." + string(b[len(b)-tail:])
}

// runStdio reads JSON commands from r (one per line), processes them, and writes responses to w.
func runStdio(ctx context.Context, h *handler.Handler, vlog *log.Logger, r io.Reader, w io.Writer) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		vlog.Printf("recv: %s", truncateLog(line, 200))
		cmd, err := envelope.ParseCommand(line)
		if err != nil {
			resp := envelope.ErrorResponse("", envelope.ErrInvalidRequest, err.Error())
			b, _ := resp.Marshal()
			vlog.Printf("send: %s", truncateLog(b, 200))
			w.Write(b)
			continue
		}
		vlog.Printf("exec: method=%s request_id=%s", cmd.Method, cmd.RequestID)
		resp := h.Handle(ctx, cmd)
		b, _ := resp.Marshal()
		vlog.Printf("send: %s", truncateLog(b, 200))
		w.Write(b)
	}
}

// runFIFO creates named pipes in the given directory and uses them for I/O.
// moneypenny-in: reads commands from here (other processes write to it)
// moneypenny-out: writes responses here (other processes read from it)
func runFIFO(ctx context.Context, h *handler.Handler, vlog *log.Logger, dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("failed to create fifo directory: %v", err)
	}

	inPath := filepath.Join(dir, "moneypenny-in")
	outPath := filepath.Join(dir, "moneypenny-out")

	// Create FIFOs.
	for _, p := range []string{inPath, outPath} {
		if err := createFifo(p); err != nil {
			log.Fatalf("failed to create fifo %s: %v", p, err)
		}
	}

	// Clean up FIFOs on exit.
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	log.Printf("fifos created: %s (in), %s (out)", inPath, outPath)

	// Open FIFOs.
	inFile, err := openFifoInput(inPath)
	if err != nil {
		log.Fatalf("failed to open fifo for reading: %v", err)
	}
	defer inFile.Close()

	outFile, err := openFifoOutput(outPath)
	if err != nil {
		log.Fatalf("failed to open fifo for writing: %v", err)
	}
	defer outFile.Close()

	vlog.Printf("fifos ready, waiting for commands")
	runStdio(ctx, h, vlog, inFile, outFile)
}

// runMI6 connects to an MI6 server and processes commands received through it.
// If the connection drops, it retries with exponential backoff (5s, 10s, 10s, ...).
func runMI6(ctx context.Context, h *handler.Handler, vlog *log.Logger, addr string, keyPath string) {
	// Parse addr: host/session_id or host:port/session_id
	host, sessionID, err := parseMI6Addr(addr)
	if err != nil {
		log.Fatalf("invalid MI6 address: %v", err)
	}

	// Ensure we have an SSH key.
	signer, pubKey, err := loadOrCreateKey(keyPath)
	if err != nil {
		log.Fatalf("failed to load/create SSH key: %v", err)
	}

	_ = signer
	_ = pubKey
	_ = host
	_ = sessionID

	// MI6 integration: spawn mi6-client as subprocess, pipe our protocol through it.
	mi6Client, err := findMI6Client()
	if err != nil {
		log.Fatalf("mi6-client not found: %v", err)
	}

	retryDelay := 5 * time.Second
	maxDelay := 10 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		vlog.Printf("connecting to MI6 at %s", addr)
		err := runMI6Once(ctx, h, vlog, mi6Client, keyPath, addr)
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			vlog.Printf("MI6 connection lost: %v", err)
		} else {
			vlog.Printf("MI6 connection closed")
		}

		vlog.Printf("reconnecting in %v...", retryDelay)
		select {
		case <-time.After(retryDelay):
		case <-ctx.Done():
			return
		}

		if retryDelay < maxDelay {
			retryDelay = maxDelay
		}
	}
}

func runMI6Once(ctx context.Context, h *handler.Handler, vlog *log.Logger, mi6Client, keyPath, addr string) error {
	cmd := exec.CommandContext(ctx, mi6Client, "--key", keyPath, addr)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get mi6-client stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get mi6-client stdout: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start mi6-client: %w", err)
	}

	vlog.Printf("MI6 connected")

	// Process commands from MI6 (via mi6-client stdout) and write responses (via mi6-client stdin).
	runStdio(ctx, h, vlog, stdout, stdin)

	stdin.Close()
	return cmd.Wait()
}

func parseMI6Addr(addr string) (host, sessionID string, err error) {
	idx := strings.IndexByte(addr, '/')
	if idx < 0 {
		return "", "", fmt.Errorf("expected host/session_id format, got %q", addr)
	}
	host = addr[:idx]
	sessionID = addr[idx+1:]
	if host == "" || sessionID == "" {
		return "", "", fmt.Errorf("host and session_id must not be empty")
	}
	if !strings.Contains(host, ":") {
		host = host + ":7007"
	}
	return host, sessionID, nil
}

func findMI6Client() (string, error) {
	// Look in PATH first, then check relative to our binary.
	path, err := findInPath("mi6-client")
	if err == nil {
		return path, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("mi6-client not found in PATH")
	}
	candidate := filepath.Join(filepath.Dir(exe), "mi6-client")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("mi6-client not found in PATH or next to moneypenny binary")
}

func findInPath(name string) (string, error) {
	return exec.LookPath(name)
}

// loadOrCreateKey loads an existing ECDSA key, or generates one if it doesn't exist.
func loadOrCreateKey(keyPath string) (interface{}, ssh.PublicKey, error) {
	if _, err := os.Stat(keyPath); err == nil {
		return loadKey(keyPath)
	}
	return generateAndSaveKey(keyPath)
}

// loadOrCreatePublicKey loads or creates the key and returns just the public key.
func loadOrCreatePublicKey(keyPath string) (ssh.PublicKey, error) {
	_, pub, err := loadOrCreateKey(keyPath)
	return pub, err
}

func loadKey(keyPath string) (interface{}, ssh.PublicKey, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	rawKey, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, nil, err
	}
	signer, ok := rawKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("expected ECDSA key, got %T", rawKey)
	}
	pubKey, err := ssh.NewPublicKey(&signer.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return signer, pubKey, nil
}

func generateAndSaveKey(keyPath string) (interface{}, ssh.PublicKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	privBytes, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, nil, err
	}

	privPEM := pem.EncodeToMemory(privBytes)
	if err := os.WriteFile(keyPath, privPEM, 0600); err != nil {
		return nil, nil, err
	}

	pubKey, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	// Also write the public key file.
	pubAuth := ssh.MarshalAuthorizedKey(pubKey)
	if err := os.WriteFile(keyPath+".pub", pubAuth, 0644); err != nil {
		return nil, nil, err
	}

	log.Printf("generated new ECDSA key at %s", keyPath)
	return key, pubKey, nil
}
