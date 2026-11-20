// Package artifact defines bounded, static conversation artifacts.
package artifact

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	MaxFileSize    = 25 * 1024 * 1024
	MaxSessionSize = 100 * 1024 * 1024
	MaxFiles       = 100
	MaxExpanded    = 100 * 1024 * 1024
	MaxEntries     = 1000
	ChunkSize      = 192 * 1024
)

type Metadata struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	View   string `json:"view,omitempty"`
}

type ReadRequest struct {
	SessionID    string `json:"session_id"`
	ID           string `json:"id"`
	Offset       int64  `json:"offset"`
	MetadataOnly bool   `json:"metadata_only,omitempty"`
}

type Chunk struct {
	Artifact Metadata `json:"artifact"`
	Content  []byte   `json:"content"`
}

// Inspect identifies viewable files. Other files remain downloadable.
func Inspect(filename, name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return "html"
	case ".pdf":
		return "pdf"
	case ".md", ".markdown":
		return "markdown"
	case ".txt", ".text", ".log":
		return "text"
	case ".zip":
		r, err := zip.OpenReader(filename)
		if err != nil {
			return "" // Invalid archives are still downloadable as ordinary files.
		}
		defer r.Close()
		if err := validateZIP(&r.Reader); err != nil {
			return ""
		}
		for _, f := range r.File {
			if f.Name == "index.html" && f.Mode().IsRegular() {
				return "zip"
			}
		}
	}
	return ""
}

func validateZIP(r *zip.Reader) error {
	if len(r.File) > MaxEntries {
		return fmt.Errorf("archive exceeds %d entries", MaxEntries)
	}
	var total uint64
	seen := make(map[string]bool)
	for _, f := range r.File {
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) ||
			path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") ||
			!filepath.IsLocal(filepath.FromSlash(name)) ||
			(!f.Mode().IsRegular() && !f.FileInfo().IsDir()) || seen[strings.ToLower(name)] {
			return fmt.Errorf("unsafe or duplicate archive entry")
		}
		for _, segment := range strings.Split(name, "/") {
			if strings.TrimRight(segment, ". ") != segment {
				return fmt.Errorf("unsafe archive entry")
			}
		}
		seen[strings.ToLower(name)] = true
		if f.UncompressedSize64 > MaxExpanded || total > MaxExpanded-f.UncompressedSize64 {
			return fmt.Errorf("archive expands beyond %d bytes", MaxExpanded)
		}
		total += f.UncompressedSize64
	}
	return nil
}

// Extract expands a validated archive into a new private directory.
func Extract(filename, destination string) error {
	r, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := validateZIP(&r.Reader); err != nil {
		return err
	}
	var total int64
	for _, f := range r.File {
		target := filepath.Join(destination, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := extractFile(f, target, &total); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string, total *int64) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(src, MaxExpanded-*total+1))
	closeErr := dst.Close()
	*total += n
	if copyErr != nil {
		return copyErr
	}
	if *total > MaxExpanded {
		return fmt.Errorf("archive expansion limit exceeded")
	}
	return closeErr
}
