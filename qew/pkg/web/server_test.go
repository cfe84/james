package web

import (
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInstallableQewRetainsAuthentication(t *testing.T) {
	s := NewServer(nil, "", "test-password", false, nil, "", nil, nil)
	t.Cleanup(s.Close)
	mux, err := s.routes()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://qew.example"+path, nil)
		if authenticated {
			now := time.Now()
			r.AddCookie(s.sessionCookie(s.makeToken(now, now)))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := get("/manifest.webmanifest", false)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest response = %d, %v", w.Code, w.Header())
	}
	var manifest struct {
		StartURL string `json:"start_url"`
		Display  string
		Icons    []struct{ Src, Sizes string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.StartURL != "/" || manifest.Display != "standalone" {
		t.Fatalf("unexpected manifest launch settings: %+v", manifest)
	}
	requiredSizes := map[string]bool{"192x192": false, "512x512": false}
	for _, icon := range manifest.Icons {
		w := get(icon.Src, false)
		config, err := png.DecodeConfig(w.Body)
		size := strconv.Itoa(config.Width) + "x" + strconv.Itoa(config.Height)
		if w.Code != http.StatusOK || err != nil || icon.Sizes != size {
			t.Fatalf("icon %s (%s): status %d, actual size %s, decode error %v", icon.Src, icon.Sizes, w.Code, size, err)
		}
		requiredSizes[size] = true
	}
	if !requiredSizes["192x192"] || !requiredSizes["512x512"] {
		t.Fatal("manifest does not provide both launcher icon sizes")
	}
	for _, path := range []string{manifest.StartURL, "/api", "/app.js"} {
		w := get(path, false)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
			t.Fatalf("unauthenticated %s: status %d, location %q", path, w.Code, w.Header().Get("Location"))
		}
	}
	for _, page := range []*httptest.ResponseRecorder{get("/login", false), get(manifest.StartURL, true)} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `rel="manifest" href="/manifest.webmanifest"`) {
			t.Fatalf("page did not advertise the manifest (status %d)", page.Code)
		}
	}
}
