package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"james/hem/pkg/protocol"
)

// Dispatcher handles dispatching commands to the appropriate handler.
type Dispatcher interface {
	Dispatch(verb, noun string, args []string) *protocol.Response
}

// Server listens on a Unix domain socket and dispatches commands.
type Server struct {
	sockPath   string
	dispatcher Dispatcher
	listener   net.Listener
	vlog       *log.Logger
}

// DefaultSocketPath returns the default path for the hem socket.
func DefaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/hem.sock"
	}
	return filepath.Join(home, ".config", "james", "hem", "hem.sock")
}

// New creates a new server.
func New(sockPath string, dispatcher Dispatcher, vlog *log.Logger) *Server {
	return &Server{
		sockPath:   sockPath,
		dispatcher: dispatcher,
		vlog:       vlog,
	}
}

// Run starts listening and serving requests. Blocks until interrupted.
func (s *Server) Run() error {
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0700); err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}

	// Remove stale socket file.
	os.Remove(s.sockPath)

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.sockPath, err)
	}
	s.listener = ln

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		s.vlog.Println("shutting down")
		ln.Close()
		os.Remove(s.sockPath)
		os.Exit(0)
	}()

	s.vlog.Printf("server listening on %s", s.sockPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	if !scanner.Scan() {
		return
	}

	var req protocol.Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		resp := protocol.ErrResponse(fmt.Sprintf("invalid request JSON: %v", err))
		writeResponse(conn, resp)
		return
	}

	s.vlog.Printf("request: %s %s %v", req.Verb, req.Noun, req.Args)

	resp := s.dispatcher.Dispatch(req.Verb, req.Noun, req.Args)

	if resp.Status == "error" {
		s.vlog.Printf("response: status=error message=%q", resp.Message)
	} else {
		s.vlog.Printf("response: status=%s", resp.Status)
	}

	writeResponse(conn, resp)
}

func writeResponse(conn net.Conn, resp *protocol.Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		b = []byte(`{"status":"error","message":"failed to marshal response"}`)
	}
	b = append(b, '\n')
	conn.Write(b)
}
