package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/crypto/ssh"

	"james/mi6/pkg/auth"
	"james/mi6/pkg/protocol"
	"james/mi6/pkg/session"
	"james/mi6/pkg/transport"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	port := flag.String("port", "", "port to listen on (overrides PORT env, default 7007)")
	listenAddr := flag.String("listen", "", "TCP address to listen on (overrides --port)")
	authorizedKeysPath := flag.String("authorized-keys", "", "path to OpenSSH authorized_keys file (required)")
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

	// SIGHUP handler: reload authorized keys.
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			newKeys, err := auth.LoadAuthorizedKeys(*authorizedKeysPath)
			if err != nil {
				log.Printf("Failed to reload authorized keys: %v", err)
				continue
			}
			authKeysMu.Lock()
			authorizedKeys = newKeys
			authKeysMu.Unlock()
			log.Printf("Reloaded %d authorized key(s) from %s", len(newKeys), *authorizedKeysPath)
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

		wg.Add(1)
		go func(conn net.Conn) {
			defer wg.Done()
			handleConnection(ctx, conn, getAuthorizedKeys, manager)
		}(conn)
	}

	wg.Wait()
	log.Printf("Server stopped")
}

func handleConnection(
	ctx context.Context,
	conn net.Conn,
	getAuthorizedKeys func() []ssh.PublicKey,
	manager *session.Manager,
) {
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("New connection from %s", remoteAddr)

	// Handshake.
	secureConn, pubKey, err := transport.ServerHandshake(conn, getAuthorizedKeys())
	if err != nil {
		log.Printf("Handshake failed for %s: %v", remoteAddr, err)
		conn.Close()
		return
	}
	defer secureConn.Close()

	fingerprint := ssh.FingerprintSHA256(pubKey)
	log.Printf("Auth success for %s (key %s)", remoteAddr, fingerprint)

	// Wait for MsgJoinSession.
	joinMsg, err := secureConn.Receive()
	if err != nil {
		log.Printf("Failed to receive join message from %s: %v", remoteAddr, err)
		return
	}
	if joinMsg.Type != protocol.MsgJoinSession {
		log.Printf("Expected MsgJoinSession from %s, got %d", remoteAddr, joinMsg.Type)
		return
	}

	sessionID := string(joinMsg.Payload)
	client := manager.Join(sessionID)
	log.Printf("Client %s (%s) joined session %q", client.ID, remoteAddr, sessionID)

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
		case protocol.MsgLeaveSession:
			log.Printf("Client %s requested leave", client.ID)
			return
		default:
			log.Printf("Unknown message type %d from client %s", msg.Type, client.ID)
		}
	}

	// Wait for write goroutine to finish (Leave will close WriteCh via defer).
	<-writeDone
}
