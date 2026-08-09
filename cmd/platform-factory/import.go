package main

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/layout"
)

func localLayoutPath(value string) bool {
	info, err := os.Stat(value)
	return err == nil && info.IsDir()
}

func prepareContainerImage(runtimeName, image, layoutName string, stderr io.Writer, execute containerExecutor) (string, error) {
	if layoutName == "" {
		layoutName = image
		image = ""
	}
	report, err := layout.Verify(layoutName)
	if err != nil {
		return "", fmt.Errorf("verify local layout: %w", err)
	}
	references := map[string]bool{}
	for _, platform := range report.Platforms {
		if platform.Reference != "" {
			references[platform.Reference] = true
		}
	}
	if image == "" {
		if len(references) != 1 {
			return "", errors.New("layout contains multiple image references; pass --layout PATH IMAGE")
		}
		for reference := range references {
			image = reference
		}
	} else if !references[image] {
		return "", fmt.Errorf("layout does not contain image reference %q", image)
	}

	var inspectArgs []string
	if runtimeName == "podman" {
		inspectArgs = []string{"image", "exists", image}
	} else {
		inspectArgs = []string{"image", "inspect", image}
	}
	if err := execute(runtimeName, inspectArgs, nil, io.Discard, io.Discard); err == nil {
		return image, nil
	}
	if err := streamLayoutToRuntime(runtimeName, layoutName, image, stderr, execute); err != nil {
		return "", fmt.Errorf("import %s into %s: %w", layoutName, runtimeName, err)
	}
	if err := execute(runtimeName, inspectArgs, nil, io.Discard, io.Discard); err != nil {
		return "", fmt.Errorf("image %q is still unavailable after import", image)
	}
	return image, nil
}

func streamLayoutToRuntime(runtimeName, root, reference string, stderr io.Writer, execute containerExecutor) error {
	reader, writer := io.Pipe()
	archiveResult := make(chan error, 1)
	go func() {
		// Podman's loader reads an OCI Image Layout archive directly.
		// Docker's loader requires the Docker Save format (a top-level
		// manifest.json referencing the config and layers), so the layout
		// is transposed into that format for docker.
		if runtimeName == "docker" {
			archiveResult <- writeDockerArchive(writer, root, reference)
			return
		}
		archiveResult <- writeLayoutArchive(writer, root)
	}()
	runtimeErr := execute(runtimeName, []string{"load"}, reader, io.Discard, stderr)
	_ = reader.CloseWithError(runtimeErr)
	archiveErr := <-archiveResult
	if runtimeErr != nil {
		return runtimeErr
	}
	return archiveErr
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociIndex struct {
	Manifests []ociDescriptor `json:"manifests"`
}

type ociManifest struct {
	Config ociDescriptor   `json:"config"`
	Layers []ociDescriptor `json:"layers"`
}

type dockerManifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// writeDockerArchive transposes an OCI Image Layout into a Docker Save
// archive so `docker load` accepts it: a top-level manifest.json naming
// the config blob and the ordered layer blobs, plus those blobs. Layers
// stay gzip-compressed; docker's loader decompresses them and derives
// the diff_ids that the config already records, so the content is
// byte-identical to the layout, just repackaged.
func writeDockerArchive(output *io.PipeWriter, root, reference string) error {
	tw := tar.NewWriter(output)
	success := false
	defer func() {
		if success {
			_ = output.Close()
		} else {
			_ = output.CloseWithError(errors.New("docker archive failed"))
		}
	}()

	manifestDescriptor, err := selectManifest(root, reference)
	if err != nil {
		return err
	}
	var manifest ociManifest
	if err := readLayoutJSON(root, manifestDescriptor.Digest, &manifest); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	configName := blobArchiveName(manifest.Config.Digest)
	if err := copyBlobToArchive(tw, root, manifest.Config.Digest, configName); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	layerNames := make([]string, 0, len(manifest.Layers))
	for _, layer := range manifest.Layers {
		name := blobArchiveName(layer.Digest)
		if err := copyBlobToArchive(tw, root, layer.Digest, name); err != nil {
			return fmt.Errorf("write layer %s: %w", layer.Digest, err)
		}
		layerNames = append(layerNames, name)
	}

	entry := dockerManifestEntry{Config: configName, Layers: layerNames}
	if reference != "" {
		entry.RepoTags = []string{reference}
	}
	manifestBytes, err := json.Marshal([]dockerManifestEntry{entry})
	if err != nil {
		return err
	}
	if err := writeArchiveBytes(tw, "manifest.json", manifestBytes); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

// selectManifest returns the layout's manifest descriptor whose
// reference matches, or the single manifest when reference is empty.
func selectManifest(root, reference string) (ociDescriptor, error) {
	var index ociIndex
	if err := readLayoutFile(filepath.Join(root, "index.json"), &index); err != nil {
		return ociDescriptor{}, fmt.Errorf("read index: %w", err)
	}
	if len(index.Manifests) == 0 {
		return ociDescriptor{}, errors.New("layout index has no manifests")
	}
	if reference == "" {
		if len(index.Manifests) != 1 {
			return ociDescriptor{}, errors.New("layout has multiple manifests; a reference is required")
		}
		return index.Manifests[0], nil
	}
	for _, descriptor := range index.Manifests {
		if descriptor.Annotations["org.opencontainers.image.ref.name"] == reference {
			return descriptor, nil
		}
	}
	return ociDescriptor{}, fmt.Errorf("layout has no manifest for reference %q", reference)
}

func blobArchiveName(digest string) string {
	return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
}

func blobPath(root, digest string) (string, error) {
	hex := strings.TrimPrefix(digest, "sha256:")
	if digest == hex || len(hex) != 64 || strings.ContainsAny(hex, "/\\.") {
		return "", fmt.Errorf("invalid blob digest %q", digest)
	}
	return filepath.Join(root, "blobs", "sha256", hex), nil
}

func readLayoutJSON(root, digest string, target any) error {
	path, err := blobPath(root, digest)
	if err != nil {
		return err
	}
	return readLayoutFile(path, target)
}

func readLayoutFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func copyBlobToArchive(tw *tar.Writer, root, digest, archiveName string) error {
	path, err := blobPath(root, digest)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("blob %q is not a regular file", digest)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: archiveName, Mode: 0o644, Size: info.Size(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(tw, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeArchiveBytes(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func writeLayoutArchive(output *io.PipeWriter, root string) error {
	tw := tar.NewWriter(output)
	success := false
	defer func() {
		if success {
			_ = output.Close()
		} else {
			_ = output.CloseWithError(errors.New("OCI layout archive failed"))
		}
	}()

	names := []string{"oci-layout", "index.json"}
	blobEntries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return err
	}
	for _, entry := range blobEntries {
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsafe blob entry %q", entry.Name())
		}
		names = append(names, filepath.Join("blobs", "sha256", entry.Name()))
	}
	sort.Strings(names)
	for _, name := range names {
		filename := filepath.Join(root, name)
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("layout entry %q is not a regular file", name)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = strings.ReplaceAll(name, string(filepath.Separator), "/")
		header.ModTime = info.ModTime()
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	success = true
	return nil
}
