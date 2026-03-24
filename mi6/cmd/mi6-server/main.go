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
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"james/mi6/pkg/auth"
	"james/mi6/pkg/protocol"
	"james/mi6/pkg/session"
	"james/mi6/pkg/transport"
)

const (
	handshakeTimeout = 10 * time.Second
	maxConcurrentConnections = 1000
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	port := flag.String("port", "", "port to listen on (overrides PORT env, default 7007)")
	listenAddr := flag.String("listen", "", "TCP address to listen on (overrides --port)")
	authorizedKeysPath := flag.String("authorized-keys", "", "path to OpenSSH authorized_keys file (required)")
	adminKeysPath := flag.String("admin-keys", "", "path to admin_keys file (defaults to admin_keys in same dir as authorized-keys)")
	serverKeyPath := flag.String("server-key", "", "path to server SSH private key (auto-generated if missing)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	// Resolve listen address: --listen > --port > PORT env > default.
	if *listenAddr == "" {
		p := *port
		if p == "" {
			p = os.Getenv("PORT")
		}
		if p == "" {
			p = "7007"
		}
		*listenAddr = ":" + p
	}

	if *showVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	if *authorizedKeysPath == "" {
		log.Fatal("--authorized-keys is required")
	}

	log.Printf("mi6-server v%s", Version)

	// Load or generate server key for mutual authentication.
	if *serverKeyPath == "" {
		*serverKeyPath = filepath.Join(filepath.Dir(*authorizedKeysPath), "server_ecdsa")
	}
	serverSigner, serverPubKey, err := auth.LoadOrGenerateServerKey(*serverKeyPath)
	if err != nil {
		log.Fatalf("Failed to load/generate server key: %v", err)
	}
	log.Printf("Server fingerprint: %s", ssh.FingerprintSHA256(serverPubKey))

	// Load authorized keys.
	var (
		authKeysMu     sync.RWMutex
		authorizedKeys []ssh.PublicKey
	)

	keys, err := auth.LoadAuthorizedKeys(*authorizedKeysPath)
	if err != nil {
		log.Fatalf("Failed to load authorized keys: %v", err)
	}
	authorizedKeys = keys
	log.Printf("Loaded %d authorized key(s) from %s", len(authorizedKeys), *authorizedKeysPath)

	getAuthorizedKeys := func() []ssh.PublicKey {
		authKeysMu.RLock()
		defer authKeysMu.RUnlock()
		return authorizedKeys
	}

	reloadAuthorizedKeys := func() {
		newKeys, err := auth.LoadAuthorizedKeys(*authorizedKeysPath)
		if err != nil {
			log.Printf("Failed to reload authorized keys: %v", err)
			return
		}
		authKeysMu.Lock()
		authorizedKeys = newKeys
		authKeysMu.Unlock()
		log.Printf("Reloaded %d authorized key(s) from %s", len(newKeys), *authorizedKeysPath)
	}

	// Load admin keys (optional).
	if *adminKeysPath == "" {
		*adminKeysPath = filepath.Join(filepath.Dir(*authorizedKeysPath), "admin_keys")
	}
	var (
		adminKeysMu sync.RWMutex
		adminKeys   []ssh.PublicKey
	)
	if akeys, err := auth.LoadAuthorizedKeys(*adminKeysPath); err == nil {
		adminKeys = akeys
		log.Printf("Loaded %d admin key(s) from %s", len(adminKeys), *adminKeysPath)
	} else if !os.IsNotExist(err) {
		log.Printf("Warning: failed to load admin keys from %s: %v", *adminKeysPath, err)
	} else {
		log.Printf("No admin_keys file at %s (admin commands disabled)", *adminKeysPath)
	}

	getAdminKeys := func() []ssh.PublicKey {
		adminKeysMu.RLock()
		defer adminKeysMu.RUnlock()
		return adminKeys
	}

	// SIGHUP handler: reload authorized keys and admin keys.
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			reloadAuthorizedKeys()
			if newAdminKeys, err := auth.LoadAuthorizedKeys(*adminKeysPath); err == nil {
				adminKeysMu.Lock()
				adminKeys = newAdminKeys
				adminKeysMu.Unlock()
				log.Printf("Reloaded %d admin key(s) from %s", len(newAdminKeys), *adminKeysPath)
			} else if !os.IsNotExist(err) {
				log.Printf("Failed to reload admin keys from %s: %v", *adminKeysPath, err)
			}
		}
	}()

	// Session manager.
	manager := session.NewManager()

	// Listen on TCP.
	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
	}
	log.Printf("Listening on %s", ln.Addr())

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, cancel := context.WithCancel(context.Background())
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	go func() {
		<-shutdown
		log.Printf("Shutting down...")
		cancel()
		ln.Close()
	}()

	// Connection semaphore to limit concurrent connections.
	connSem := make(chan struct{}, maxConcurrentConnections)

	// Accept loop.
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				// Shutting down, stop accepting.
			default:
				log.Printf("Accept error: %v", err)
			}
			break
		}

		// Rate limit: block if too many concurrent connections.
		select {
		case connSem <- struct{}{}:
		default:
			log.Printf("Connection limit reached, rejecting %s", conn.RemoteAddr())
			conn.Close()
			continue
		}

		// Enable TCP keepalive to detect dead connections.
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
		}

		wg.Add(1)
		go func(conn net.Conn) {
			defer wg.Done()
			defer func() { <-connSem }()
			handleConnection(ctx, conn, serverSigner, serverPubKey, getAuthorizedKeys, getAdminKeys, *authorizedKeysPath, reloadAuthorizedKeys, manager)
		}(conn)
	}

	wg.Wait()
	log.Printf("Server stopped")
}

