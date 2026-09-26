package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"james/moneypenny/pkg/envelope"
)

const (
	maxCommandJobsPerSession = 4
	maxCommandJobs           = 16
	maxCommandJobRecords     = 64
	maxCommandOutput         = 4 << 20
	commandTimeout           = time.Hour
	commandRetention         = 24 * time.Hour
)

type commandJob struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Stdout    string    `json:"stdout"`
	Stderr    string    `json:"stderr"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`

	sessionID string
	cancel    context.CancelFunc
	stopped   bool
	done      chan struct{}
}

type boundedOutput struct {
	file      *os.File
	remaining int
	truncated bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > w.remaining {
		p = p[:w.remaining]
		w.truncated = true
	}
	written, err := w.file.Write(p)
	w.remaining -= written
	if err != nil {
		return written, err
	}
	return n, nil
}

func (h *Handler) jobPath(sessionID, id string) (string, error) {
	if len(id) != 32 {
		return "", &gadgetError{"invalid_request", "Invalid job ID"}
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", &gadgetError{"invalid_request", "Invalid job ID"}
	}
	root, err := h.sessionDirPath(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "jobs", id), nil
}

func saveCommandJob(job *commandJob, dir string) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "status.tmp")
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "status.json"))
}

func (h *Handler) readCommandJob(sessionID, id string) (*commandJob, error) {
	dir, err := h.jobPath(sessionID, id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, &gadgetError{"not_found", "Job not found in this session"}
	}
	if err != nil {
		return nil, err
	}
	var job commandJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, fmt.Errorf("read job status: %w", err)
	}
	if job.ID != id {
		return nil, fmt.Errorf("job status identity mismatch")
	}
	return &job, nil
}

