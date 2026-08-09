// Package registry implements the project-owned OCI Distribution client.
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	indexMediaType    = "application/vnd.oci.image.index.v1+json"
	artifactMediaType = "application/vnd.oci.artifact.manifest.v1+json"
	maxResponseBody   = 1 << 20
	uploadChunkSize   = 4 << 20
	// maxFetchedBlobSize bounds GetBlob: it exists for fetching small,
	// verify-before-trust artifacts (signatures, SBOM, provenance), not
	// for pulling multi-gigabyte image layers - see GetBlob's doc comment.
	maxFetchedBlobSize = 64 << 20

	manifestAcceptTypes = "application/vnd.oci.image.index.v1+json," +
		"application/vnd.oci.image.manifest.v1+json," +
		"application/vnd.oci.artifact.manifest.v1+json," +
		"application/vnd.docker.distribution.manifest.v2+json," +
		"application/vnd.docker.distribution.manifest.list.v2+json"
)

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type imageIndex struct {
	MediaType string       `json:"mediaType"`
	Manifests []descriptor `json:"manifests"`
}

// Client publishes verified OCI layouts using only net/http.
type Client struct {
	HTTP     *http.Client
	Scheme   string
	Username string
	Password string
	// SessionDir persists opaque registry upload locations so a new process can
	// reconcile and resume an interrupted blob upload. Empty disables it.
	SessionDir string
	// MountFrom requests a cross-repository blob mount before uploading.
	// Registries that decline it return an upload Location and transparently
	// fall back to the normal streaming upload.
	MountFrom string

	mu    sync.RWMutex
	token string
}

