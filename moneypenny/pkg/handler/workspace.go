package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"james/moneypenny/pkg/envelope"
)

func scratchRoot() (string, error) {
	if runtime.GOOS == "windows" {
		dir, err := os.UserCacheDir() // LocalAppData on Windows.
		return filepath.Join(dir, "james", "workspaces"), err
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "james", "workspaces"), err
}

func removeScratchWorkspace(root, id string) error {
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("invalid session ID")
	}
	dir, err := os.OpenRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.RemoveAll(id)
}

func createSessionWorktree(ctx context.Context, worktree *envelope.WorktreeData) (string, error) {
	if !filepath.IsLocal(worktree.Folder) || worktree.Folder == "." ||
		strings.TrimSpace(worktree.Folder) != worktree.Folder ||
		strings.ContainsAny(worktree.Folder, `/\:<>|?*"`) ||
		(runtime.GOOS == "windows" && strings.HasSuffix(worktree.Folder, ".")) {
		return "", fmt.Errorf("worktree folder must be a single directory name")
	}
	if worktree.Branch == "" || strings.HasPrefix(worktree.Branch, "-") || strings.HasPrefix(worktree.Branch, "@{") {
		return "", fmt.Errorf("worktree requires a new branch name")
	}
	repo := worktree.Repository
	if strings.HasPrefix(repo, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		repo = filepath.Join(home, repo[2:])
	}
	if repo == "" {
		return "", fmt.Errorf("select a Git repository root")
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	runGit := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		return cmd.CombinedOutput()
	}
	out, err := runGit("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a Git checkout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	selected, err := os.Stat(repo)
	if err != nil {
		return "", err
	}
	top, err := os.Stat(strings.TrimRight(string(out), "\r\n"))
	if err != nil || !os.SameFile(selected, top) {
		return "", fmt.Errorf("select the root of the Git checkout, not a subfolder")
	}
	dest := filepath.Join(filepath.Dir(repo), worktree.Folder)
	if err := os.Mkdir(dest, 0777); errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("worktree destination already exists: %s", dest)
	} else if err != nil {
		return "", err
	}
	out, err = runGit("worktree", "add", "-b", worktree.Branch, "--", dest, "HEAD")
	if err != nil {
		if cleanupErr := os.Remove(dest); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			log.Printf("could not remove failed worktree destination %q: %v", dest, cleanupErr)
		}
		return "", fmt.Errorf("creating Git worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return dest, nil
}
