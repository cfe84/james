// Package updater implements auto-update for moneypenny via GitHub releases.
// It periodically checks for new releases, downloads the appropriate binary,
// and performs a binary swap + re-exec when all sessions are idle.
package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// releaseManifestPublicKeyBase64 authenticates release manifests published by the
// James release workflow. The corresponding private key is never stored in
// the repository.
const releaseManifestPublicKeyBase64 = "FRfu0TrG4DaIRr5155mKBER3ktucVpZ4oc9Iy5rm1zM="

type releaseManifest struct {
	Version   string            `json:"version"`
	Artifacts map[string]string `json:"artifacts"`
}

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
	vlog    *log.Logger // verbose (only with -v)
	slog    *log.Logger // standard (always visible)

	// triggerCh signals the Run loop to perform an out-of-band cycle.
	// Buffered with size 1; if a trigger is already pending, additional
	// requests are dropped (the pending one will catch any new release).
	triggerCh chan struct{}

	// For re-exec
	execArgs      []string // os.Args
	beforeRestart func()
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

// WithBeforeRestart registers cleanup that must finish before the replacement
// process starts. Windows cannot exec in place; without this, os.Exit in the
// Windows re-exec path skips main's defers and can leave SQLite WAL mappings
// open while the replacement process initializes.
func WithBeforeRestart(f func()) Option {
	return func(u *Updater) { u.beforeRestart = f }
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
		slog:           log.New(os.Stderr, "", log.LstdFlags),
		execArgs:       os.Args,
		triggerCh:      make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// TriggerCheck signals the updater to run a check cycle as soon as possible.
// Returns true if a trigger was queued, false if one was already pending.
// Safe to call concurrently.
func (u *Updater) TriggerCheck() bool {
	select {
	case u.triggerCh <- struct{}{}:
		return true
	default:
		return false
	}
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
	// Do an initial check shortly after startup (or sooner if triggered).
	select {
	case <-time.After(30 * time.Second):
	case <-u.triggerCh:
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
		case <-u.triggerCh:
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
		u.slog.Printf("update check failed: %v", err)
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

	u.slog.Printf("update available: v%s → v%s", u.currentVersion, tag)

	// 2. Download and stage.
	u.setStatus(StatusDownloading)
	stagedDir, err := u.downloadAndStage(ctx, release)
	if err != nil {
		u.slog.Printf("download failed: %v", err)
		u.setError(err)
		return
	}

	u.mu.Lock()
	u.stagedDir = stagedDir
	u.mu.Unlock()

	// 3. Wait for idle.
	u.setStatus(StatusWaitingIdle)
	u.slog.Printf("v%s downloaded, waiting for all sessions to be idle before restarting", tag)
	u.vlog.Printf("update staged at %s", stagedDir)

	if !u.waitForIdle(ctx) {
		u.vlog.Printf("context cancelled while waiting for idle")
		return
	}

	// 4. Swap and restart.
	u.setStatus(StatusRestarting)
	u.slog.Printf("all sessions idle, updating to v%s and restarting", tag)

	if err := u.swapAndRestart(stagedDir); err != nil {
		u.slog.Printf("swap failed: %v", err)
		u.setError(err)
	}
	// If swapAndRestart succeeds, we don't return — the process is replaced.
}

// gitHubRelease is the subset of the GitHub release API we need.
type gitHubRelease struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func findReleaseAsset(rel *gitHubRelease, name string) (releaseAsset, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("release asset %q not found", name)
}

func downloadAsset(ctx context.Context, asset releaseAsset) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s returned %d", asset.Name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", asset.Name, err)
	}
	return data, nil
}

func verifyReleaseManifest(manifestBytes, signature []byte, tag string) (releaseManifest, error) {
	return verifyReleaseManifestWithKey(manifestBytes, signature, tag, releaseManifestPublicKeyBase64)
}

func verifyReleaseManifestWithKey(manifestBytes, signature []byte, tag, publicKeyBase64 string) (releaseManifest, error) {
	publicKeyBytes, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil || len(publicKeyBytes) != ed25519.PublicKeySize {
		return releaseManifest{}, fmt.Errorf("invalid embedded release public key")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKeyBytes), manifestBytes, signature) {
		return releaseManifest{}, fmt.Errorf("release manifest signature verification failed")
	}
	var manifest releaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return releaseManifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if manifest.Version == "" || manifest.Version != strings.TrimPrefix(tag, "v") {
		return releaseManifest{}, fmt.Errorf("release manifest version %q does not match tag %q", manifest.Version, tag)
	}
	if len(manifest.Artifacts) == 0 {
		return releaseManifest{}, fmt.Errorf("release manifest contains no artifacts")
	}
	return manifest, nil
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