func handleConnection(
	ctx context.Context,
	conn net.Conn,
	serverSigner crypto.Signer,
	serverPubKey ssh.PublicKey,
	getAuthorizedKeys func() []ssh.PublicKey,
	getAdminKeys func() []ssh.PublicKey,
	authorizedKeysPath string,
	reloadAuthorizedKeys func(),
	manager *session.Manager,
) {
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("New connection from %s", remoteAddr)

	// Set handshake timeout to prevent slowloris attacks.
	conn.SetDeadline(time.Now().Add(handshakeTimeout))

	// Handshake with mutual authentication.
	secureConn, pubKey, err := transport.ServerHandshake(transport.ServerHandshakeParams{
		Conn:           conn,
		Signer:         serverSigner,
		PubKey:         serverPubKey,
		AuthorizedKeys: getAuthorizedKeys(),
	})
	if err != nil {
		log.Printf("Handshake failed for %s: %v", remoteAddr, err)
		conn.Close()
		return
	}
	defer secureConn.Close()

	// Clear the deadline after successful handshake.
	conn.SetDeadline(time.Time{})

	fingerprint := ssh.FingerprintSHA256(pubKey)
	log.Printf("Auth success for %s (key %s)", remoteAddr, fingerprint)

	// Wait for MsgJoinSession or MsgJoinSessionExclusive.
	joinMsg, err := secureConn.Receive()
	if err != nil {
		log.Printf("Failed to receive join message from %s: %v", remoteAddr, err)
		return
	}
	exclusive := joinMsg.Type == protocol.MsgJoinSessionExclusive
	if joinMsg.Type != protocol.MsgJoinSession && !exclusive {
		log.Printf("Expected MsgJoinSession from %s, got %d", remoteAddr, joinMsg.Type)
		return
	}

	sessionID := string(joinMsg.Payload)

	// Intercept admin session.
	if sessionID == "__admin__" {
		handleAdminSession(secureConn, pubKey, getAdminKeys(), authorizedKeysPath, reloadAuthorizedKeys, remoteAddr)
		return
	}

	if err := session.ValidateSessionID(sessionID); err != nil {
		log.Printf("Invalid session ID from %s: %v", remoteAddr, err)
		_ = secureConn.Send(&protocol.Message{Type: protocol.MsgAuthFail, Payload: []byte("invalid session ID")})
		return
	}

	var client *session.Client
	if exclusive {
		client, err = manager.JoinExclusive(sessionID)
		if err != nil {
			log.Printf("Exclusive join rejected for %s on session %q: %v", remoteAddr, sessionID, err)
			_ = secureConn.Send(&protocol.Message{Type: protocol.MsgAuthFail, Payload: []byte("session already has a client connected (exclusive required)")})
			return
		}
		log.Printf("Client %s (%s) joined session %q (exclusive)", client.ID, remoteAddr, sessionID)
	} else {
		client, err = manager.Join(sessionID)
		if err != nil {
			log.Printf("Join rejected for %s on session %q: %v", remoteAddr, sessionID, err)
			_ = secureConn.Send(&protocol.Message{Type: protocol.MsgAuthFail, Payload: []byte("session has an exclusive client connected")})
			return
		}
		log.Printf("Client %s (%s) joined session %q", client.ID, remoteAddr, sessionID)
	}

	// Send MsgJoinSessionOK.
	if err := secureConn.Send(&protocol.Message{Type: protocol.MsgJoinSessionOK}); err != nil {
		log.Printf("Failed to send join OK to %s: %v", remoteAddr, err)
		manager.Leave(sessionID, client.ID)
		return
	}

	// Cleanup on exit.
	defer func() {
		manager.Leave(sessionID, client.ID)
		log.Printf("Client %s (%s) left session %q", client.ID, remoteAddr, sessionID)
	}()

	// Write goroutine: forward data from client.WriteCh to the secure connection.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for data := range client.WriteCh {
			if err := secureConn.Send(&protocol.Message{
				Type:    protocol.MsgData,
				Payload: data,
			}); err != nil {
				log.Printf("Write error for client %s: %v", client.ID, err)
				return
			}
		}
	}()

	// Keepalive goroutine: send periodic pings to detect dead connections.
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := secureConn.Send(&protocol.Message{Type: protocol.MsgPing}); err != nil {
					log.Printf("Ping failed for client %s: %v", client.ID, err)
					conn.Close() // force read loop to exit
					return
				}
			case <-writeDone:
				return
			}
		}
	}()

	// Read loop: receive messages from the client.
	for {
		msg, err := secureConn.Receive()
		if err != nil {
			log.Printf("Read error for client %s: %v", client.ID, err)
			break
		}

		switch msg.Type {
		case protocol.MsgData:
			manager.Broadcast(sessionID, client.ID, msg.Payload)
		case protocol.MsgPing:
			if err := secureConn.Send(&protocol.Message{Type: protocol.MsgPong}); err != nil {
				log.Printf("Failed to send pong to client %s: %v", client.ID, err)
				return
			}
		case protocol.MsgPong:
			// Keepalive response from client — nothing to do.
		case protocol.MsgLeaveSession:
			log.Printf("Client %s requested leave", client.ID)
			return
		default:
			log.Printf("Unknown message type %d from client %s", msg.Type, client.ID)
		}
	}

	// Wait for write and ping goroutines to finish (Leave will close WriteCh via defer).
	<-writeDone
	<-pingDone
}
