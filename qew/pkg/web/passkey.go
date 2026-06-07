package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// storedCredential wraps a webauthn.Credential with management metadata.
type storedCredential struct {
	Label      string              `json:"label"`
	CreatedAt  time.Time           `json:"createdAt"`
	Credential webauthn.Credential `json:"credential"`
}

// passkeyData is the on-disk JSON shape for the passkey store.
type passkeyData struct {
	UserID      []byte             `json:"userID"`
	Credentials []storedCredential `json:"credentials"`
}

// PasskeyStore persists WebAuthn credentials for the single fixed "qew" user to
// a JSON file (0600), guarded by a mutex. The user handle (UserID) is generated
// once and reused so all credentials belong to the same WebAuthn user account.
type PasskeyStore struct {
	path string
	mu   sync.Mutex
	data passkeyData
}

// NewPasskeyStore loads the store at path, creating it (with a fresh random user
// handle) on first use. The file is created lazily on the first save.
func NewPasskeyStore(path string) (*PasskeyStore, error) {
	s := &PasskeyStore{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PasskeyStore) load() error {
	info, err := os.Lstat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			uid := make([]byte, 32)
			if _, err := rand.Read(uid); err != nil {
				return fmt.Errorf("generating passkey user id: %w", err)
			}
			s.data = passkeyData{UserID: uid}
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("passkey file %s is a symlink; refusing to read", s.path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("passkey file %s is not a regular file", s.path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("passkey file %s has insecure permissions %o (want 0600)", s.path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return fmt.Errorf("parsing passkey file %s: %w", s.path, err)
	}
	if len(s.data.UserID) == 0 {
		uid := make([]byte, 32)
		if _, err := rand.Read(uid); err != nil {
			return fmt.Errorf("generating passkey user id: %w", err)
		}
		s.data.UserID = uid
	}
	return nil
}

// save persists the store atomically (write temp + rename). Caller must hold mu.
func (s *PasskeyStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// user returns a snapshot of the WebAuthn user (handle + all credentials).
func (s *PasskeyStore) user() *qewUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	creds := make([]webauthn.Credential, len(s.data.Credentials))
	for i, c := range s.data.Credentials {
		creds[i] = c.Credential
	}
	uid := make([]byte, len(s.data.UserID))
	copy(uid, s.data.UserID)
	return &qewUser{id: uid, creds: creds}
}

// add stores a newly registered credential with a label.
func (s *PasskeyStore) add(label string, cred *webauthn.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if label == "" {
		label = "passkey"
	}
	s.data.Credentials = append(s.data.Credentials, storedCredential{
		Label:      label,
		CreatedAt:  time.Now(),
		Credential: *cred,
	})
	return s.save()
}

// updateCounter persists the authenticator state (sign counter, clone warning)
// for the credential matching cred.ID after a successful login.
func (s *PasskeyStore) updateCounter(cred *webauthn.Credential) error {
	if cred == nil || len(cred.ID) == 0 {
		return nil
	}
	target := base64.RawURLEncoding.EncodeToString(cred.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Credentials {
		if base64.RawURLEncoding.EncodeToString(s.data.Credentials[i].Credential.ID) == target {
			s.data.Credentials[i].Credential.Authenticator = cred.Authenticator
			return s.save()
		}
	}
	return nil
}

// list returns metadata for all registered credentials.
func (s *PasskeyStore) list() []passkeyInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]passkeyInfo, 0, len(s.data.Credentials))
	for _, c := range s.data.Credentials {
		out = append(out, passkeyInfo{
			ID:        base64.RawURLEncoding.EncodeToString(c.Credential.ID),
			Label:     c.Label,
			CreatedAt: c.CreatedAt,
		})
	}
	return out
}

// remove deletes the credential with the given base64url id. Returns true if a
// credential was removed.
func (s *PasskeyStore) remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Credentials {
		if base64.RawURLEncoding.EncodeToString(s.data.Credentials[i].Credential.ID) == id {
			s.data.Credentials = append(s.data.Credentials[:i], s.data.Credentials[i+1:]...)
			return true, s.save()
		}
	}
	return false, nil
}

// count returns the number of registered credentials.
func (s *PasskeyStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Credentials)
}

// passkeyInfo is the management-UI view of a registered credential.
type passkeyInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
}

// qewUser implements webauthn.User for the single fixed "qew" account.
type qewUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u *qewUser) WebAuthnID() []byte                         { return u.id }
func (u *qewUser) WebAuthnName() string                       { return "qew" }
func (u *qewUser) WebAuthnDisplayName() string                { return "Qew" }
func (u *qewUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }
