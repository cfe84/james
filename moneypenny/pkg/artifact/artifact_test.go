package artifact

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestZIPViewingAndExtraction(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		mode        os.FileMode
		view        bool
		safe        bool
	}{
		{"root index", "index.html", 0600, true, true},
		{"nested index", "report/index.html", 0600, false, true},
		{"traversal", "../index.html", 0600, false, false},
		{"backslash traversal", `..\index.html`, 0600, false, false},
		{"absolute", "/index.html", 0600, false, false},
		{"drive", "C:/index.html", 0600, false, false},
		{"symlink", "index.html", os.ModeSymlink | 0600, false, false},
		{"windows normalization", ".. /index.html", 0600, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "report.zip")
			out, err := os.Create(filename)
			if err != nil {
				t.Fatal(err)
			}
			z := zip.NewWriter(out)
			header := &zip.FileHeader{Name: tc.entry}
			header.SetMode(tc.mode)
			w, err := z.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("report content")); err != nil {
				t.Fatal(err)
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
			view := Inspect(filename, "report.zip")
			if (view == "zip") != tc.view {
				t.Fatalf("view=%q", view)
			}
			dest := filepath.Join(dir, "extracted")
			err = Extract(filename, dest)
			if (err == nil) != tc.safe {
				t.Fatalf("extraction error=%v, safe=%v", err, tc.safe)
			}
			if tc.safe {
				data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(tc.entry)))
				if err != nil || string(data) != "report content" {
					t.Fatalf("extracted data=%q, err=%v", data, err)
				}
			}
		})
	}
	// Malicious size claims must be rejected before allocating or extracting.
	header := &zip.FileHeader{Name: "index.html", UncompressedSize64: MaxExpanded + 1}
	if err := validateZIP(&zip.Reader{File: []*zip.File{{FileHeader: *header}}}); err == nil {
		t.Fatal("oversized expansion accepted")
	}
	duplicate := zip.File{FileHeader: zip.FileHeader{Name: "index.html"}}
	if err := validateZIP(&zip.Reader{File: []*zip.File{&duplicate, &duplicate}}); err == nil {
		t.Fatal("duplicate archive entries accepted")
	}
}
