// Package updater implements auto-update for moneypenny via GitHub releases.
// It periodically checks for new releases, downloads the appropriate binary,
// and performs a binary swap + re-exec when all sessions are idle.
package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Status constants for the update lifecycle.
const (
	StatusUpToDate    = "up_to_date"
	StatusChecking    = "checking"
	StatusDownloading = "downloading"
	StatusStaged      = "staged"
	StatusWaitingIdle = "waiting_idle"
	StatusRestarting  = "restarting"
	StatusError       = "error"
)

// SessionChecker is an interface for checking if all sessions are idle.
type SessionChecker interface {
	AllSessionsIdle() bool
}

// Info holds the current state of the updater for status queries.
type Info struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Status          string `json:"status"`
	LastChecked     string `json:"last_checked,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Updater checks for new GitHub releases and performs binary self-updates.
type Updater struct {
	mu sync.RWMutex

	currentVersion string
	repo           string // "owner/repo" e.g. "cfe84/james"
	dataDir        string // e.g. ~/.config/james/moneypenny
	checkInterval  time.Duration
	idleCheckFreq  time.Duration

	// State
	status        string
	latestVersion string
	lastChecked   time.Time
	lastError     string
	stagedDir     string // path to staged binaries

	checker SessionChecker
	vlog    *log.Logger

	// For re-exec
	execArgs []string // os.Args
}

// Option configures the Updater.
type Option func(*Updater)

// WithCheckInterval sets how often to check for updates.
func WithCheckInterval(d time.Duration) Option {
	return func(u *Updater) { u.checkInterval = d }
}

// WithLogger sets a verbose logger.
func WithLogger(l *log.Logger) Option {
	return func(u *Updater) { u.vlog = l }
}

// New creates an Updater.
func New(currentVersion, repo, dataDir string, checker SessionChecker, opts ...Option) *Updater {
	u := &Updater{
		currentVersion: currentVersion,
		repo:           repo,
		dataDir:        dataDir,
		checkInterval:  1 * time.Hour,
		idleCheckFreq:  30 * time.Second,
		status:         StatusUpToDate,
		checker:        checker,
		vlog:           log.New(io.Discard, "[updater] ", log.LstdFlags),
		execArgs:       os.Args,
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// Status returns the current update info.
func (u *Updater) Status() Info {
	u.mu.RLock()
	defer u.mu.RUnlock()
	lc := ""
	if !u.lastChecked.IsZero() {
		lc = u.lastChecked.UTC().Format(time.RFC3339)
	}
	return Info{
		CurrentVersion:  u.currentVersion,
		LatestVersion:   u.latestVersion,
		UpdateAvailable: u.latestVersion != "" && u.latestVersion != u.currentVersion,
		Status:          u.status,
		LastChecked:     lc,
		Error:           u.lastError,
	}
}

func (u *Updater) setStatus(s string) {
	u.mu.Lock()
	u.status = s
	u.mu.Unlock()
}

func (u *Updater) setError(err error) {
	u.mu.Lock()
	u.status = StatusError
	u.lastError = err.Error()
	u.mu.Unlock()
}

// Run starts the update check loop. Blocks until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	// Do an initial check shortly after startup.
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}

	u.cycle(ctx)

	ticker := time.NewTicker(u.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			u.cycle(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// cycle runs one check → download → wait-for-idle → swap cycle.
func (u *Updater) cycle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// 1. Check for new release.
	u.setStatus(StatusChecking)
	release, err := u.checkLatest(ctx)
	u.mu.Lock()
	u.lastChecked = time.Now()
	u.mu.Unlock()

	if err != nil {
		u.vlog.Printf("update check failed: %v", err)
		u.setError(err)
		return
	}

	tag := strings.TrimPrefix(release.TagName, "v")
	u.mu.Lock()
	u.latestVersion = tag
	u.mu.Unlock()

	if !isNewer(tag, u.currentVersion) {
		u.vlog.Printf("up to date (current=%s, latest=%s)", u.currentVersion, tag)
		u.setStatus(StatusUpToDate)
		return
	}

	u.vlog.Printf("new version available: %s (current: %s)", tag, u.currentVersion)

	// 2. Download and stage.
	u.setStatus(StatusDownloading)
	stagedDir, err := u.downloadAndStage(ctx, release)
	if err != nil {
		u.vlog.Printf("download failed: %v", err)
		u.setError(err)
		return
	}

	u.mu.Lock()
	u.stagedDir = stagedDir
	u.mu.Unlock()

	// 3. Wait for idle.
	u.setStatus(StatusWaitingIdle)
	u.vlog.Printf("update staged at %s, waiting for all sessions to be idle", stagedDir)

	if !u.waitForIdle(ctx) {
		u.vlog.Printf("context cancelled while waiting for idle")
		return
	}

	// 4. Swap and restart.
	u.setStatus(StatusRestarting)
	u.vlog.Printf("all sessions idle, performing binary swap")

	if err := u.swapAndRestart(stagedDir); err != nil {
		u.vlog.Printf("swap failed: %v", err)
		u.setError(err)
	}
	// If swapAndRestart succeeds, we don't return — the process is replaced.
}

// gitHubRelease is the subset of the GitHub release API we need.
type gitHubRelease struct {
	TagName string          `json:"tag_name"`
	Assets  []releaseAsset  `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (u *Updater) checkLatest(ctx context.Context) (*gitHubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var rel gitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &rel, nil
}

