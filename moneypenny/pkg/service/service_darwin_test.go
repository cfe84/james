//go:build darwin

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdArgumentsLoadLoginPathAndPreserveArguments(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "custom bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zprofile"), []byte(`export PATH="$ZDOTDIR/custom bin:/usr/bin:/bin"`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "daemon ' & $name")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$PATH\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		BinaryPath: binary,
		Local:      true,
		DataDir:    `data ' " & $(exit 19); $HOME`,
	}
	t.Setenv("ZDOTDIR", dir)
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, shell := range []string{"/bin/zsh", ""} {
		t.Run("shell="+shell, func(t *testing.T) {
			t.Setenv("SHELL", shell)
			args := launchdArguments(cfg)
			output, err := exec.Command(args[0], args[1:]...).CombinedOutput()
			if err != nil {
				t.Fatalf("launch daemon: %v: %s", err, output)
			}
			want := strings.Join(append([]string{binDir + ":/usr/bin:/bin"}, cfg.BuildArgs()...), "\n") + "\n"
			if string(output) != want {
				t.Fatalf("daemon environment/arguments = %q, want %q", output, want)
			}
		})
	}
}
