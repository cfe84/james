package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"james/moneypenny/pkg/artifact"
)

const artifactPreviewTTL = 30 * time.Minute
const maxMarkdownPreviewSource = 1024 * 1024

var artifactMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

type artifactPreview struct {
	mu        sync.Mutex
	dir       string
	root      string
	session   string
	id        string
	sha256    string
	timer     *time.Timer
	created   time.Time
	entryName string
	format    string
}

func renderArtifactMarkdown(filename, destination string) error {
	in, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer in.Close()
	source, err := io.ReadAll(io.LimitReader(in, maxMarkdownPreviewSource+1))
	if err != nil {
		return err
	}
	truncated := len(source) > maxMarkdownPreviewSource
	if truncated {
		end := maxMarkdownPreviewSource
		for end > 0 && !utf8.RuneStart(source[end]) {
			end--
		}
		source = source[:end]
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.WriteString(out, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Markdown preview</title><style>:root{color-scheme:light dark}body{max-width:72ch;margin:2rem auto;padding:0 1rem;font:1rem/1.6 sans-serif;overflow-wrap:anywhere}pre,table{display:block;overflow:auto}td,th{padding:.25rem .5rem}</style><main>`); err != nil {
		return err
	}
	if truncated {
		if _, err := io.WriteString(out, `<p><strong>Preview truncated. Download the file to read the full document.</strong></p>`); err != nil {
			return err
		}
	}
	if err := artifactMarkdown.Convert(source, out); err != nil {
		return err
	}
	return out.Close()
}

func (s *Server) removeArtifactFiles(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		s.vlog.Printf("artifact temporary cleanup failed: %v", err)
	}
}

func (s *Server) artifactTempDir() (string, error) {
	root := s.artifactCacheDir
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cache, "james", "qew", "artifacts")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "preview-") {
			continue
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if time.Since(info.ModTime()) >= artifactPreviewTTL {
			s.removeArtifactFiles(filepath.Join(root, entry.Name()))
		}
	}
	return os.MkdirTemp(root, "preview-")
}

func (s *Server) expireArtifact(token string) {
	s.artifactMu.Lock()
	entry := s.artifacts[token]
	delete(s.artifacts, token)
	s.artifactMu.Unlock()
	if entry != nil {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.timer.Stop()
		s.removeArtifactFiles(entry.dir)
		<-s.artifactSlots
	}
}

func (s *Server) closeArtifactPreviews() {
	s.artifactMu.Lock()
	tokens := make([]string, 0, len(s.artifacts))
	for token := range s.artifacts {
		tokens = append(tokens, token)
	}
	s.artifactMu.Unlock()
	for _, token := range tokens {
		s.expireArtifact(token)
	}
}

func (s *Server) artifactChunk(session, id string, offset int64, metadataOnly bool) (artifact.Chunk, error) {
	raw, err := json.Marshal(artifact.ReadRequest{SessionID: session, ID: id, Offset: offset, MetadataOnly: metadataOnly})
	if err != nil {
		return artifact.Chunk{}, err
	}
	resp, err := s.hem.Send(&Request{Verb: "read", Noun: "artifact", Args: []string{string(raw)}})
	if err != nil {
		return artifact.Chunk{}, err
	}
	if resp.Status != "ok" {
		return artifact.Chunk{}, fmt.Errorf("artifact unavailable: %s", resp.Message)
	}
	var chunk artifact.Chunk
	if err := json.Unmarshal(resp.Data, &chunk); err != nil {
		return chunk, err
	}
	m := chunk.Artifact
	if m.ID != id || m.Size < 0 || m.Size > artifact.MaxFileSize || len(chunk.Content) > artifact.ChunkSize ||
		offset > m.Size || int64(len(chunk.Content)) > m.Size-offset ||
		(!metadataOnly && offset < m.Size && len(chunk.Content) == 0) || filepath.Base(m.Name) != m.Name ||
		strings.ContainsAny(m.Name, "/\\\x00\r\n") || m.Name == "" || m.Name == "." {
		return chunk, fmt.Errorf("invalid artifact response")
	}
	return chunk, nil
}

func (s *Server) fetchArtifact(r *http.Request, session, id, filename string) (artifact.Metadata, error) {
	out, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return artifact.Metadata{}, err
	}
	defer out.Close()
	hash := sha256.New()
	var meta artifact.Metadata
	var offset int64
	for {
		if err := r.Context().Err(); err != nil {
			return meta, err
		}
		chunk, err := s.artifactChunk(session, id, offset, false)
		if err != nil {
			return meta, err
		}
		if offset == 0 {
			meta = chunk.Artifact
		} else if meta != chunk.Artifact {
			return meta, fmt.Errorf("artifact changed during transfer")
		}
		if _, err := io.MultiWriter(out, hash).Write(chunk.Content); err != nil {
			return meta, err
		}
		offset += int64(len(chunk.Content))
		if offset == meta.Size {
			break
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != meta.SHA256 {
		return meta, fmt.Errorf("artifact checksum mismatch")
	}
	return meta, out.Close()
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/artifacts/"), "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if parts[2] == "review" {
		s.submitArtifactReview(w, r, parts[0], parts[1])
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if parts[2] != "view" && parts[2] != "preview" && parts[2] != "download" {
		http.NotFound(w, r)
		return
	}
	if parts[2] == "view" {
		s.serveArtifactViewer(w, parts[0], parts[1])
		return
	}
	review := parts[2] == "preview" && r.URL.Query().Get("review") == "1"
	var suffix string
	if review {
		if strings.HasPrefix(parts[0], artifact.ProjectScope) {
			http.Error(w, "Project artifacts do not have a conversation to send reviews to", http.StatusUnsupportedMediaType)
			return
		}
		suffix = "?review=1"
	}
	if parts[2] == "preview" {
		chunk, err := s.artifactChunk(parts[0], parts[1], 0, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.artifactMu.Lock()
		var existing string
		for token, entry := range s.artifacts {
			if entry.session == parts[0] && entry.id == parts[1] && entry.sha256 == chunk.Artifact.SHA256 {
				existing = "/artifact-view/" + token + "/" + entry.entryName
				break
			}
		}
		s.artifactMu.Unlock()
		if existing != "" {
			http.Redirect(w, r, existing+suffix, http.StatusSeeOther)
			return
		}
	}
	// Evict the oldest preview rather than forcing users to wait for expiry.
	if len(s.artifactSlots) == cap(s.artifactSlots) {
		s.artifactMu.Lock()
		var oldest string
		for token, entry := range s.artifacts {
			if oldest == "" || entry.created.Before(s.artifacts[oldest].created) {
				oldest = token
			}
		}
		s.artifactMu.Unlock()
		if oldest != "" {
			s.expireArtifact(oldest)
		}
	}
	select {
	case s.artifactSlots <- struct{}{}:
	default:
		http.Error(w, "Too many artifact transfers; try again shortly", http.StatusTooManyRequests)
		return
	}
	retained := false
	defer func() {
		if !retained {
			<-s.artifactSlots
		}
	}()
	dir, err := s.artifactTempDir()
	if err != nil {
		http.Error(w, "Cannot create artifact temporary storage", http.StatusInternalServerError)
		return
	}
	defer func() {
		if !retained {
			s.removeArtifactFiles(dir)
		}
	}()
	filename := filepath.Join(dir, "content")
	meta, err := s.fetchArtifact(r, parts[0], parts[1], filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if parts[2] == "download" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": meta.Name}))
		in, err := os.Open(filename)
		if err != nil {
			http.Error(w, "Cannot read artifact", http.StatusInternalServerError)
			return
		}
		defer in.Close()
		http.ServeContent(w, r, meta.Name, time.Time{}, in)
		return
	}
	view := artifact.Inspect(filename, meta.Name)
	if view == "" {
		http.Error(w, "This artifact is download-only", http.StatusUnsupportedMediaType)
		return
	}
	root := filepath.Join(dir, "view")
	if err := os.Mkdir(root, 0700); err != nil {
		http.Error(w, "Cannot prepare artifact preview", http.StatusInternalServerError)
		return
	}
	entryName := "index.html"
	switch view {
	case "zip":
		err = artifact.Extract(filename, root)
	case "markdown":
		err = renderArtifactMarkdown(filename, filepath.Join(root, entryName))
	case "text":
		entryName = "document.txt"
		err = os.Rename(filename, filepath.Join(root, entryName))
	case "pdf":
		entryName = "document.pdf"
		err = os.Rename(filename, filepath.Join(root, entryName))
	default:
		err = os.Rename(filename, filepath.Join(root, entryName))
	}
	if err != nil {
		http.Error(w, "Cannot prepare artifact preview: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		http.Error(w, "Cannot allocate artifact preview", http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(random[:])
	entry := &artifactPreview{dir: dir, root: root, session: parts[0], id: parts[1], sha256: meta.SHA256, created: time.Now(), entryName: entryName, format: view}
	s.artifactMu.Lock()
	entry.timer = time.AfterFunc(artifactPreviewTTL, func() { s.expireArtifact(token) })
	s.artifacts[token] = entry
	s.artifactMu.Unlock()
	retained = true
	http.Redirect(w, r, "/artifact-view/"+token+"/"+entryName+suffix, http.StatusSeeOther)
}

func (s *Server) handleArtifactPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/artifact-view/"), "/", 2)
	if len(parts) != 2 || parts[1] == "" || path.Clean(parts[1]) != parts[1] ||
		strings.ContainsAny(parts[1], "\\:\x00") || strings.HasPrefix(parts[1], "/") ||
		parts[1] == ".." || strings.HasPrefix(parts[1], "../") {
		http.NotFound(w, r)
		return
	}
	s.artifactMu.Lock()
	entry := s.artifacts[parts[0]]
	if entry != nil {
		entry.mu.Lock()
	}
	s.artifactMu.Unlock()
	if entry == nil {
		http.Error(w, "Preview expired; open View again from the conversation", http.StatusGone)
		return
	}
	// Recheck ownership/existence so a deleted session cannot keep serving cached content.
	if _, err := s.artifactChunk(entry.session, entry.id, 0, true); err != nil {
		entry.mu.Unlock()
		s.expireArtifact(parts[0])
		http.Error(w, "Artifact is no longer available", http.StatusGone)
		return
	}
	defer entry.mu.Unlock()
	target := filepath.Join(entry.root, filepath.FromSlash(parts[1]))
	if r.URL.Query().Get("review") == "1" {
		w.Header().Set("Cache-Control", "no-store")
		if parts[1] != entry.entryName || !artifactReviewable(entry.format) || strings.HasPrefix(entry.session, artifact.ProjectScope) {
			http.Error(w, "This artifact cannot be reviewed", http.StatusUnsupportedMediaType)
			return
		}
		serveArtifactReviewDocument(w, target, entry, artifactAssetSources(r.Host, parts[0]))
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	source := artifactAssetSources(r.Host, parts[0])
	policy := "default-src 'none'; script-src 'unsafe-inline' " + source + "; style-src 'unsafe-inline' " + source + "; img-src data: blob: " + source + "; font-src data: " + source + "; media-src blob: " + source + "; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
	if entry.format == "markdown" || entry.format == "text" {
		policy = "sandbox; default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"
	} else if strings.ToLower(filepath.Ext(target)) == ".pdf" {
		// Native PDF viewers need their own embedded renderer, not an HTML sandbox.
		policy = "default-src 'none'; object-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'none'"
	} else {
		policy = "sandbox allow-scripts; " + policy
	}
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	contentType := mime.TypeByExtension(filepath.Ext(target))
	if entry.format == "text" {
		contentType = "text/plain; charset=utf-8"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	in, err := os.Open(target)
	if err != nil {
		http.Error(w, "Cannot read artifact preview", http.StatusInternalServerError)
		return
	}
	defer in.Close()
	http.ServeContent(w, r, info.Name(), time.Time{}, in)
}

func artifactAssetSources(host, token string) string {
	base := url.URL{Scheme: "http", Host: host, Path: "/artifact-view/" + token + "/"}
	source := base.String()
	base.Scheme = "https"
	return source + " " + base.String()
}
