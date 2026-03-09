package main

import (
	"context"
	"crypto"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"james/mi6/internal/batch"
	"james/mi6/pkg/auth"
	"james/mi6/pkg/protocol"
	"james/mi6/pkg/transport"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	server := flag.String("server", "", "server address (host:port)")
	sessionID := flag.String("session-id", "", "session ID to join")
	keyPath := flag.String("key", "", "path to SSH private key file")
	keyValue := flag.String("key-value", "", "ECDSA private key PEM content (or set MI6_KEY env var)")
	generateKey := flag.Bool("generate-key", false, "generate a new ECDSA key pair and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	batchTimeout := flag.Duration("batch-timeout", 100*time.Millisecond, "idle timeout for stdin batching")
	batchSize := flag.Int("batch-size", 4096, "max batch size in bytes")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	if *generateKey {
		privPEM, pubAuth, err := auth.GenerateKey()
		if err != nil {
			log.Fatalf("failed to generate key: %v", err)
		}
		fmt.Fprintf(os.Stdout, "%s", privPEM)
		fmt.Fprintf(os.Stderr, "Public key:\n%s", pubAuth)
		os.Exit(0)
	}

	// Support positional arg: mi6-client host/session_id
	if flag.NArg() == 1 && *server == "" && *sessionID == "" {
		arg := flag.Arg(0)
		// Split on first "/" to get host and session_id.
		// Format: host:port/session_id or host/session_id (default port 7007)
		if idx := strings.IndexByte(arg, '/'); idx >= 0 {
			*server = arg[:idx]
			*sessionID = arg[idx+1:]
		} else {
			*server = arg
		}
	}

	// Default port if not specified.
	if *server != "" && !strings.Contains(*server, ":") {
		*server = *server + ":7007"
	}

	// Resolve key: --key-value flag > MI6_KEY env var > --key file
	if *keyValue == "" {
		if envKey := os.Getenv("MI6_KEY"); envKey != "" {
			*keyValue = envKey
		}
	}

	hasKey := *keyPath != "" || *keyValue != ""
	if *server == "" || *sessionID == "" || !hasKey {
		fmt.Fprintf(os.Stderr, "Usage: mi6-client [--server HOST:PORT --session-id ID | HOST/SESSION_ID] [--key PATH | --key-value PEM]\n")
		fmt.Fprintf(os.Stderr, "       mi6-client --generate-key\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Load private key.
	var (
		signer crypto.Signer
		pubKey ssh.PublicKey
		err    error
	)
	if *keyValue != "" {
		signer, pubKey, err = auth.ParsePrivateKeyBytes([]byte(*keyValue))
	} else {
		signer, pubKey, err = auth.LoadPrivateKey(*keyPath)
	}
	if err != nil {
		log.Fatalf("failed to load private key: %v", err)
	}

	// Determine known_hosts path for TOFU verification.
	knownHostsPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		knownHostsPath = home + "/.config/james/mi6/known_hosts"
	}

	// Connect to server via TCP.
	conn, err := net.Dial("tcp", *server)
	if err != nil {
		log.Fatalf("failed to connect to server: %v", err)
	}
	// Enable TCP keepalive to detect dead connections.
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}

	// Perform mutual-auth handshake with TOFU server verification.
	secureConn, err := transport.ClientHandshake(transport.ClientHandshakeParams{
		Conn:           conn,
		Signer:         signer,
		PubKey:         pubKey,
		ServerAddr:     *server,
		KnownHostsPath: knownHostsPath,
	})
	if err != nil {
		log.Fatalf("handshake failed: %v", err)
	}
	defer secureConn.Close()

	// Send MsgJoinSession.
	if err := secureConn.Send(&protocol.Message{
		Type:    protocol.MsgJoinSession,
		Payload: []byte(*sessionID),
	}); err != nil {
		log.Fatalf("failed to send join session: %v", err)
	}

	// Wait for MsgJoinSessionOK.
	joinResp, err := secureConn.Receive()
	if err != nil {
		log.Fatalf("failed to receive join session response: %v", err)
	}
	if joinResp.Type != protocol.MsgJoinSessionOK {
		log.Fatalf("expected MsgJoinSessionOK, got message type %d", joinResp.Type)
	}

	// Set up context for coordinated shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Channel to signal when goroutines finish.
	done := make(chan struct{}, 2)

	// Receive loop: read from server, write to stdout.
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msg, err := secureConn.Receive()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("receive error: %v", err)
				cancel()
				return
			}
			switch msg.Type {
			case protocol.MsgData:
				if _, err := os.Stdout.Write(msg.Payload); err != nil {
					log.Printf("stdout write error: %v", err)
					cancel()
					return
				}
			case protocol.MsgPing:
				if err := secureConn.Send(&protocol.Message{Type: protocol.MsgPong}); err != nil {
					log.Printf("failed to send pong: %v", err)
					cancel()
					return
				}
			}
		}
	}()

	// Stdin sender: batch stdin and send as MsgData.
	go func() {
		defer func() { done <- struct{}{} }()
		batcher := batch.New(*batchSize, *batchTimeout)
		flush := func(data []byte) error {
			return secureConn.Send(&protocol.Message{
				Type:    protocol.MsgData,
				Payload: data,
			})
		}
		if err := batcher.Run(ctx, os.Stdin, flush); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("stdin batcher error: %v", err)
		}
		// stdin EOF: leave session and shut down.
		cancel()
	}()

	// Wait for shutdown.
	<-ctx.Done()

	// Send MsgLeaveSession (best effort).
	_ = secureConn.Send(&protocol.Message{Type: protocol.MsgLeaveSession})
}
