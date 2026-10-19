package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// BootstrapArtifact describes an archive authenticated by the release manifest.
type BootstrapArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// BootstrapArtifacts returns verified release archives without executing them.
func BootstrapArtifacts(ctx context.Context) (string, map[string]BootstrapArtifact, error) {
	u := &Updater{repo: "cfe84/james"}
	rel, err := u.checkLatest(ctx)
	if err != nil {
		return "", nil, err
	}
	return bootstrapArtifacts(ctx, rel, releaseManifestPublicKeyBase64)
}

func bootstrapArtifacts(ctx context.Context, rel *gitHubRelease, publicKey string) (string, map[string]BootstrapArtifact, error) {
	manifestAsset, err := findReleaseAsset(rel, "james-manifest.json")
	if err != nil {
		return "", nil, err
	}
	signatureAsset, err := findReleaseAsset(rel, "james-manifest.json.sig")
	if err != nil {
		return "", nil, err
	}
	body, err := downloadAsset(ctx, manifestAsset)
	if err != nil {
		return "", nil, err
	}
	signature, err := downloadAsset(ctx, signatureAsset)
	if err != nil {
		return "", nil, err
	}
	manifest, err := verifyReleaseManifestWithKey(body, signature, rel.TagName, publicKey)
	if err != nil {
		return "", nil, err
	}
	artifacts := make(map[string]BootstrapArtifact)
	for _, name := range []string{"james-darwin-arm64.tar.gz", "james-windows-amd64.zip"} {
		asset, err := findReleaseAsset(rel, name)
		if err != nil {
			return "", nil, err
		}
		digest := strings.ToLower(strings.TrimSpace(manifest.Artifacts[name]))
		if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != sha256.Size {
			return "", nil, fmt.Errorf("invalid release digest for %s", name)
		}
		if !strings.HasPrefix(asset.BrowserDownloadURL, "https://github.com/cfe84/james/releases/download/") {
			return "", nil, fmt.Errorf("unexpected release archive URL")
		}
		artifacts[name] = BootstrapArtifact{URL: asset.BrowserDownloadURL, SHA256: digest}
	}
	return manifest.Version, artifacts, nil
}
