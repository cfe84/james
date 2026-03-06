package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"james/hem/assets"
)

// playNotificationSound plays the embedded notification sound if sound-notification is enabled.
func (e *Executor) playNotificationSound() {
	enabled, _ := e.store.GetDefault("sound-notification")
	if enabled != "true" {
		return
	}

	// Write the wav to a temp file in the data dir (reuse if exists).
	wavPath := notificationWavPath()
	if _, err := os.Stat(wavPath); err != nil {
		dir := filepath.Dir(wavPath)
		os.MkdirAll(dir, 0700)
		if err := os.WriteFile(wavPath, assets.NotificationWav, 0644); err != nil {
			return
		}
	}

	// Play asynchronously.
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
	return filepath.Join(home, ".config", "james", "hem", "notification.wav")
}
