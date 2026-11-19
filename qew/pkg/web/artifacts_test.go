package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"james/moneypenny/pkg/artifact"
)

type artifactHem struct {
	mu       sync.Mutex
	meta     artifact.Metadata
	content  []byte
	requests []artifact.ReadRequest
	deleted  bool
}

func (h *artifactHem) Send(req *Request) (*Response, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.Verb != "read" || req.Noun != "artifact" || len(req.Args) != 1 {
		return nil, fmt.Errorf("unexpected artifact request: %+v", req)
	}
	var data artifact.ReadRequest
	if err := json.Unmarshal([]byte(req.Args[0]), &data); err != nil {
		return nil, err
	}
	h.requests = append(h.requests, data)
	if h.deleted || data.ID != h.meta.ID || data.SessionID != "session" {
		return &Response{Status: "error", Message: "session or artifact missing"}, nil
	}
	var content []byte
	if !data.MetadataOnly {
		end := min(data.Offset+artifact.ChunkSize, int64(len(h.content)))
		content = h.content[data.Offset:end]
	}
	raw, _ := json.Marshal(artifact.Chunk{Artifact: h.meta, Content: content})
	return &Response{Status: "ok", Data: raw}, nil
}

func artifactWebServer(t *testing.T, name string, content []byte) (*Server, *artifactHem, http.Handler) {
	t.Helper()
	sum := sha256.Sum256(content)
	hem := &artifactHem{
		meta:    artifact.Metadata{ID: "00000000-0000-0000-0000-000000000001", Name: name, Title: "Report", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])},
		content: content,
	}
	server := NewServer(hem, "", "password", true, log.New(io.Discard, "", 0), "test", nil, nil)
	server.artifactCacheDir = t.TempDir()
	t.Cleanup(server.Close)
	mux := http.NewServeMux()
	mux.HandleFunc("/artifacts/", server.requireAuth(server.handleArtifact))
	mux.HandleFunc("/artifact-view/", server.handleArtifactPreview)
	return server, hem, mux
}

func artifactRequest(server *Server, mux http.Handler, target string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if authenticated {
		req.AddCookie(server.sessionCookie(server.makeToken(time.Now(), time.Now())))
	}
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, req)
	return out
}

func TestArtifactHTTPViewDownloadAndExpiry(t *testing.T) {
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for _, file := range []struct{ name, content string }{{"index.html", `<script src="report.js"></script><h1>Review</h1>`}, {"report.js", `document.title = 'Report';`}} {
		w, err := z.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		content []byte
		view    bool
	}{
		{"report.html", []byte("<h1>Review</h1>"), true},
		{"report.zip", archive.Bytes(), true},
		{"report.pdf", []byte("%PDF-1.4\n"), true},
		{"report.bin", bytes.Repeat([]byte{0, 1, 2}, artifact.ChunkSize/2), false},
		{"invalid.zip", []byte("not a ZIP"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, hem, mux := artifactWebServer(t, tc.name, tc.content)
			base := "/artifacts/session/" + hem.meta.ID
			if resp := artifactRequest(s, mux, base+"/download", false); resp.Code != http.StatusFound || len(hem.requests) != 0 {
				t.Fatal("unauthenticated request reached Hem")
			}
			download := artifactRequest(s, mux, base+"/download", true)
			if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), tc.content) ||
				!strings.Contains(download.Header().Get("Content-Disposition"), "attachment;") ||
				download.Header().Get("Content-Type") != "application/octet-stream" || len(s.artifactSlots) != 0 {
				t.Fatalf("download changed original or retained transfer: %+v", download)
			}
			view := artifactRequest(s, mux, base+"/view", true)
			if view.Code != http.StatusOK || view.Header().Get("Cache-Control") != "no-store" ||
				!strings.Contains(view.Body.String(), `src="`+base+`/preview"`) {
				t.Fatalf("stable viewer missing preview frame: %d %s", view.Code, view.Body)
			}
			view = artifactRequest(s, mux, base+"/preview", true)
			if !tc.view {
				if view.Code != http.StatusUnsupportedMediaType || len(s.artifactSlots) != 0 {
					t.Fatalf("download-only artifact view accepted: %+v", view)
				}
				return
			}
			if view.Code != http.StatusSeeOther {
				t.Fatalf("view failed: %d %s", view.Code, view.Body)
			}
			location := view.Header().Get("Location")
			// Asset capability URLs work without login cookies, including module requests.
			preview := artifactRequest(s, mux, location, false)
			if preview.Code != http.StatusOK || preview.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("preview failed: %d %s", preview.Code, preview.Body)
			}
			csp := preview.Header().Get("Content-Security-Policy")
			if (tc.name != "report.pdf" && (!strings.Contains(csp, "connect-src 'none'") || !strings.Contains(csp, "sandbox allow-scripts;"))) || strings.Contains(csp, "allow-same-origin") {
				t.Fatalf("unsafe preview CSP: %s", csp)
			}
			token := strings.Split(location, "/")[2]
			entry := s.artifacts[token]
			dir := entry.dir
			if tc.name == "report.zip" {
				asset := artifactRequest(s, mux, "/artifact-view/"+token+"/report.js", false)
				if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "document.title") {
					t.Fatalf("ZIP asset not served: %+v", asset)
				}
			}
			count := len(hem.requests)
			if resp := artifactRequest(s, mux, base+"/preview", true); resp.Header().Get("Location") != location ||
				len(hem.requests) != count+1 || !hem.requests[count].MetadataOnly {
				t.Fatal("repeat view fetched the file again")
			}
			for _, target := range []string{"/artifact-view/unknown/index.html", "/artifact-view/" + token + "/%2e%2e/private"} {
				if resp := artifactRequest(s, mux, target, false); resp.Code == http.StatusOK {
					t.Fatalf("invalid preview path accepted: %s", target)
				}
			}
			hem.deleted = true
			if resp := artifactRequest(s, mux, location, false); resp.Code != http.StatusGone {
				t.Fatal("deleted session preview remained accessible")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) || len(s.artifactSlots) != 0 {
				t.Fatalf("deleted preview not cleaned: %v", err)
			}
		})
	}
}