// exeSuffix returns ".exe" on Windows, "" otherwise.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// downloadAndStage downloads the platform-specific archive and extracts moneypenny + mi6-client.
func (u *Updater) downloadAndStage(ctx context.Context, rel *gitHubRelease) (string, error) {
	// Find the right asset: james-GOOS-GOARCH.tar.gz (or .zip on Windows).
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	archiveName := fmt.Sprintf("james-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
	archiveAsset, err := findReleaseAsset(rel, archiveName)
	if err != nil {
		return "", err
	}

	manifestAsset, err := findReleaseAsset(rel, "james-manifest.json")
	if err != nil {
		return "", err
	}
	signatureAsset, err := findReleaseAsset(rel, "james-manifest.json.sig")
	if err != nil {
		return "", err
	}
	manifestBytes, err := downloadAsset(ctx, manifestAsset)
	if err != nil {
		return "", err
	}
	signature, err := downloadAsset(ctx, signatureAsset)
	if err != nil {
		return "", err
	}
	manifest, err := verifyReleaseManifest(manifestBytes, signature, rel.TagName)
	if err != nil {
		return "", err
	}
	expectedDigest, ok := manifest.Artifacts[archiveName]
	if !ok {
		return "", fmt.Errorf("release manifest has no digest for %s", archiveName)
	}
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if len(expectedDigest) != sha256.Size*2 {
		return "", fmt.Errorf("invalid digest for %s", archiveName)
	}
	if _, err := hex.DecodeString(expectedDigest); err != nil {
		return "", fmt.Errorf("invalid digest for %s: %w", archiveName, err)
	}

	u.vlog.Printf("downloading %s", archiveName)

	// Download to temp file.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveAsset.BrowserDownloadURL, nil)
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

	archiveFile, err := os.CreateTemp("", "james-update-archive-*")
	if err != nil {
		return "", fmt.Errorf("create archive temp file: %w", err)
	}
	archivePath := archiveFile.Name()
	defer os.Remove(archivePath)
	defer archiveFile.Close()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archiveFile, hash), resp.Body); err != nil {
		return "", fmt.Errorf("download archive: %w", err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != expectedDigest {
		return "", fmt.Errorf("archive digest mismatch for %s", archiveName)
	}
	if err := archiveFile.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}

	// Stage directory.
	tag := strings.TrimPrefix(rel.TagName, "v")
	stageDir := filepath.Join(u.dataDir, "updates", tag)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return "", fmt.Errorf("create stage dir: %w", err)
	}

	suffix := exeSuffix()
	wantBinaries := map[string]bool{
		"moneypenny" + suffix: false,
		"mi6-client" + suffix: false,
	}

	if runtime.GOOS == "windows" {
		archive, openErr := os.Open(archivePath)
		if openErr != nil {
			err = openErr
		} else {
			err = u.extractZip(archive, stageDir, wantBinaries)
			archive.Close()
		}
	} else {
		archive, openErr := os.Open(archivePath)
		if openErr != nil {
			err = openErr
		} else {
			err = u.extractTarGz(archive, stageDir, wantBinaries)
			archive.Close()
		}
	}
	if err != nil {
		os.RemoveAll(stageDir)
		return "", err
	}

	// Verify at least moneypenny was extracted.
	if !wantBinaries["moneypenny"+suffix] {
		os.RemoveAll(stageDir)
		return "", fmt.Errorf("moneypenny binary not found in archive")
	}

	return stageDir, nil
}

// extractTarGz extracts wanted binaries from a tar.gz stream.
func (u *Updater) extractTarGz(r io.Reader, stageDir string, wantBinaries map[string]bool) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Entries are like james-darwin-arm64/moneypenny
		base := filepath.Base(hdr.Name)
		if _, want := wantBinaries[base]; !want {
			continue
		}

		outPath := filepath.Join(stageDir, base)
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("create %s: %w", base, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("extract %s: %w", base, err)
		}
		f.Close()
		wantBinaries[base] = true
		u.vlog.Printf("extracted %s to %s", base, outPath)
	}

	return nil
}

// extractZip extracts wanted binaries from a zip archive.
// Since zip requires random access, we download to a temp file first.
func (u *Updater) extractZip(r io.Reader, stageDir string, wantBinaries map[string]bool) error {
	// Write to temp file since zip needs random access.
	tmp, err := os.CreateTemp("", "james-update-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, r)
	if err != nil {
		return fmt.Errorf("download to temp: %w", err)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return fmt.Errorf("zip reader: %w", err)
	}

	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if _, want := wantBinaries[base]; !want {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", base, err)
		}

		outPath := filepath.Join(stageDir, base)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			rc.Close()
			return fmt.Errorf("create %s: %w", base, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return fmt.Errorf("extract %s: %w", base, err)
		}
		out.Close()
		rc.Close()
		wantBinaries[base] = true
		u.vlog.Printf("extracted %s to %s", base, outPath)
	}

	return nil
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

	suffix := exeSuffix()
	newExe := filepath.Join(stagedDir, "moneypenny"+suffix)
	if _, err := os.Stat(newExe); err != nil {
		return fmt.Errorf("staged binary not found: %w", err)
	}

	// Also swap mi6-client if it was staged and exists alongside current binary.
	currentDir := filepath.Dir(currentExe)
	newMI6 := filepath.Join(stagedDir, "mi6-client"+suffix)
	if _, err := os.Stat(newMI6); err == nil {
		currentMI6 := filepath.Join(currentDir, "mi6-client"+suffix)
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

	// Close resources owned by main before starting a separate replacement
	// process on Windows. On Unix syscall.Exec replaces this process directly,
	// but the hook remains safe and makes shutdown ordering explicit.
	if u.beforeRestart != nil {
		u.beforeRestart()
	}

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
