package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	waCookieName     = "qew_wa"
	waChallengeTTL   = 5 * time.Minute
	waCookieMaxAgeSc = 300
	// maxOutstandingChallenges bounds the in-memory challenge map so an
	// unauthenticated client spamming /webauthn/login/begin can't grow it
	// without limit (each entry self-expires after waChallengeTTL anyway).
	maxOutstandingChallenges = 512
)

// waChallenge is a pending WebAuthn ceremony (registration or login) keyed by a
// short-lived temporary cookie, holding the server-side SessionData until the
// browser posts the finish response.
type waChallenge struct {
	session *webauthn.SessionData
	kind    string // "register" or "login" — finish must match the begin
	expires time.Time
}

// webauthnFor builds a per-request WebAuthn relying party from the request Host
// header. The RP ID is the host without its port; the allowed origin is the
// scheme + host (scheme honours X-Forwarded-Proto from the TLS-terminating
// proxy, defaulting to https in non-development mode).
func (s *Server) webauthnFor(r *http.Request) (*webauthn.WebAuthn, error) {
	host := r.Host
	rpid := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		rpid = h
	}
	scheme := "https"
	if s.development {
		// Only in development do we trust X-Forwarded-Proto to select http vs
		// https (e.g. a local proxy). In production the origin is always https:
		// a forged X-Forwarded-Proto: http can then never weaken the check (it
		// would simply fail the origin match rather than downgrade it).
		scheme = "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
			scheme = "https"
		}
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rpid,
		RPDisplayName: "Qew",
		RPOrigins:     []string{scheme + "://" + host},
	})
}

// --- challenge session storage ---

// waCookie builds the temporary challenge cookie (optionally a clear cookie when
// maxAge < 0) with consistent security attributes.
func (s *Server) waCookie(value string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name:     waCookieName,
		Value:    value,
		Path:     "/webauthn/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
	if !s.development {
		cookie.Secure = true
	}
	return cookie
}

// putChallenge stores a pending ceremony and sets the temporary cookie that
// identifies it on the finish request. It also sweeps expired challenges.
func (s *Server) putChallenge(w http.ResponseWriter, session *webauthn.SessionData, kind string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(key)

	s.waMu.Lock()
	now := time.Now()
	for k, c := range s.waChallenges {
		if now.After(c.expires) {
			delete(s.waChallenges, k)
		}
	}
	if len(s.waChallenges) >= maxOutstandingChallenges {
		s.waMu.Unlock()
		return errTooManyChallenges
	}
	s.waChallenges[id] = &waChallenge{session: session, kind: kind, expires: now.Add(waChallengeTTL)}
	s.waMu.Unlock()

	http.SetCookie(w, s.waCookie(id, waCookieMaxAgeSc))
	return nil
}

// takeChallenge consumes the pending ceremony referenced by the request's
// temporary cookie (single use) and clears the cookie. The stored ceremony kind
// must match expectKind so a registration challenge can't be replayed at a login
// finish endpoint (or vice versa).
func (s *Server) takeChallenge(w http.ResponseWriter, r *http.Request, expectKind string) *webauthn.SessionData {
	cookie, err := r.Cookie(waCookieName)
	if err != nil {
		return nil
	}
	s.waMu.Lock()
	c, ok := s.waChallenges[cookie.Value]
	if ok {
		delete(s.waChallenges, cookie.Value)
	}
	s.waMu.Unlock()

	// Clear the temporary cookie regardless (same attributes as when set).
	http.SetCookie(w, s.waCookie("", -1))

	if !ok || time.Now().After(c.expires) || c.kind != expectKind {
		return nil
	}
	return c.session
}

var errTooManyChallenges = errorString("too many outstanding webauthn challenges")

type errorString string

func (e errorString) Error() string { return string(e) }

// --- registration (requires an authenticated password session) ---

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	wa, err := s.webauthnFor(r)
	if err != nil {
		http.Error(w, "webauthn config error", http.StatusInternalServerError)
		return
	}
	user := s.passkeys.user()
	options, session, err := wa.BeginRegistration(user)
	if err != nil {
		s.vlog.Printf("passkey register begin: %v", err)
		http.Error(w, "registration error", http.StatusInternalServerError)
		return
	}
	if err := s.putChallenge(w, session, "register"); err != nil {
		http.Error(w, "challenge error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, options)
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	session := s.takeChallenge(w, r, "register")
	if session == nil {
		http.Error(w, "no pending registration", http.StatusBadRequest)
		return
	}
	wa, err := s.webauthnFor(r)
	if err != nil {
		http.Error(w, "webauthn config error", http.StatusInternalServerError)
		return
	}
	label := r.URL.Query().Get("label")
	user := s.passkeys.user()
	cred, err := wa.FinishRegistration(user, *session, r)
	if err != nil {
		s.vlog.Printf("passkey register finish: %v", err)
		http.Error(w, "registration failed", http.StatusBadRequest)
		return
	}
	if err := s.passkeys.add(label, cred); err != nil {
		s.vlog.Printf("passkey store: %v", err)
		http.Error(w, "could not save passkey", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- login (public, rate-limited) ---

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.passkeys.count() == 0 {
		http.Error(w, "no passkeys registered", http.StatusBadRequest)
		return
	}
	ip := remoteIP(r)
	if wait := s.loginDelay(ip); wait > 0 {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]string{"error": "rate limited"})
		return
	}
	wa, err := s.webauthnFor(r)
	if err != nil {
		http.Error(w, "webauthn config error", http.StatusInternalServerError)
		return
	}
	user := s.passkeys.user()
	options, session, err := wa.BeginLogin(user)
	if err != nil {
		s.vlog.Printf("passkey login begin: %v", err)
		http.Error(w, "login error", http.StatusInternalServerError)
		return
	}
	if err := s.putChallenge(w, session, "login"); err != nil {
		http.Error(w, "challenge error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, options)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ip := remoteIP(r)
	if wait := s.loginDelay(ip); wait > 0 {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]string{"error": "rate limited"})
		return
	}
	session := s.takeChallenge(w, r, "login")
	if session == nil {
		http.Error(w, "no pending login", http.StatusBadRequest)
		return
	}
	wa, err := s.webauthnFor(r)
	if err != nil {
		http.Error(w, "webauthn config error", http.StatusInternalServerError)
		return
	}
	user := s.passkeys.user()
	cred, err := wa.FinishLogin(user, *session, r)
	if err != nil {
		s.recordLoginFailure(ip)
		s.vlog.Printf("passkey login finish: %v", err)
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	// Persist the updated sign counter / clone-warning state.
	if err := s.passkeys.updateCounter(cred); err != nil {
		s.vlog.Printf("passkey counter update: %v", err)
	}
	s.clearLoginAttempts(ip)
	now := time.Now()
	http.SetCookie(w, s.sessionCookie(s.makeToken(now, now)))
	writeJSON(w, map[string]bool{"ok": true})
}

// --- credential management (requires an authenticated session) ---

func (s *Server) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.passkeys.list())
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	removed, err := s.passkeys.remove(req.ID)
	if err != nil {
		http.Error(w, "could not delete", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"removed": removed})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
