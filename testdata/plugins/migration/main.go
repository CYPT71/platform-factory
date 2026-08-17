package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"

	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

var state struct {
	sync.Mutex
	resource *plugin.MigrationResource
	applies  int
	artifact plugin.MigrationArtifact
}

var (
	configuredName          = "migration-fixture"
	configuredBidirectional = "0"
	configuredInitial       = "0"
)

func discover(ctx context.Context, p plugin.MigrationDiscoverParams) (plugin.MigrationDiscoverResult, error) {
	if plugin.TraceIDFromContext(ctx) == "" {
		return plugin.MigrationDiscoverResult{}, fmt.Errorf("trace ID required")
	}
	pluginName := configuredName
	if configuredBidirectional == "1" {
		state.Lock()
		defer state.Unlock()
		if state.resource == nil {
			return plugin.MigrationDiscoverResult{Status: "complete"}, nil
		}
		r := *state.resource
		r.Origin.Source = pluginName
		r.Origin.NativeType = "native-compute"
		r.Origin.NativeID = pluginName + "/" + r.ID
		return plugin.MigrationDiscoverResult{Status: "complete", Resources: []plugin.MigrationResource{r}}, nil
	}
	r := plugin.MigrationResource{ID: "vm-1", Kind: "compute", Origin: plugin.MigrationResourceOrigin{Source: pluginName, NativeType: "vm", NativeID: "one"}, Attributes: map[string]string{"cpu": "2"}, Requirements: []plugin.MigrationRequirement{{Capability: plugin.CapabilityMigrationApply, Version: "v1"}}}
	if p.Cursor == "" {
		return plugin.MigrationDiscoverResult{Status: "complete", Resources: []plugin.MigrationResource{r}, NextCursor: "page-2"}, nil
	}
	if p.Cursor == "page-2" {
		return plugin.MigrationDiscoverResult{Status: "partial", Unknowns: []plugin.MigrationUnknownObservation{{Source: "migration-fixture", Kind: "permission-denied", Scope: "private-network", Reason: "read denied"}}}, nil
	}
	return plugin.MigrationDiscoverResult{}, fmt.Errorf("invalid cursor")
}
func observe(context.Context, plugin.MigrationObserveParams) (plugin.MigrationObserveResult, error) {
	state.Lock()
	defer state.Unlock()
	if state.resource == nil {
		return plugin.MigrationObserveResult{}, nil
	}
	r := *state.resource
	return plugin.MigrationObserveResult{Found: true, Resource: &r}, nil
}
func inspect(context.Context, plugin.MigrationInspectParams) (plugin.MigrationInspectResult, error) {
	state.Lock()
	defer state.Unlock()
	if state.resource == nil {
		return plugin.MigrationInspectResult{}, nil
	}
	r := *state.resource
	r.Origin.Source = configuredName
	r.Origin.NativeType = "native-compute"
	r.Origin.NativeID = configuredName + "/" + r.ID
	return plugin.MigrationInspectResult{Found: true, Resource: &r}, nil
}
func apply(ctx context.Context, p plugin.MigrationApplyParams) (plugin.MigrationApplyResult, error) {
	if plugin.OperationIDFromContext(ctx) == "" {
		return plugin.MigrationApplyResult{}, fmt.Errorf("operation ID required")
	}
	state.Lock()
	defer state.Unlock()
	r := p.Resource
	state.resource = &r
	state.applies++
	return plugin.MigrationApplyResult{Accepted: true}, nil
}
func exportArtifact(context.Context, plugin.MigrationExportParams) (plugin.MigrationExportResult, error) {
	data, err := portableOCI()
	if err != nil {
		return plugin.MigrationExportResult{}, err
	}
	sum := sha256.Sum256(data)
	return plugin.MigrationExportResult{Artifact: plugin.MigrationArtifact{Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(data)), Format: "oci-layout.tar.gz", Data: data}}, nil
}
func portableOCI() ([]byte, error) {
	var rawLayer bytes.Buffer
	lt := tar.NewWriter(&rawLayer)
	payload := []byte("application")
	if err := lt.WriteHeader(&tar.Header{Name: "app", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := lt.Write(payload); err != nil {
		return nil, err
	}
	if err := lt.Close(); err != nil {
		return nil, err
	}
	diff := sha256.Sum256(rawLayer.Bytes())
	var layer bytes.Buffer
	gz := gzip.NewWriter(&layer)
	if _, err := gz.Write(rawLayer.Bytes()); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	config, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hex.EncodeToString(diff[:])}}})
	type desc struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Size        int64             `json:"size"`
		Platform    any               `json:"platform,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}
	mk := func(media string, data []byte) desc {
		s := sha256.Sum256(data)
		return desc{MediaType: media, Digest: "sha256:" + hex.EncodeToString(s[:]), Size: int64(len(data))}
	}
	cd, ld := mk("application/vnd.oci.image.config.v1+json", config), mk("application/vnd.oci.image.layer.v1.tar+gzip", layer.Bytes())
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": cd, "layers": []desc{ld}})
	md := mk("application/vnd.oci.image.manifest.v1+json", manifest)
	md.Platform = map[string]string{"os": "linux", "architecture": "amd64"}
	md.Annotations = map[string]string{"org.opencontainers.image.ref.name": "fixture:latest"}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []desc{md}})
	files := map[string][]byte{"oci-layout": []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), "index.json": index, "blobs/sha256/" + cd.Digest[7:]: config, "blobs/sha256/" + ld.Digest[7:]: layer.Bytes(), "blobs/sha256/" + md.Digest[7:]: manifest}
	var out bytes.Buffer
	og := gzip.NewWriter(&out)
	ot := tar.NewWriter(og)
	for _, dir := range []string{"blobs", "blobs/sha256"} {
		if err := ot.WriteHeader(&tar.Header{Name: dir, Mode: 0o700, Typeflag: tar.TypeDir}); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		if err := ot.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := ot.Write(data); err != nil {
			return nil, err
		}
	}
	if err := ot.Close(); err != nil {
		return nil, err
	}
	if err := og.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func importArtifact(ctx context.Context, p plugin.MigrationImportParams) (plugin.MigrationImportResult, error) {
	if plugin.OperationIDFromContext(ctx) == "" || len(p.Artifact.Data) == 0 {
		return plugin.MigrationImportResult{}, fmt.Errorf("verified artifact and operation ID required")
	}
	state.Lock()
	r := p.Resource
	state.resource = &r
	state.artifact = plugin.MigrationArtifact{Digest: p.Artifact.Digest, Size: p.Artifact.Size, Format: p.Artifact.Format}
	state.Unlock()
	return plugin.MigrationImportResult{Accepted: true}, nil
}
func observeArtifact(context.Context, plugin.MigrationArtifactObserveParams) (plugin.MigrationArtifactObserveResult, error) {
	state.Lock()
	defer state.Unlock()
	if state.resource == nil || state.artifact.Digest == "" {
		return plugin.MigrationArtifactObserveResult{}, nil
	}
	r := *state.resource
	return plugin.MigrationArtifactObserveResult{Found: true, Resource: &r, NativeBinding: "installed/" + r.ID, Attestation: []byte(state.artifact.Digest)}, nil
}
func main() {
	pluginName := configuredName
	if configuredInitial == "1" {
		r := plugin.MigrationResource{ID: "vm-1", Kind: "compute", Origin: plugin.MigrationResourceOrigin{Source: pluginName, NativeType: "native-compute", NativeID: pluginName + "/vm-1"}, Attributes: map[string]string{"cpu": "2"}, Requirements: []plugin.MigrationRequirement{{Capability: plugin.CapabilityMigrationApply, Version: "v1"}}}
		state.resource = &r
	}
	s := plugin.NewServer(pluginName, "v1")
	plugin.RegisterMigration(s, discover, observe, apply)
	plugin.RegisterMigrationInspect(s, inspect)
	plugin.RegisterMigrationArtifacts(s, exportArtifact, importArtifact, observeArtifact)
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
