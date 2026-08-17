package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/internal/rootfs"
)

// pullImageRootfs pulls imageRef (registry.ParsePullReference: a
// digest-pinned reference, e.g. "python@sha256:..." or
// "registry.example/org/image@sha256:...") via the project's own native
// OCI registry client - the same client pf publish/pf verify-release
// already use, never the docker CLI - materializes it as a local OCI
// image layout under a temporary directory, then extracts it into destDir
// using internal/rootfs.Convert: the exact same safe, budgeted, whiteout-
// and symlink-aware extraction this repo already relies on elsewhere, not
// a second, newly-written implementation of that logic. destDir must not
// already exist (Convert's own contract). Returns the resolved
// single-platform manifest digest, so callers can record exactly what was
// pulled.
func pullImageRootfs(ctx context.Context, imageRef, architecture, destDir string) (string, error) {
	return pullImageRootfsWithClient(ctx, &registry.Client{Scheme: "https", FollowBlobRedirects: true}, imageRef, architecture, destDir)
}

// pullImageRootfsWithClient is pullImageRootfs with an injectable
// *registry.Client - the same dependency-injection seam
// internal/registry's own tests already use (Client.HTTP), letting a
// test point at a fake in-process transport instead of the real Docker
// Hub. pullImageRootfs itself is the only production caller.
func pullImageRootfsWithClient(ctx context.Context, client *registry.Client, imageRef, architecture, destDir string) (string, error) {
	target, digest, err := registry.ParsePullReference(imageRef)
	if err != nil {
		return "", err
	}
	manifestBytes, _, err := client.GetManifest(ctx, target, digest)
	if err != nil {
		return "", fmt.Errorf("pull manifest: %w", err)
	}
	selectedDigest := digest
	var top struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform,omitempty"`
		} `json:"manifests,omitempty"`
	}
	if err := json.Unmarshal(manifestBytes, &top); err != nil {
		return "", fmt.Errorf("decode manifest: %w", err)
	}
	if len(top.Manifests) > 0 {
		found := false
		for _, candidate := range top.Manifests {
			if candidate.Platform != nil && candidate.Platform.OS == "linux" && candidate.Platform.Architecture == architecture {
				selectedDigest, found = candidate.Digest, true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%s has no manifest for linux/%s", imageRef, architecture)
		}
		manifestBytes, _, err = client.GetManifest(ctx, target, selectedDigest)
		if err != nil {
			return "", fmt.Errorf("pull platform manifest: %w", err)
		}
	}
	var doc struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestBytes, &doc); err != nil || len(doc.Layers) == 0 {
		return "", fmt.Errorf("%s: invalid or empty manifest", imageRef)
	}

	layoutDir, err := os.MkdirTemp("", "platform-factory-pull-layout-*")
	if err != nil {
		return "", fmt.Errorf("create temporary layout: %w", err)
	}
	defer os.RemoveAll(layoutDir)
	if err := os.MkdirAll(filepath.Join(layoutDir, "blobs", "sha256"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(layoutDir, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		return "", err
	}
	writeBlob := func(blobDigest string, content []byte) error {
		hexDigest, ok := strings.CutPrefix(blobDigest, "sha256:")
		if !ok {
			return fmt.Errorf("unsupported digest algorithm in %q", blobDigest)
		}
		return os.WriteFile(filepath.Join(layoutDir, "blobs", "sha256", hexDigest), content, 0o444)
	}
	if err := writeBlob(selectedDigest, manifestBytes); err != nil {
		return "", err
	}
	configBytes, err := client.GetBlob(ctx, target, doc.Config.Digest)
	if err != nil {
		return "", fmt.Errorf("pull config: %w", err)
	}
	if err := writeBlob(doc.Config.Digest, configBytes); err != nil {
		return "", err
	}
	for _, layer := range doc.Layers {
		blob, err := client.GetBlob(ctx, target, layer.Digest)
		if err != nil {
			return "", fmt.Errorf("pull layer %s: %w", layer.Digest, err)
		}
		if err := writeBlob(layer.Digest, blob); err != nil {
			return "", err
		}
	}
	indexBytes, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    selectedDigest,
			"size":      len(manifestBytes),
			"platform":  map[string]string{"os": "linux", "architecture": architecture},
		}},
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(layoutDir, "index.json"), indexBytes, 0o644); err != nil {
		return "", err
	}

	if _, err := rootfs.Convert(rootfs.Options{Layout: layoutDir, Output: destDir}); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	return selectedDigest, nil
}