func (h *Handler) runGadget(sessionID string, request gadgetRequest) (any, error) {
	switch request.Method {
	case "run.start":
		var data struct {
			Argv []string `json:"argv"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		if len(data.Argv) == 0 || len(data.Argv) > 128 || strings.TrimSpace(data.Argv[0]) == "" {
			return nil, &gadgetError{"invalid_request", "Supply a command and up to 127 arguments"}
		}
		for _, arg := range data.Argv {
			if strings.ContainsRune(arg, 0) {
				return nil, &gadgetError{"invalid_request", "Command arguments cannot contain NUL"}
			}
		}
		return h.startCommandJob(sessionID, data.Argv)
	case "run.list":
		var data struct{}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		h.jobsMu.Lock()
		entries, err := h.commandJobEntries(sessionID)
		h.jobsMu.Unlock()
		if err != nil {
			return nil, err
		}
		jobs := make([]commandJob, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			job, err := h.readCommandJob(sessionID, entry.Name())
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, *job)
		}
		return jobs, nil
	case "run.status", "run.stop":
		var data struct {
			ID string `json:"id"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		h.jobsMu.Lock()
		_, err := h.commandJobEntries(sessionID)
		h.jobsMu.Unlock()
		if err != nil {
			return nil, err
		}
		job, err := h.readCommandJob(sessionID, data.ID)
		if err != nil {
			return nil, err
		}
		if request.Method == "run.stop" && job.Status == "running" {
			h.jobsMu.Lock()
			running := h.jobs[data.ID]
			if running != nil && running.sessionID == sessionID {
				running.stopped = true
				running.cancel()
			}
			h.jobsMu.Unlock()
			if running == nil {
				return nil, fmt.Errorf("job is no longer running; check status")
			}
		}
		return *job, nil
	}
	return nil, &gadgetError{"unknown_method", "Unknown run operation"}
}

func (h *Handler) startCommandJob(sessionID string, argv []string) (any, error) {
	session, err := h.store.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load job session: %w", err)
	}
	if session == nil {
		return nil, &gadgetError{"not_found", "Session not found"}
	}
	if session.Path == "" {
		return nil, &gadgetError{"invalid_request", "Session has no working directory"}
	}
	h.jobsMu.Lock()
	defer h.jobsMu.Unlock()
	if h.jobsClosing {
		return nil, &gadgetError{"unavailable", "Daemon is stopping"}
	}
	entries, err := h.commandJobEntries(sessionID)
	if err != nil {
		return nil, err
	}
	if len(entries) >= maxCommandJobRecords {
		return nil, &gadgetError{"capacity", "Too many retained command jobs; completed jobs expire after 24 hours"}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random[:])
	dir, err := h.jobPath(sessionID, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	stdoutPath, stderrPath := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		stdout.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	job := &commandJob{ID: id, Status: "running", Stdout: stdoutPath, Stderr: stderrPath,
		StartedAt: time.Now().UTC(), sessionID: sessionID, cancel: cancel, done: make(chan struct{})}
	if h.jobsClosing {
		cancel()
		stdout.Close()
		stderr.Close()
		_ = os.RemoveAll(dir)
		return nil, &gadgetError{"unavailable", "Daemon is stopping"}
	}
	activeForSession := 0
	for _, running := range h.jobs {
		if running.sessionID == sessionID {
			activeForSession++
		}
	}
	if len(h.jobs) >= maxCommandJobs || activeForSession >= maxCommandJobsPerSession {
		cancel()
		stdout.Close()
		stderr.Close()
		_ = os.RemoveAll(dir)
		return nil, &gadgetError{"capacity", "Too many running commands"}
	}
	if err := saveCommandJob(job, dir); err != nil {
		cancel()
		stdout.Close()
		stderr.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = session.Path
	cmd.Stdin = nil
	environment, err := sessionEnvironment(session)
	if err != nil {
		cancel()
		stdout.Close()
		stderr.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cmd.Env = cmd.Environ()
	for key, value := range environment {
		if strings.HasPrefix(strings.ToUpper(key), "JAMES_GADGETS_") || strings.HasPrefix(strings.ToUpper(key), "JAMES_HEM_") {
			continue
		}
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out := &boundedOutput{file: stdout, remaining: maxCommandOutput}
	errOut := &boundedOutput{file: stderr, remaining: maxCommandOutput}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Start(); err != nil {
		cancel()
		stdout.Close()
		stderr.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start command: %w", err)
	}
	h.jobs[id] = job
	h.jobsWG.Add(1)
	go func() {
		defer h.jobsWG.Done()
		defer close(job.done)
		waitErr := cmd.Wait()
		stdout.Close()
		stderr.Close()
		cancel()
		h.jobsMu.Lock()
		switch {
		case job.stopped:
			job.Status = "stopped"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			job.Status = "timed_out"
		case waitErr != nil:
			job.Status = "failed"
		default:
			job.Status = "completed"
		}
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			if code >= 0 {
				job.ExitCode = &code
			}
		}
		job.Truncated = out.truncated || errOut.truncated
		job.EndedAt = time.Now().UTC()
		err := saveCommandJob(job, dir)
		delete(h.jobs, id)
		h.jobsMu.Unlock()
		if err != nil {
			h.vlog("save command job %s: %v", id, err)
			return
		}
		h.callbackCommandJob(job)
	}()
	return job, nil
}

func (h *Handler) callbackCommandJob(job *commandJob) {
	h.jobsMu.Lock()
	stopping := h.jobsClosing
	h.jobsMu.Unlock()
	if stopping {
		return // startup recovery delivers the persisted terminal result
	}
	prompt := fmt.Sprintf("Background command job %s finished: status=%s, exit_code=%v. stdout: %s; stderr: %s. Inspect these files if needed; output is capped at 4 MiB per stream.",
		job.ID, job.Status, exitCodeLabel(job.ExitCode), job.Stdout, job.Stderr)
	claimed, err := h.store.QueueJobCallback(job.sessionID, job.ID, prompt)
	if err != nil {
		h.vlog("queue command callback %s: %v", job.ID, err)
		return
	}
	if claimed {
		if h.notifyWriter != nil {
			_ = h.notifyWriter.SendAsync(envelope.EventChatStatus, job.sessionID, map[string]string{
				"status": "working", "reason": "command_callback",
			})
		}
		go h.continueQueuedPrompts(job.sessionID)
	}
}

func exitCodeLabel(code *int) string {
	if code == nil {
		return "unavailable"
	}
	return fmt.Sprint(*code)
}

func (h *Handler) stopCommandJobs() {
	h.jobsMu.Lock()
	h.jobsClosing = true
	stop, done := h.jobsCleanupStop, h.jobsCleanupDone
	h.jobsCleanupStop = nil
	h.jobsCleanupDone = nil
	if stop != nil {
		close(stop)
	}
	for _, job := range h.jobs {
		job.stopped = true
		job.cancel()
	}
	h.jobsMu.Unlock()
	if done != nil {
		<-done
	}
	h.jobsWG.Wait()
}

func (h *Handler) cleanupCommandJobs(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			sessions, err := h.store.ListSessions()
			if err != nil {
				h.vlog("list sessions for command cleanup: %v", err)
				continue
			}
			for _, session := range sessions {
				h.jobsMu.Lock()
				_, err := h.commandJobEntries(session.SessionID)
				h.jobsMu.Unlock()
				if err != nil {
					h.vlog("cleanup command jobs for %s: %v", session.SessionID, err)
				}
			}
		}
	}
}

func (h *Handler) stopSessionCommandJobs(sessionID string) {
	h.jobsMu.Lock()
	var done []<-chan struct{}
	for _, job := range h.jobs {
		if job.sessionID == sessionID {
			job.stopped = true
			job.cancel()
			done = append(done, job.done)
		}
	}
	h.jobsMu.Unlock()
	for _, ch := range done {
		<-ch
	}
}

func (h *Handler) commandJobEntries(sessionID string) ([]os.DirEntry, error) {
	root, err := h.sessionDirPath(sessionID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "jobs")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		job, err := h.readCommandJob(sessionID, entry.Name())
		if err != nil {
			return nil, err
		}
		if job.Status != "running" && !job.EndedAt.IsZero() && time.Since(job.EndedAt) >= commandRetention {
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return nil, fmt.Errorf("remove expired command job: %w", err)
			}
			if err := h.store.DeleteJobCallback(sessionID, job.ID); err != nil {
				return nil, fmt.Errorf("remove expired command callback record: %w", err)
			}
			continue
		}
		kept = append(kept, entry)
	}
	return kept, nil
}

func (h *Handler) recoverCommandJobs() {
	sessions, err := h.store.ListSessions()
	if err != nil {
		h.vlog("recover command jobs: %v", err)
		return
	}
	for _, session := range sessions {
		root, err := h.sessionDirPath(session.SessionID)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, "jobs"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			h.vlog("read command jobs for %s: %v", session.SessionID, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			job, err := h.readCommandJob(session.SessionID, entry.Name())
			if err != nil {
				h.vlog("recover command job: %v", err)
				continue
			}
			job.sessionID = session.SessionID
			if job.Status == "running" {
				h.jobsMu.Lock()
				active := h.jobs[job.ID] != nil
				h.jobsMu.Unlock()
				if active {
					continue
				}
				job.Status = "interrupted"
				job.EndedAt = time.Now().UTC()
				if err := saveCommandJob(job, filepath.Join(root, "jobs", job.ID)); err != nil {
					h.vlog("mark interrupted command job %s: %v", job.ID, err)
					continue
				}
			}
			h.callbackCommandJob(job)
		}
		if _, err := h.commandJobEntries(session.SessionID); err != nil {
			h.vlog("prune command jobs for %s: %v", session.SessionID, err)
		}
	}
}
