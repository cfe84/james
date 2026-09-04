package main

import (
	"strings"
	"testing"
)

func TestNonInteractiveInstallValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"requires level", []string{"--non-interactive", "--local"}, "exactly one of --user or --system"},
		{"rejects both levels", []string{"--non-interactive", "--user", "--system", "--local"}, "exactly one of --user or --system"},
		{"requires transport", []string{"--non-interactive", "--user"}, "exactly one of --local or --mi6"},
		{"rejects both transports", []string{"--non-interactive", "--user", "--local", "--mi6", "relay/session"}, "exactly one of --local or --mi6"},
		{"requires MI6 pin", []string{"--non-interactive", "--user", "--mi6", "relay/session"}, "--mi6-server-fingerprint is required"},
		{"rejects invalid interval", []string{"--non-interactive", "--user", "--local", "--auto-update", "--update-interval", "later"}, "invalid --update-interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runNonInteractiveInstall(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runNonInteractiveInstall(%v) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}
