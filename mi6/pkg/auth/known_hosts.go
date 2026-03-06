package auth

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// CheckKnownHost verifies a server's public key against the known_hosts file.
// Returns nil if the key is known and matches, ErrUnknownHost if not seen before
// (and adds it — TOFU), or ErrHostKeyChanged if the fingerprint changed.
func CheckKnownHost(knownHostsPath, serverAddr string, serverKey ssh.PublicKey) error {
	fingerprint := ssh.FingerprintSHA256(serverKey)

	entries, err := loadKnownHosts(knownHostsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading known_hosts: %w", err)
	}

	if existing, ok := entries[serverAddr]; ok {
		if existing == fingerprint {
			return nil // known and matches
		}
		return fmt.Errorf(
			"server key changed for %s!\nExpected: %s\nGot:      %s\n"+
				"This could indicate a man-in-the-middle attack.\n"+
				"If the server key was legitimately rotated, remove the old entry from:\n  %s",
			serverAddr, existing, fingerprint, knownHostsPath,
		)
	}

	// TOFU: first time seeing this server, trust and record.
	if err := addKnownHost(knownHostsPath, serverAddr, fingerprint); err != nil {
		return fmt.Errorf("saving known host: %w", err)
	}

	return nil
}

func loadKnownHosts(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	entries := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			entries[parts[0]] = parts[1]
		}
	}
	return entries, scanner.Err()
}

func addKnownHost(path, serverAddr, fingerprint string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", serverAddr, fingerprint)
	return err
}