// Result identifies the immutable object installed before the requested tag.
type Result struct {
	Digest    string `json:"digest"`
	Reference string `json:"reference"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Blobs     int    `json:"blobs"`
}

// ArtifactResult identifies an OCI 1.1 artifact manifest linked to a subject.
type ArtifactResult struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// GetManifest fetches the manifest at reference (a tag or a digest) in
// target and returns its raw bytes plus the Content-Type the registry
// served it as. If reference is itself a digest, the returned bytes are
// re-hashed and checked against it before returning - a registry's
// Docker-Content-Digest response header is informational, never trusted
// on its own the way this client trusts a digest it computes itself.
func (c *Client) GetManifest(ctx context.Context, target Reference, reference string) ([]byte, string, error) {
	response, err := c.doAccept(ctx, http.MethodGet, target, "/manifests/"+url.PathEscape(reference),
		nil, "", manifestAcceptTypes, -1)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", responseError("get manifest", response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return nil, "", fmt.Errorf("registry: read manifest: %w", err)
	}
	if wantHex, err := digestHex(reference); err == nil {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != wantHex {
			return nil, "", fmt.Errorf("registry: manifest %s digest mismatch", reference)
		}
	}
	return body, response.Header.Get("Content-Type"), nil
}

// GetBlob fetches and returns the blob at digest in target, verifying its
// SHA-256 digest before returning - never the server's status code or
// Content-Length alone. Bounded to maxFetchedBlobSize: this is for
// fetching small, already-digest-addressed artifacts a caller is about to
// verify (a signature envelope, an SBOM, a provenance predicate - the
// shapes secure-oci verify-release and publish's own subject artifacts
// use), not a general-purpose layer puller.
func (c *Client) GetBlob(ctx context.Context, target Reference, digest string) ([]byte, error) {
	wantHex, err := digestHex(digest)
	if err != nil {
		return nil, err
	}
	response, err := c.do(ctx, http.MethodGet, target, "/blobs/"+url.PathEscape(digest), nil, "", -1)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError("get blob", response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFetchedBlobSize+1))
	if err != nil {
		return nil, fmt.Errorf("registry: read blob %s: %w", digest, err)
	}
	if int64(len(body)) > maxFetchedBlobSize {
		return nil, fmt.Errorf("registry: blob %s exceeds the %d byte fetch limit", digest, maxFetchedBlobSize)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != wantHex {
		return nil, fmt.Errorf("registry: blob %s digest mismatch", digest)
	}
	return body, nil
}

// PushArtifact uploads payload and publishes an OCI artifact manifest whose
// subject is the immutable image digest. No tag is required: the registry's
// referrers API can discover it from the subject relationship.
func (c *Client) PushArtifact(ctx context.Context, target Reference, subjectDigest, subjectMediaType string, subjectSize int64, artifactType, payloadMediaType string, payload []byte) (ArtifactResult, error) {
	if _, err := digestHex(subjectDigest); err != nil {
		return ArtifactResult{}, err
	}
	if artifactType == "" || payloadMediaType == "" {
		return ArtifactResult{}, errors.New("registry: artifact and payload media types are required")
	}
	sum := sha256.Sum256(payload)
	payloadDigest := "sha256:" + hex.EncodeToString(sum[:])
	exists, err := c.blobExists(ctx, target, payloadDigest)
	if err != nil {
		return ArtifactResult{}, err
	}
	if !exists {
		if err := c.uploadBlob(ctx, target, payloadDigest, int64(len(payload)), bytes.NewReader(payload)); err != nil {
			return ArtifactResult{}, err
		}
	}
	manifest := struct {
		MediaType    string       `json:"mediaType"`
		ArtifactType string       `json:"artifactType"`
		Blobs        []descriptor `json:"blobs"`
		Subject      descriptor   `json:"subject"`
	}{
		MediaType: artifactMediaType, ArtifactType: artifactType,
		Blobs:   []descriptor{{MediaType: payloadMediaType, Digest: payloadDigest, Size: int64(len(payload))}},
		Subject: descriptor{MediaType: subjectMediaType, Digest: subjectDigest, Size: subjectSize},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return ArtifactResult{}, err
	}
	manifestSum := sha256.Sum256(encoded)
	digest := "sha256:" + hex.EncodeToString(manifestSum[:])
	if err := c.putManifest(ctx, target, digest, artifactMediaType, encoded); err != nil {
		return ArtifactResult{}, fmt.Errorf("registry: publish artifact: %w", err)
	}
	return ArtifactResult{Digest: digest, Size: int64(len(encoded))}, nil
}

// PushLayout uploads all layout blobs, installs the selected manifest by
// digest, and only then moves the target tag. sourceReference selects an
// org.opencontainers.image.ref.name annotation when the layout has several
// top-level entries.
func (c *Client) PushLayout(ctx context.Context, layoutDir string, target Reference, sourceReference string) (Result, error) {
	return c.pushLayout(ctx, layoutDir, target, sourceReference, true)
}

// PushLayoutByDigest uploads and installs the manifest by digest but leaves
// the mutable tag untouched. Call TagLayout only after policy and linked
// artifacts succeed.
func (c *Client) PushLayoutByDigest(ctx context.Context, layoutDir string, target Reference, sourceReference string) (Result, error) {
	return c.pushLayout(ctx, layoutDir, target, sourceReference, false)
}

func (c *Client) pushLayout(ctx context.Context, layoutDir string, target Reference, sourceReference string, moveTag bool) (Result, error) {
	indexBytes, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		return Result{}, fmt.Errorf("registry: read layout index: %w", err)
	}
	var index imageIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return Result{}, fmt.Errorf("registry: decode layout index: %w", err)
	}
	selected, err := selectManifest(index.Manifests, sourceReference)
	if err != nil {
		return Result{}, err
	}
	manifestBytes, err := readVerifiedBlob(layoutDir, selected)
	if err != nil {
		return Result{}, err
	}

	blobDir := filepath.Join(layoutDir, "blobs", "sha256")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return Result{}, fmt.Errorf("registry: read blobs: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	uploaded := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || len(entry.Name()) != 64 {
			continue
		}
		digest := "sha256:" + entry.Name()
		exists, err := c.blobExists(ctx, target, digest)
		if err != nil {
			return Result{}, err
		}
		if exists {
			continue
		}
		file, err := os.Open(filepath.Join(blobDir, entry.Name()))
		if err != nil {
			return Result{}, fmt.Errorf("registry: open blob %s: %w", digest, err)
		}
		info, statErr := file.Stat()
		if statErr == nil {
			err = c.uploadBlob(ctx, target, digest, info.Size(), file)
		}
		_ = file.Close()
		if statErr != nil {
			return Result{}, fmt.Errorf("registry: stat blob %s: %w", digest, statErr)
		}
		if err != nil {
			return Result{}, err
		}
		uploaded++
	}

	if err := c.putManifest(ctx, target, selected.Digest, selected.MediaType, manifestBytes); err != nil {
		return Result{}, fmt.Errorf("registry: install manifest by digest: %w", err)
	}
	if moveTag {
		if err := c.putManifest(ctx, target, target.Tag, selected.MediaType, manifestBytes); err != nil {
			return Result{}, fmt.Errorf("registry: move tag: %w", err)
		}
	}
	return Result{
		Digest: selected.Digest, Reference: target.Registry + "/" + target.Repository + "@" + selected.Digest,
		MediaType: selected.MediaType, Size: selected.Size, Blobs: uploaded,
	}, nil
}

// TagLayout moves target.Tag to the already-installed verified manifest.
func (c *Client) TagLayout(ctx context.Context, layoutDir string, target Reference, sourceReference string) error {
	indexBytes, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		return fmt.Errorf("registry: read layout index: %w", err)
	}
	var index imageIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return fmt.Errorf("registry: decode layout index: %w", err)
	}
	selected, err := selectManifest(index.Manifests, sourceReference)
	if err != nil {
		return err
	}
	manifest, err := readVerifiedBlob(layoutDir, selected)
	if err != nil {
		return err
	}
	return c.putManifest(ctx, target, target.Tag, selected.MediaType, manifest)
}

func selectManifest(manifests []descriptor, source string) (descriptor, error) {
	if len(manifests) == 0 {
		return descriptor{}, errors.New("registry: layout index has no manifests")
	}
	if source == "" {
		if len(manifests) != 1 {
			return descriptor{}, errors.New("registry: layout contains multiple references; select one with --source-ref")
		}
		return manifests[0], nil
	}
	for _, candidate := range manifests {
		if candidate.Annotations["org.opencontainers.image.ref.name"] == source {
			return candidate, nil
		}
	}
	return descriptor{}, fmt.Errorf("registry: source reference %q is not present in the layout", source)
}

func readVerifiedBlob(layoutDir string, desc descriptor) ([]byte, error) {
	hexDigest, err := digestHex(desc.Digest)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(layoutDir, "blobs", "sha256", hexDigest))
	if err != nil {
		return nil, fmt.Errorf("registry: read manifest %s: %w", desc.Digest, err)
	}
	if int64(len(data)) != desc.Size {
		return nil, fmt.Errorf("registry: manifest %s size mismatch", desc.Digest)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hexDigest {
		return nil, fmt.Errorf("registry: manifest %s digest mismatch", desc.Digest)
	}
	return data, nil
}

func (c *Client) blobExists(ctx context.Context, target Reference, digest string) (bool, error) {
	response, err := c.do(ctx, http.MethodHead, target, "/blobs/"+url.PathEscape(digest), nil, "", -1)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, responseError("check blob", response)
	}
	return true, nil
}

func (c *Client) uploadBlob(ctx context.Context, target Reference, digest string, size int64, content io.Reader) error {
	uploadURL, offset, resumed, err := c.resumeUpload(ctx, target, digest, size)
	if err != nil {
		return err
	}
	suffix := "/blobs/uploads/"
	if c.MountFrom != "" {
		if strings.HasPrefix(c.MountFrom, "/") || strings.HasSuffix(c.MountFrom, "/") ||
			strings.Contains(c.MountFrom, "..") || strings.ContainsAny(c.MountFrom, " \t\r\n\x00") {
			return fmt.Errorf("registry: invalid cross-repository mount source %q", c.MountFrom)
		}
		query := url.Values{"mount": []string{digest}, "from": []string{c.MountFrom}}
		suffix += "?" + query.Encode()
	}
	if !resumed {
		response, err := c.do(ctx, http.MethodPost, target, suffix, nil, "", 0)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode == http.StatusCreated {
			return nil
		}
		if response.StatusCode != http.StatusAccepted {
			return responseError("start blob upload", response)
		}
		location := response.Header.Get("Location")
		if location == "" {
			return errors.New("registry: start blob upload returned no Location")
		}
		uploadURL, err = c.resolveLocation(target, location)
		if err != nil {
			return err
		}
		if err := c.saveUpload(target, digest, size, uploadURL); err != nil {
			return err
		}
	}
	if offset > 0 {
		if offset > size {
			return fmt.Errorf("registry: resumed upload offset %d exceeds blob size %d", offset, size)
		}
		if skipped, err := io.CopyN(io.Discard, content, offset); err != nil || skipped != offset {
			return fmt.Errorf("registry: seek resumed blob to %d: %w", offset, err)
		}
	}
	buffer := make([]byte, uploadChunkSize)
	for {
		n, readErr := io.ReadFull(content, buffer)
		if n > 0 {
			sent := 0
			for attempts := 0; sent < n; attempts++ {
				if attempts >= 4 {
					return fmt.Errorf("registry: upload blob %s did not make progress after retries", digest)
				}
				chunkStart := offset + int64(sent)
				patch, patchErr := c.doURL(ctx, http.MethodPatch, uploadURL,
					bytes.NewReader(buffer[sent:n]), "application/octet-stream", "", int64(n-sent))
				if patchErr == nil && patch.StatusCode == http.StatusAccepted {
					if next := patch.Header.Get("Location"); next != "" {
						uploadURL, err = c.resolveLocation(target, next)
					}
					_ = patch.Body.Close()
					if err != nil {
						return err
					}
					if err := c.saveUpload(target, digest, size, uploadURL); err != nil {
						return err
					}
					sent = n
					continue
				}
				if patch != nil {
					_ = patch.Body.Close()
				}
				committed, reconcileErr := c.uploadOffset(ctx, uploadURL)
				if reconcileErr != nil {
					if patchErr != nil {
						return patchErr
					}
					return reconcileErr
				}
				if committed < chunkStart || committed > offset+int64(n) {
					return fmt.Errorf("registry: upload offset %d is outside current chunk [%d,%d]", committed, chunkStart, offset+int64(n))
				}
				sent = int(committed - offset)
			}
			offset += int64(n)
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("registry: read blob %s: %w", digest, readErr)
		}
	}
	query := uploadURL.Query()
	query.Set("digest", digest)
	uploadURL.RawQuery = query.Encode()
	finish, err := c.doURL(ctx, http.MethodPut, uploadURL, nil, "application/octet-stream", "", 0)
	if err != nil {
		return err
	}
	defer finish.Body.Close()
	if finish.StatusCode != http.StatusCreated {
		return responseError("finish blob upload", finish)
	}
	if err := c.removeUpload(target, digest); err != nil {
		return err
	}
	return nil
}

type uploadSession struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

func (c *Client) uploadSessionPath(target Reference, digest string) string {
	if c.SessionDir == "" {
		return ""
	}
	key := sha256.Sum256([]byte(target.Registry + "\x00" + target.Repository + "\x00" + digest))
	return filepath.Join(c.SessionDir, hex.EncodeToString(key[:])+".json")
}

func (c *Client) saveUpload(target Reference, digest string, size int64, uploadURL *url.URL) error {
	path := c.uploadSessionPath(target, digest)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(c.SessionDir, 0o700); err != nil {
		return fmt.Errorf("registry: create upload session directory: %w", err)
	}
	data, err := json.Marshal(uploadSession{Digest: digest, Size: size, URL: uploadURL.String()})
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("registry: persist upload session: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("registry: install upload session: %w", err)
	}
	return nil
}

func (c *Client) resumeUpload(ctx context.Context, target Reference, digest string, size int64) (*url.URL, int64, bool, error) {
	path := c.uploadSessionPath(target, digest)
	if path == "" {
		return nil, 0, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("registry: read upload session: %w", err)
	}
	var session uploadSession
	if json.Unmarshal(data, &session) != nil || session.Digest != digest || session.Size != size {
		_ = os.Remove(path)
		return nil, 0, false, nil
	}
	uploadURL, err := c.resolveLocation(target, session.URL)
	if err != nil {
		return nil, 0, false, err
	}
	offset, err := c.uploadOffset(ctx, uploadURL)
	if err != nil {
		_ = os.Remove(path)
		return nil, 0, false, nil
	}
	return uploadURL, offset, true, nil
}

func (c *Client) removeUpload(target Reference, digest string) error {
	path := c.uploadSessionPath(target, digest)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("registry: remove upload session: %w", err)
	}
	return nil
}

func (c *Client) uploadOffset(ctx context.Context, uploadURL *url.URL) (int64, error) {
	response, err := c.doURL(ctx, http.MethodGet, uploadURL, nil, "", "", 0)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusAccepted {
		return 0, responseError("query upload offset", response)
	}
	value := strings.TrimSpace(response.Header.Get("Range"))
	if value == "" {
		return 0, nil
	}
	value = strings.TrimPrefix(value, "bytes=")
	_, end, ok := strings.Cut(value, "-")
	if !ok {
		return 0, fmt.Errorf("registry: invalid upload Range %q", value)
	}
	last, err := strconv.ParseInt(end, 10, 64)
	if err != nil || last < 0 {
		return 0, fmt.Errorf("registry: invalid upload Range %q", value)
	}
	return last + 1, nil
}

func (c *Client) putManifest(ctx context.Context, target Reference, reference, mediaType string, data []byte) error {
	if mediaType == "" {
		mediaType = indexMediaType
	}
	response, err := c.do(ctx, http.MethodPut, target, "/manifests/"+url.PathEscape(reference),
		bytes.NewReader(data), mediaType, int64(len(data)))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusAccepted {
		return responseError("put manifest", response)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method string, target Reference, suffix string, body io.Reader, contentType string, size int64) (*http.Response, error) {
	return c.doAccept(ctx, method, target, suffix, body, contentType, "", size)
}

// doAccept is do plus an explicit Accept header, needed for content
// negotiation on manifest GETs (an OCI registry may otherwise respond
// with whichever media type it prefers, not the one the caller asked
// for).
func (c *Client) doAccept(ctx context.Context, method string, target Reference, suffix string, body io.Reader, contentType, accept string, size int64) (*http.Response, error) {
	scheme := c.Scheme
	if scheme == "" {
		scheme = "https"
	}
	pathSuffix, rawQuery, _ := strings.Cut(suffix, "?")
	endpoint := &url.URL{Scheme: scheme, Host: target.Registry, Path: "/v2/" + target.Repository + pathSuffix, RawQuery: rawQuery}
	return c.doURL(ctx, method, endpoint, body, contentType, accept, size)
}

func (c *Client) doURL(ctx context.Context, method string, endpoint *url.URL, body io.Reader, contentType, accept string, size int64) (*http.Response, error) {
	request, err := c.newRequest(ctx, method, endpoint, body, contentType, accept, size)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("registry: %s %s: %w", method, endpoint.Redacted(), err)
	}
	if response.StatusCode != http.StatusUnauthorized || request.Header.Get("Authorization") != "" && c.currentToken() != "" {
		return response, nil
	}
	challenge := response.Header.Get("WWW-Authenticate")
	_ = response.Body.Close()
	if err := c.authorize(ctx, challenge); err != nil {
		return nil, err
	}
	var replay io.Reader
	if body != nil {
		if request.GetBody == nil {
			return nil, errors.New("registry: authentication challenge arrived after a non-replayable upload started")
		}
		replayBody, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		defer replayBody.Close()
		replay = replayBody
	}
	retry, err := c.newRequest(ctx, method, endpoint, replay, contentType, accept, size)
	if err != nil {
		return nil, err
	}
	response, err = c.httpClient().Do(retry)
	if err != nil {
		return nil, fmt.Errorf("registry: authenticated %s %s: %w", method, endpoint.Redacted(), err)
	}
	return response, nil
}

func (c *Client) newRequest(ctx context.Context, method string, endpoint *url.URL, body io.Reader, contentType, accept string, size int64) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if size >= 0 {
		request.ContentLength = size
	}
	if token := c.currentToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	} else if c.Username != "" {
		request.SetBasicAuth(c.Username, c.Password)
	}
	return request, nil
}

func (c *Client) authorize(ctx context.Context, challenge string) error {
	scheme, parameters, found := strings.Cut(strings.TrimSpace(challenge), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return fmt.Errorf("registry: unsupported authentication challenge %q", challenge)
	}
	values, err := parseAuthParameters(parameters)
	if err != nil {
		return fmt.Errorf("registry: invalid Bearer challenge: %w", err)
	}
	realm, err := url.Parse(values["realm"])
	if err != nil || realm.Scheme == "" || realm.Host == "" {
		return errors.New("registry: Bearer challenge has an invalid realm")
	}
	if realm.Scheme != "https" && c.Scheme != "http" {
		return errors.New("registry: refusing credentials over an insecure token realm")
	}
	query := realm.Query()
	for _, key := range []string{"service", "scope"} {
		if values[key] != "" {
			query.Set(key, values[key])
		}
	}
	realm.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	if c.Username != "" {
		request.SetBasicAuth(c.Username, c.Password)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("registry: acquire bearer token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError("acquire bearer token", response)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(&payload); err != nil {
		return fmt.Errorf("registry: decode bearer token: %w", err)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return errors.New("registry: token service returned no token")
	}
	c.mu.Lock()
	c.token = payload.Token
	c.mu.Unlock()
	return nil
}

func parseAuthParameters(input string) (map[string]string, error) {
	values := map[string]string{}
	for len(strings.TrimSpace(input)) > 0 {
		input = strings.TrimSpace(input)
		keyEnd := strings.IndexByte(input, '=')
		if keyEnd <= 0 {
			return nil, errors.New("parameter has no name or value")
		}
		key := strings.ToLower(strings.TrimSpace(input[:keyEnd]))
		input = strings.TrimSpace(input[keyEnd+1:])
		var value string
		if strings.HasPrefix(input, `"`) {
			input = input[1:]
			end := strings.IndexByte(input, '"')
			if end < 0 {
				return nil, errors.New("unterminated quoted value")
			}
			value, input = input[:end], input[end+1:]
		} else {
			end := strings.IndexByte(input, ',')
			if end < 0 {
				value, input = strings.TrimSpace(input), ""
			} else {
				value, input = strings.TrimSpace(input[:end]), input[end:]
			}
		}
		if key == "" || value == "" {
			return nil, errors.New("empty parameter")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate parameter %q", key)
		}
		values[key] = value
		input = strings.TrimSpace(input)
		if input == "" {
			break
		}
		if input[0] != ',' {
			return nil, errors.New("parameters must be comma-separated")
		}
		input = input[1:]
	}
	return values, nil
}

func (c *Client) currentToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) resolveLocation(target Reference, location string) (*url.URL, error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("registry: invalid upload Location: %w", err)
	}
	if parsed.IsAbs() {
		if parsed.Host != target.Registry {
			return nil, errors.New("registry: upload Location changed registry host")
		}
		return parsed, nil
	}
	scheme := c.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: target.Registry}).ResolveReference(parsed), nil
}

func responseError(operation string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	return fmt.Errorf("registry: %s: HTTP %d: %s", operation, response.StatusCode, strings.TrimSpace(string(body)))
}

func digestHex(digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return "", fmt.Errorf("registry: invalid digest %q", digest)
	}
	value := strings.TrimPrefix(digest, "sha256:")
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("registry: invalid digest %q", digest)
	}
	return value, nil
}
