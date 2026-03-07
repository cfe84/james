package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

//go:embed static
var staticFS embed.FS

const authCookieName = "qew_session"

// Server is the Qew web server.
type Server struct {
	hem      HemClient
	vlog     *log.Logger
	addr     string
	password string
	secret   []byte // HMAC signing key for session tokens
	pollMu   sync.Mutex
}

// NewServer creates a new Qew web server.
func NewServer(hem HemClient, listenAddr, password string, vlog *log.Logger) *Server {
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Server{
		hem:      hem,
		vlog:     vlog,
		addr:     listenAddr,
		password: password,
		secret:   secret,
	}
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	mux := http.NewServeMux()

	if s.password != "" {
		mux.HandleFunc("/login", s.handleLogin)
	}

	// API endpoint: POST /api — proxy to Hem via MI6.
	mux.HandleFunc("/api", s.requireAuth(s.handleAPI))

	// WebSocket endpoint for real-time polling.
	mux.Handle("/ws", s.requireAuthWS(websocket.Handler(s.handleWS)))

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
		http.Error(w, fmt.Sprintf("MI6 error: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleWS(ws *websocket.Conn) {
	defer ws.Close()
	s.vlog.Printf("WebSocket connected: %s", ws.Request().RemoteAddr)

	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(ws, &raw); err != nil {
			s.vlog.Printf("WebSocket read error: %v", err)
			return
		}

		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			websocket.JSON.Send(ws, map[string]string{
				"status":  "error",
				"message": fmt.Sprintf("bad request: %v", err),
			})
			continue
		}

		s.vlog.Printf("WS: %s %s %v", req.Verb, req.Noun, req.Args)

		resp, err := s.hem.Send(&req)
		if err != nil {
			websocket.JSON.Send(ws, map[string]string{
				"status":  "error",
				"message": fmt.Sprintf("MI6 error: %v", err),
			})
			continue
		}

		websocket.JSON.Send(ws, resp)
	}
}

// --- Auth ---

func (s *Server) makeToken() string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("qew-auth"))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) validToken(token string) bool {
	return hmac.Equal([]byte(token), []byte(s.makeToken()))
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

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.ParseForm()
		if r.FormValue("password") == s.password {
			http.SetCookie(w, &http.Cookie{
				Name:     authCookieName,
				Value:    s.makeToken(),
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int((7 * 24 * time.Hour).Seconds()),
			})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, loginPageHTML("Incorrect password"))
		return
	}
	fmt.Fprint(w, loginPageHTML(""))
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
