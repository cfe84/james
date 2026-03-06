package session

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
)

// Client represents a connected client in a session.
type Client struct {
	ID      string
	WriteCh chan []byte // buffered channel for outbound data
}

// NewClient creates a client with a unique ID and buffered write channel.
func NewClient(bufSize int) *Client {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return &Client{
		ID:      id,
		WriteCh: make(chan []byte, bufSize),
	}
}

// Session holds all clients connected to a particular session ID.
type Session struct {
	ID      string
	mu      sync.RWMutex
	clients map[string]*Client
}

// Manager manages all active sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// MaxSessionIDLength is the maximum allowed session ID length.
const MaxSessionIDLength = 256

// ErrInvalidSessionID is returned when a session ID fails validation.
var ErrInvalidSessionID = errors.New("session: invalid session ID")

// ValidateSessionID checks that a session ID is valid: non-empty, within length limit,
// and contains only allowed characters (alphanumeric + common specials).
func ValidateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSessionID)
	}
	if len(id) > MaxSessionIDLength {
		return fmt.Errorf("%w: exceeds %d characters", ErrInvalidSessionID, MaxSessionIDLength)
	}
	for _, r := range id {
		if !isAllowedSessionIDChar(r) {
			return fmt.Errorf("%w: invalid character %q", ErrInvalidSessionID, r)
		}
	}
	return nil
}

func isAllowedSessionIDChar(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return true
	}
	switch r {
	case '.', '-', '_', '~', '!', '@', '#', '$', '%', '^', '&', '*', '(', ')',
		'+', '=', '{', '}', '[', ']', ';', '\'', ':', '"', ',', '<', '>', '/', '?', '`':
		return true
	}
	return false
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// Join adds a client to a session (creating the session if needed). Returns the client.
func (m *Manager) Join(sessionID string) *Client {
	client := NewClient(256)

	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		sess = &Session{
			ID:      sessionID,
			clients: make(map[string]*Client),
		}
		m.sessions[sessionID] = sess
	}

	sess.mu.Lock()
	sess.clients[client.ID] = client
	sess.mu.Unlock()

	return client
}

// Leave removes a client from a session. Closes the client's WriteCh.
// If session becomes empty, it is deleted.
func (m *Manager) Leave(sessionID string, clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		return
	}

	sess.mu.Lock()
	client, exists := sess.clients[clientID]
	if exists {
		close(client.WriteCh)
		delete(sess.clients, clientID)
	}
	empty := len(sess.clients) == 0
	sess.mu.Unlock()

	if empty {
		delete(m.sessions, sessionID)
	}
}

// Broadcast sends data to all clients in the session EXCEPT the sender.
// Non-blocking: if a client's WriteCh is full, skip that client (slow consumer).
func (m *Manager) Broadcast(sessionID string, senderID string, data []byte) {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return
	}

	sess.mu.RLock()
	defer sess.mu.RUnlock()

	for id, client := range sess.clients {
		if id == senderID {
			continue
		}
		// Copy data for each recipient to prevent shared mutable state.
		dataCopy := make([]byte, len(data))
		copy(dataCopy, data)
		select {
		case client.WriteCh <- dataCopy:
		default:
			// slow consumer, skip
		}
	}
}

// ClientCount returns the number of clients in a session (0 if session doesn't exist).
func (m *Manager) ClientCount(sessionID string) int {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}

	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return len(sess.clients)
}
