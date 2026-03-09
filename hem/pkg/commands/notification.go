package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"james/hem/assets"
)

// PlayNotificationSound plays the embedded notification sound.
func PlayNotificationSound() {
	wavPath := notificationWavPath()
	dir := filepath.Dir(wavPath)
	os.MkdirAll(dir, 0700)
	// Always write to pick up embedded file changes across builds.
	if err := os.WriteFile(wavPath, assets.NotificationWav, 0644); err != nil {
		return
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("afplay", wavPath)
	case "linux":
		cmd = exec.Command("aplay", "-q", wavPath)
	default:
		return
	}
	cmd.Start()
}

func notificationWavPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "james", "hem", "james.wav")
}
