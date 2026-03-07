package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"james/qew/pkg/web"
)

var Version = "dev"

func main() {
	mi6Addr := flag.String("mi6", "", "MI6 address for Hem control channel (host/session_id)")
	sockPath := flag.String("socket", "", "Hem server Unix socket path (default: ~/.config/james/hem/hem.sock)")
	listenAddr := flag.String("listen", ":8077", "HTTP listen address")
	keyPath := flag.String("key", "", "SSH key path (default: ~/.config/james/qew/qew_ecdsa)")
	password := flag.String("password", "", "password for web UI authentication (required unless --development)")
	development := flag.Bool("development", false, "development mode: allow no password, no Secure cookie flag")
	showPubKey := flag.Bool("show-public-key", false, "output the public key and exit")
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()

	if *keyPath == "" {
		home, _ := os.UserHomeDir()
		*keyPath = filepath.Join(home, ".config", "james", "qew", "qew_ecdsa")
	}

	// Ensure key directory exists.
	os.MkdirAll(filepath.Dir(*keyPath), 0700)

	if *showPubKey {
		pubKey, err := loadOrCreatePublicKey(*keyPath)
		if err != nil {
			log.Fatalf("failed to load public key: %v", err)
		}
		fmt.Print(string(ssh.MarshalAuthorizedKey(pubKey)))
		return
	}

	if *mi6Addr == "" && *sockPath == "" {
		home, _ := os.UserHomeDir()
		defaultSock := filepath.Join(home, ".config", "james", "hem", "hem.sock")
		sockPath = &defaultSock
	}
	if *mi6Addr != "" && *sockPath != "" {
		log.Fatal("specify either --mi6 or --socket, not both")
	}

	vlog := log.New(io.Discard, "[qew] ", log.LstdFlags)
	if *verbose {
		vlog = log.New(os.Stderr, "[qew] ", log.LstdFlags)
	}

	// Require password on non-loopback addresses unless --development.
	if *password == "" && !*development {
		host, _, _ := net.SplitHostPort(*listenAddr)
		if host == "" || host == "0.0.0.0" || host == "::" {
			log.Fatal("--password is required when listening on a non-loopback address (use --development to override)")
		}
		ip := net.ParseIP(host)
		if ip != nil && !ip.IsLoopback() {
			log.Fatal("--password is required when listening on a non-loopback address (use --development to override)")
		}
	}

	log.Printf("Qew v%s", Version)

	var hem web.HemClient
	if *mi6Addr != "" {
		// Ensure key exists (generate if needed).
		if _, err := loadOrCreatePublicKey(*keyPath); err != nil {
			log.Fatalf("failed to load/create SSH key: %v", err)
		}
		log.Printf("connecting to Hem via MI6: %s", *mi6Addr)
		mi6Client := web.NewMI6Client(*mi6Addr, *keyPath, vlog)
		if err := mi6Client.Start(); err != nil {
			log.Fatalf("failed to connect to MI6: %v", err)
		}
		hem = mi6Client
	} else {
		log.Printf("connecting to Hem via socket: %s", *sockPath)
		hem = &web.SocketClient{SockPath: *sockPath}
	}

	srv := web.NewServer(hem, *listenAddr, *password, *development, vlog)
	if err := srv.Run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func loadOrCreatePublicKey(keyPath string) (ssh.PublicKey, error) {
	if _, err := os.Stat(keyPath); err == nil {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		rawKey, err := ssh.ParseRawPrivateKey(data)
		if err != nil {
			return nil, err
		}
		signer, ok := rawKey.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("expected ECDSA key, got %T", rawKey)
		}
		return ssh.NewPublicKey(&signer.PublicKey)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	privBytes, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, err
	}

	privPEM := pem.EncodeToMemory(privBytes)
	if err := os.WriteFile(keyPath, privPEM, 0600); err != nil {
		return nil, err
	}

	pubKey, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}

	pubAuth := ssh.MarshalAuthorizedKey(pubKey)
	if err := os.WriteFile(keyPath+".pub", pubAuth, 0644); err != nil {
		return nil, err
	}

	log.Printf("generated new ECDSA key at %s", keyPath)
	return pubKey, nil
}
