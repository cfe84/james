package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

//go:embed static
var staticFS embed.FS

const (
	authCookieName = "qew_session"
	tokenMaxAge    = 7 * 24 * time.Hour
)

// Server is the Qew web server.
type Server struct {
	hem         HemClient
	vlog        *log.Logger
	addr        string
	password    string
	development bool
	secret      []byte // HMAC signing key for session tokens
	pollMu      sync.Mutex

	// Login rate limiting: per-IP tracking.
	loginMu       sync.Mutex
	loginAttempts map[string]*loginTracker
}

type loginTracker struct {
	failures int
	lastFail time.Time
}

// NewServer creates a new Qew web server.
func NewServer(hem HemClient, listenAddr, password string, development bool, vlog *log.Logger) *Server {
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Server{
		hem:           hem,
		vlog:          vlog,
		addr:          listenAddr,
		password:      password,
		development:   development,
		secret:        secret,
		loginAttempts: make(map[string]*loginTracker),
	}
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	mux := http.NewServeMux()

	if s.password != "" {
		mux.HandleFunc("/login", s.handleLogin)
	}

	// API endpoint: POST /api — proxy to Hem.
	mux.HandleFunc("/api", s.requireAuth(s.csrfProtect(s.handleAPI)))

	// WebSocket endpoint for real-time polling.
	mux.Handle("/ws", s.requireAuthWS(http.HandlerFunc(s.handleWSUpgrade)))

	// Static files.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("static fs: %w", err)
	}
	mux.Handle("/", s.requireAuth(http.FileServer(http.FS(staticSub)).ServeHTTP))

	s.vlog.Printf("Qew web server listening on %s", s.addr)
	log.Printf("Qew web UI: http://%s", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	s.vlog.Printf("API: %s %s %v", req.Verb, req.Noun, req.Args)

	resp, err := s.hem.Send(&req)
	if err != nil {
		http.Error(w, fmt.Sprintf("backend error: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleWSUpgrade checks the Origin header before upgrading to WebSocket.
func (s *Server) handleWSUpgrade(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := s.isAllowedOrigin(origin, r.Host)
		if !allowed {
			s.vlog.Printf("WebSocket rejected: origin %q vs host %q", origin, r.Host)
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
	}

	wsHandler := websocket.Handler(s.handleWS)
	wsHandler.ServeHTTP(w, r)
}

func (s *Server) isAllowedOrigin(origin, host string) bool {
	// Strip scheme from origin to compare with Host header.
	o := origin
	for _, prefix := range []string{"https://", "http://"} {
		o = strings.TrimPrefix(o, prefix)
	}
	// Compare origin host with request host.
	oHost, _, _ := net.SplitHostPort(o)
	if oHost == "" {
		oHost = o
	}
	rHost, _, _ := net.SplitHostPort(host)
	if rHost == "" {
		rHost = host
	}
	return oHost == rHost
}

func (s *Server) handleWS(ws *websocket.Conn) {
	defer ws.Close()
	s.vlog.Printf("WebSocket connected: %s", ws.Request().RemoteAddr)

	// Subscribe to broadcasts if using MI6.
	var broadcastCh <-chan *Response
	var unsubscribe func()
	if bc, ok := s.hem.(BroadcastHemClient); ok {
		broadcastCh, unsubscribe = bc.Subscribe()
		defer unsubscribe()
		s.vlog.Printf("WebSocket subscribed to broadcasts")
	}

	// Channel for messages to send to client.
	sendCh := make(chan interface{}, 10)
	defer close(sendCh)

	// Goroutine to send messages to WebSocket.
	go func() {
		for msg := range sendCh {
			if err := websocket.JSON.Send(ws, msg); err != nil {
				s.vlog.Printf("WebSocket send error: %v", err)
				return
			}
		}
	}()

	// Goroutine to listen for broadcasts.
	if broadcastCh != nil {
		go func() {
			for resp := range broadcastCh {
				sendCh <- resp
			}
		}()
	}

	// Main loop: read requests from client.
	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(ws, &raw); err != nil {
			s.vlog.Printf("WebSocket read error: %v", err)
			return
		}

		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			sendCh <- map[string]string{
				"status":  "error",
				"message": fmt.Sprintf("bad request: %v", err),
			}
			continue
		}

		s.vlog.Printf("WS: %s %s %v", req.Verb, req.Noun, req.Args)

		resp, err := s.hem.Send(&req)
		if err != nil {
			sendCh <- map[string]string{
				"status":  "error",
				"message": fmt.Sprintf("backend error: %v", err),
			}
			continue
		}

		sendCh <- resp
	}
}

// --- Auth ---

// makeToken creates a signed token embedding the issued-at timestamp.
func (s *Server) makeToken() string {
	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(time.Now().Unix()))
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(ts)
	sig := mac.Sum(nil)
	return hex.EncodeToString(ts) + "." + hex.EncodeToString(sig)
}

// validToken checks the token signature and expiry.
func (s *Server) validToken(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	tsBytes, err := hex.DecodeString(parts[0])
	if err != nil || len(tsBytes) != 8 {
		return false
	}
	sigBytes, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}

	// Verify signature.
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(tsBytes)
	expected := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expected) {
		return false
	}

	// Check expiry.
	issuedAt := time.Unix(int64(binary.BigEndian.Uint64(tsBytes)), 0)
	return time.Since(issuedAt) < tokenMaxAge
}

