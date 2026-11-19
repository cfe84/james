package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

func downloadArtifact(req Request, call func(Request) (response, int), stderr io.Writer) (response, int) {
	destination := req.Data["destination"].(string)
	delete(req.Data, "destination")
	if _, err := os.Lstat(destination); err == nil {
		return response{}, fail(stderr, "output", "destination already exists")
	} else if !os.IsNotExist(err) {
		return response{}, fail(stderr, "output", err.Error())
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".gadgets-artifact-")
	if err != nil {
		return response{}, fail(stderr, "output", err.Error())
	}
	defer func() {
		_ = file.Close()
		if err := os.Remove(file.Name()); err != nil {
			fail(stderr, "cleanup", err.Error())
		}
	}()

	type manifest struct {
		ID     string `json:"id"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	var expected manifest
	hash := sha256.New()
	for offset := int64(0); ; {
		req.Data["offset"] = offset
		result, status := call(req)
		if status != 0 {
			return result, status
		}
		var chunk struct {
			Artifact manifest `json:"artifact"`
			Content  []byte   `json:"content"`
		}
		if err := json.Unmarshal(result.Data, &chunk); err != nil ||
			chunk.Artifact.ID != req.Data["id"] || chunk.Artifact.Size < 0 || chunk.Artifact.Size > 25<<20 {
			return response{}, fail(stderr, "protocol", "invalid artifact manifest")
		}
		if offset == 0 {
			expected = chunk.Artifact
		}
		n := int64(len(chunk.Content))
		if chunk.Artifact != expected || n > expected.Size-offset || (n == 0 && offset < expected.Size) {
			return response{}, fail(stderr, "protocol", "artifact changed or returned an invalid chunk")
		}
		if _, err := io.MultiWriter(file, hash).Write(chunk.Content); err != nil {
			return response{}, fail(stderr, "output", err.Error())
		}
		offset += n
		if offset == expected.Size {
			break
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return response{}, fail(stderr, "protocol", "artifact checksum mismatch")
	}
	if err := file.Close(); err != nil {
		return response{}, fail(stderr, "output", err.Error())
	}
	// Link publishes the completed file without replacing a concurrent writer.
	if err := os.Link(file.Name(), destination); err != nil {
		return response{}, fail(stderr, "output", err.Error())
	}
	data, _ := json.Marshal(map[string]any{"id": expected.ID, "path": destination, "size": expected.Size})
	ok := true
	return response{Success: &ok, Data: data}, 0
}