func TestArtifactTransferFailureAndCacheEviction(t *testing.T) {
	s, hem, mux := artifactWebServer(t, "report.html", []byte("report"))
	hem.meta.SHA256 = strings.Repeat("0", 64)
	base := "/artifacts/session/" + hem.meta.ID + "/preview"
	if resp := artifactRequest(s, mux, base, true); resp.Code != http.StatusBadGateway || len(s.artifactSlots) != 0 {
		t.Fatal("corrupt transfer succeeded or leaked capacity")
	}
	sum := sha256.Sum256(hem.content)
	hem.meta.SHA256 = hex.EncodeToString(sum[:])
	var firstLocation, firstDir string
	for i := 0; i < 5; i++ {
		hem.meta.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		resp := artifactRequest(s, mux, "/artifacts/session/"+hem.meta.ID+"/preview", true)
		if resp.Code != http.StatusSeeOther {
			t.Fatalf("cache did not evict: %d %s", resp.Code, resp.Body)
		}

		if i == 0 {
			firstLocation = resp.Header().Get("Location")
			firstDir = s.artifacts[strings.Split(firstLocation, "/")[2]].dir
		}
	}
	if len(s.artifacts) != 4 || len(s.artifactSlots) != 4 {
		t.Fatal("preview capacity not bounded")
	}
	if resp := artifactRequest(s, mux, firstLocation, false); resp.Code != http.StatusGone {
		t.Fatal("evicted preview still accessible")
	}
	if _, err := os.Stat(firstDir); !os.IsNotExist(err) {
		t.Fatal("eviction retained extracted content")
	}
	s.closeArtifactPreviews()
	if len(s.artifactSlots) != 0 {
		t.Fatal("shutdown retained previews")
	}
}

func TestArtifactViewerRefreshLoadsReplacement(t *testing.T) {
	s, hem, mux := artifactWebServer(t, "report.html", []byte("first status"))
	base := "/artifacts/session/" + hem.meta.ID
	viewer := artifactRequest(s, mux, base+"/view", true)
	first := artifactRequest(s, mux, base+"/preview", true)
	if first.Code != http.StatusSeeOther {
		t.Fatalf("initial preview: %d %s", first.Code, first.Body)
	}
	hem.content = []byte("newer status")
	sum := sha256.Sum256(hem.content)
	hem.meta.SHA256 = hex.EncodeToString(sum[:])
	hem.meta.Size = int64(len(hem.content))
	if refreshed := artifactRequest(s, mux, base+"/view", true); refreshed.Code != http.StatusOK || refreshed.Body.String() != viewer.Body.String() {
		t.Fatal("viewer address changed after replacement")
	}
	next := artifactRequest(s, mux, base+"/preview", true)
	location := next.Header().Get("Location")
	if next.Code != http.StatusSeeOther || location == first.Header().Get("Location") {
		t.Fatalf("refresh reused stale preview: %d %s", next.Code, next.Body)
	}
	for _, target := range []string{location, base + "/download"} {
		resp := artifactRequest(s, mux, target, true)
		if resp.Code != http.StatusOK || resp.Body.String() != string(hem.content) {
			t.Fatalf("%s: got %d %s", target, resp.Code, resp.Body)
		}
	}
	s.closeArtifactPreviews()
	if resp := artifactRequest(s, mux, base+"/preview", true); resp.Code != http.StatusSeeOther {
		t.Fatalf("stable viewer did not recover after preview expiry: %d %s", resp.Code, resp.Body)
	}
}