func (s *Server) isAuthenticated(r *http.Request) bool {
	if s.password == "" {
		return true
	}
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false
	}
	return s.validToken(cookie.Value)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.isAuthenticated(r) {
			next(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

func (s *Server) requireAuthWS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// --- CSRF ---

// csrfProtect requires a custom header on non-GET requests to prevent CSRF.
// Browsers block cross-origin requests from setting custom headers without CORS preflight.
func (s *Server) csrfProtect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Requested-With") != "QewClient" {
				http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// --- Login with rate limiting ---

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		ip := remoteIP(r)

		// Check rate limit.
		if wait := s.loginDelay(ip); wait > 0 {
			s.vlog.Printf("login rate-limited: %s (wait %v)", ip, wait)
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, loginPageHTML(fmt.Sprintf("Too many attempts. Try again in %d seconds.", int(wait.Seconds())+1)))
			return
		}

		r.ParseForm()
		pw := r.FormValue("password")
		if subtle.ConstantTimeCompare([]byte(pw), []byte(s.password)) == 1 {
			s.clearLoginAttempts(ip)
			cookie := &http.Cookie{
				Name:     authCookieName,
				Value:    s.makeToken(),
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				MaxAge:   int(tokenMaxAge.Seconds()),
			}
			if !s.development {
				cookie.Secure = true
			}
			http.SetCookie(w, cookie)
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		s.recordLoginFailure(ip)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, loginPageHTML("Incorrect password"))
		return
	}
	fmt.Fprint(w, loginPageHTML(""))
}

// loginDelay returns how long the IP must wait before next attempt.
// Exponential backoff: 0, 1s, 2s, 4s, 8s, 16s, 30s max.
func (s *Server) loginDelay(ip string) time.Duration {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	t, ok := s.loginAttempts[ip]
	if !ok || t.failures == 0 {
		return 0
	}
	delay := time.Duration(1<<(t.failures-1)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	elapsed := time.Since(t.lastFail)
	if elapsed >= delay {
		return 0
	}
	return delay - elapsed
}

func (s *Server) recordLoginFailure(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	t, ok := s.loginAttempts[ip]
	if !ok {
		t = &loginTracker{}
		s.loginAttempts[ip] = t
	}
	t.failures++
	t.lastFail = time.Now()
}

func (s *Server) clearLoginAttempts(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginAttempts, ip)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loginPageHTML(errorMsg string) string {
	errBlock := ""
	if errorMsg != "" {
		errBlock = `<p style="color:var(--danger);margin-bottom:12px">` + errorMsg + `</p>`
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Qew — Login</title>
<style>
:root {
  --bg: #1a1a2e;
  --surface: #16213e;
  --surface2: #0f3460;
  --primary: #e94560;
  --text: #eee;
  --danger: #ef4444;
}
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
  background: var(--bg); color: var(--text);
  display: flex; align-items: center; justify-content: center; min-height: 100vh;
}
.login-box {
  background: var(--surface); padding: 32px; border-radius: 12px;
  width: 100%; max-width: 360px;
}
.login-box h1 { font-size: 1.4em; margin-bottom: 24px; }
.login-box h1 span { color: var(--primary); }
.login-box input {
  width: 100%; background: var(--bg); color: var(--text);
  border: 1px solid var(--surface2); border-radius: 6px;
  padding: 10px; font-size: 1em; margin-bottom: 16px;
}
.login-box input:focus { outline: none; border-color: var(--primary); }
.login-box button {
  width: 100%; background: var(--primary); color: white;
  border: none; border-radius: 6px; padding: 10px;
  font-size: 1em; font-weight: 600; cursor: pointer;
}
</style>
</head>
<body>
<div class="login-box">
  <h1><span>Qew</span> — James</h1>
  ` + errBlock + `
  <form method="POST" action="/login">
    <input type="password" name="password" placeholder="Password" autofocus>
    <button type="submit">Sign in</button>
  </form>
</div>
</body>
</html>`
}