// downloadAndStage downloads the platform-specific archive and extracts moneypenny + mi6-client.
func (u *Updater) downloadAndStage(ctx context.Context, rel *gitHubRelease) (string, error) {
	// Find the right asset: james-GOOS-GOARCH.tar.gz
	archiveName := fmt.Sprintf("james-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var downloadURL string
	for _, a := range rel.Assets {
		if a.Name == archiveName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return "", fmt.Errorf("no asset found for %s", archiveName)
	}

	u.vlog.Printf("downloading %s", archiveName)

	// Download to temp file.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Stage directory.
	tag := strings.TrimPrefix(rel.TagName, "v")
	stageDir := filepath.Join(u.dataDir, "updates", tag)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return "", fmt.Errorf("create stage dir: %w", err)
	}

	// Extract from tar.gz — we want moneypenny and mi6-client binaries.
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		os.RemoveAll(stageDir)
		return "", fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	wantBinaries := map[string]bool{"moneypenny": false, "mi6-client": false}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(stageDir)
			return "", fmt.Errorf("read tar: %w", err)
		}

		// Entries are like james-darwin-arm64/moneypenny
		base := filepath.Base(hdr.Name)
		if _, want := wantBinaries[base]; !want {
			continue
		}

		outPath := filepath.Join(stageDir, base)
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			os.RemoveAll(stageDir)
			return "", fmt.Errorf("create %s: %w", base, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			os.RemoveAll(stageDir)
			return "", fmt.Errorf("extract %s: %w", base, err)
		}
		f.Close()
		wantBinaries[base] = true
		u.vlog.Printf("extracted %s to %s", base, outPath)
	}

	// Verify at least moneypenny was extracted.
	if !wantBinaries["moneypenny"] {
		os.RemoveAll(stageDir)
		return "", fmt.Errorf("moneypenny binary not found in archive")
	}

	return stageDir, nil
}

// waitForIdle polls until all sessions are idle or context is cancelled.
func (u *Updater) waitForIdle(ctx context.Context) bool {
	// Check immediately first.
	if u.checker.AllSessionsIdle() {
		return true
	}

	ticker := time.NewTicker(u.idleCheckFreq)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if u.checker.AllSessionsIdle() {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
}

// swapAndRestart replaces the current binary and re-execs.
func (u *Updater) swapAndRestart(stagedDir string) error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get current executable: %w", err)
	}
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	newExe := filepath.Join(stagedDir, "moneypenny")
	if _, err := os.Stat(newExe); err != nil {
		return fmt.Errorf("staged binary not found: %w", err)
	}

	// Also swap mi6-client if it was staged and exists alongside current binary.
	currentDir := filepath.Dir(currentExe)
	newMI6 := filepath.Join(stagedDir, "mi6-client")
	if _, err := os.Stat(newMI6); err == nil {
		currentMI6 := filepath.Join(currentDir, "mi6-client")
		if _, err := os.Stat(currentMI6); err == nil {
			u.vlog.Printf("swapping mi6-client: %s -> %s", newMI6, currentMI6)
			if err := atomicSwap(newMI6, currentMI6); err != nil {
				u.vlog.Printf("warning: failed to swap mi6-client: %v", err)
				// Non-fatal — continue with moneypenny swap.
			}
		}
	}

	// Swap the moneypenny binary.
	u.vlog.Printf("swapping moneypenny: %s -> %s", newExe, currentExe)
	if err := atomicSwap(newExe, currentExe); err != nil {
		return fmt.Errorf("swap binary: %w", err)
	}

	// Clean up staged directory.
	os.RemoveAll(stagedDir)

	// Re-exec with the same arguments.
	u.vlog.Printf("re-execing with args: %v", u.execArgs)
	return reExec(currentExe, u.execArgs)
}

// atomicSwap replaces dst with src using rename (atomic on same filesystem)
// or copy+rename if cross-device.
func atomicSwap(src, dst string) error {
	// Try to rename the old binary out of the way first, then move new in.
	backup := dst + ".old"
	os.Remove(backup) // clean up any previous backup

	if err := os.Rename(dst, backup); err != nil {
		return fmt.Errorf("backup old binary: %w", err)
	}

	if err := os.Rename(src, dst); err != nil {
		// Rename failed (cross-device), fall back to copy.
		if err2 := copyFile(src, dst); err2 != nil {
			// Restore backup.
			os.Rename(backup, dst)
			return fmt.Errorf("copy new binary: %w", err2)
		}
	}

	os.Remove(backup)
	return nil
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// isNewer returns true if version a is newer than version b.
// Simple semver comparison (major.minor.patch).
func isNewer(a, b string) bool {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return true
		}
		if pa[i] < pb[i] {
			return false
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var parts [3]int
	fmt.Sscanf(v, "%d.%d.%d", &parts[0], &parts[1], &parts[2])
	return parts
}
