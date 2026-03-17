package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"james/mi6/pkg/auth"
	"james/mi6/pkg/protocol"
	"james/mi6/pkg/transport"
)

// adminMu serialises admin write operations (add/delete keys) to prevent
// concurrent modifications to the authorized_keys file.
var adminMu sync.Mutex

// AdminRequest is a JSON command sent by an admin client.
type AdminRequest struct {
	Command     string `json:"command"`               // "list_keys", "add_key", "delete_key"
	Key         string `json:"key,omitempty"`          // for add_key: full authorized_keys line
	Fingerprint string `json:"fingerprint,omitempty"`  // for delete_key: SHA256:... fingerprint
}

// AdminKeyInfo describes a single key in authorized_keys.
type AdminKeyInfo struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Comment     string `json:"comment"`
	Line        string `json:"line"` // full authorized_keys line (for display)
}

// AdminResponse is a JSON response sent back to the admin client.
type AdminResponse struct {
	Status  string         `json:"status"`            // "ok" or "error"
	Message string         `json:"message,omitempty"`
	Keys    []AdminKeyInfo `json:"keys,omitempty"`
}

// handleAdminSession handles a client that joined the __admin__ session.
// It verifies the client is an admin, then processes commands in a request-response loop.
func handleAdminSession(
	secureConn *transport.SecureConn,
	pubKey ssh.PublicKey,
	adminKeys []ssh.PublicKey,
	authorizedKeysPath string,
	reloadFn func(),
	remoteAddr string,
) {
	fingerprint := ssh.FingerprintSHA256(pubKey)

	// Check admin authorization.
	if !auth.IsAuthorized(pubKey, adminKeys) {
		log.Printf("Admin access denied for %s (key %s)", remoteAddr, fingerprint)
		_ = secureConn.Send(&protocol.Message{
			Type:    protocol.MsgAuthFail,
			Payload: []byte("not an admin key"),
		})
		return
	}

	log.Printf("Admin session started for %s (key %s)", remoteAddr, fingerprint)

	// Confirm join.
	if err := secureConn.Send(&protocol.Message{Type: protocol.MsgJoinSessionOK}); err != nil {
		log.Printf("Failed to send admin join OK to %s: %v", remoteAddr, err)
		return
	}

	// Command loop.
	for {
		msg, err := secureConn.Receive()
		if err != nil {
			log.Printf("Admin read error from %s: %v", remoteAddr, err)
			return
		}

		switch msg.Type {
		case protocol.MsgData:
			resp := processAdminCommand(msg.Payload, authorizedKeysPath, reloadFn)
			respData, _ := json.Marshal(resp)
			respData = append(respData, '\n')
			if err := secureConn.Send(&protocol.Message{
				Type:    protocol.MsgData,
				Payload: respData,
			}); err != nil {
				log.Printf("Admin write error to %s: %v", remoteAddr, err)
				return
			}

		case protocol.MsgPing:
			_ = secureConn.Send(&protocol.Message{Type: protocol.MsgPong})

		case protocol.MsgLeaveSession:
			log.Printf("Admin session ended for %s", remoteAddr)
			return

		default:
			log.Printf("Unknown message type %d from admin %s", msg.Type, remoteAddr)
		}
	}
}

func processAdminCommand(data []byte, authorizedKeysPath string, reloadFn func()) *AdminResponse {
	var req AdminRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("invalid request: %v", err)}
	}

	switch req.Command {
	case "list_keys":
		return adminListKeys(authorizedKeysPath)
	case "add_key":
		return adminAddKey(authorizedKeysPath, req.Key, reloadFn)
	case "delete_key":
		return adminDeleteKey(authorizedKeysPath, req.Fingerprint, reloadFn)
	default:
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("unknown command: %s", req.Command)}
	}
}

func adminListKeys(authorizedKeysPath string) *AdminResponse {
	lines, err := readKeyLines(authorizedKeysPath)
	if err != nil {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("reading keys: %v", err)}
	}

	var keys []AdminKeyInfo
	for _, line := range lines {
		key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue // skip unparseable lines
		}
		ki := AdminKeyInfo{
			Fingerprint: ssh.FingerprintSHA256(key),
			Type:        key.Type(),
			Comment:     comment,
			Line:        strings.TrimSpace(line),
		}
		keys = append(keys, ki)
	}

	return &AdminResponse{Status: "ok", Keys: keys}
}

func adminAddKey(authorizedKeysPath, keyLine string, reloadFn func()) *AdminResponse {
	if keyLine == "" {
		return &AdminResponse{Status: "error", Message: "key is required"}
	}

	// Validate the key parses.
	_, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyLine))
	if err != nil {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("invalid key: %v", err)}
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	// Read existing lines.
	lines, err := readKeyLines(authorizedKeysPath)
	if err != nil && !os.IsNotExist(err) {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("reading keys: %v", err)}
	}

	// Append.
	lines = append(lines, strings.TrimSpace(keyLine))

	if err := atomicWriteLines(authorizedKeysPath, lines); err != nil {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("writing keys: %v", err)}
	}

	reloadFn()
	log.Printf("Admin: added key to %s", authorizedKeysPath)
	return &AdminResponse{Status: "ok", Message: "Key added"}
}

func adminDeleteKey(authorizedKeysPath, fingerprint string, reloadFn func()) *AdminResponse {
	if fingerprint == "" {
		return &AdminResponse{Status: "error", Message: "fingerprint is required"}
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	lines, err := readKeyLines(authorizedKeysPath)
	if err != nil {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("reading keys: %v", err)}
	}

	var kept []string
	found := false
	for _, line := range lines {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			kept = append(kept, line) // preserve unparseable lines
			continue
		}
		if ssh.FingerprintSHA256(key) == fingerprint {
			found = true
			continue // skip this key
		}
		kept = append(kept, line)
	}

	if !found {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("key not found: %s", fingerprint)}
	}

	if err := atomicWriteLines(authorizedKeysPath, kept); err != nil {
		return &AdminResponse{Status: "error", Message: fmt.Sprintf("writing keys: %v", err)}
	}

	reloadFn()
	log.Printf("Admin: deleted key %s from %s", fingerprint, authorizedKeysPath)
	return &AdminResponse{Status: "ok", Message: "Key deleted"}
}

// readKeyLines reads the authorized_keys file and returns non-empty, non-comment lines.
func readKeyLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return lines, nil
}

// atomicWriteLines writes lines to a file atomically (temp + rename).
func atomicWriteLines(path string, lines []string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".authorized_keys.tmp.")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	content := strings.Join(lines, "\n") + "\n"
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}
