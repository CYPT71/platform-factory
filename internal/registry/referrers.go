package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ReferrerDescriptor describes one OCI 1.1 artifact attached to a subject digest.
type ReferrerDescriptor struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// ListReferrers queries the OCI 1.1 referrers API for subjectDigest and falls
// back to the OCI 1.1 referrers tag schema (sha256-<hex>) when the registry
// returns HTTP 404 for /referrers/<digest>.
func (c *Client) ListReferrers(ctx context.Context, target Reference, subjectDigest string) ([]ReferrerDescriptor, error) {
	wantHex, err := digestHex(subjectDigest)
	if err != nil {
		return nil, err
	}
	response, err := c.doAccept(ctx, http.MethodGet, target, "/referrers/"+url.PathEscape(subjectDigest), nil, "", indexMediaType, -1)
	if err != nil {
		return nil, err
	}
	var body []byte
	switch response.StatusCode {
	case http.StatusOK:
		body, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
		_ = response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("registry: read referrers: %w", err)
		}
	case http.StatusNotFound:
		_ = response.Body.Close()
		tag := "sha256-" + wantHex
		fallbackResp, fallbackErr := c.doAccept(ctx, http.MethodGet, target, "/manifests/"+url.PathEscape(tag), nil, "", indexMediaType, -1)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		if fallbackResp.StatusCode == http.StatusNotFound {
			_ = fallbackResp.Body.Close()
			return nil, nil
		}
		if fallbackResp.StatusCode != http.StatusOK {
			defer fallbackResp.Body.Close()
			return nil, responseError("get referrers fallback", fallbackResp)
		}
		body, err = io.ReadAll(io.LimitReader(fallbackResp.Body, maxResponseBody+1))
		_ = fallbackResp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("registry: read referrers fallback: %w", err)
		}
	default:
		defer response.Body.Close()
		return nil, responseError("list referrers", response)
	}
	if len(body) > maxResponseBody {
		return nil, errors.New("registry: referrers index exceeds size limit")
	}
	var parsed struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Manifests     []ReferrerDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("registry: decode referrers index: %w", err)
	}
	if parsed.SchemaVersion != 2 {
		return nil, fmt.Errorf("registry: unexpected referrers schemaVersion %d", parsed.SchemaVersion)
	}
	for _, d := range parsed.Manifests {
		if _, err := digestHex(d.Digest); err != nil || d.Size < 0 {
			return nil, fmt.Errorf("registry: invalid referrer descriptor %q", d.Digest)
		}
	}
	return parsed.Manifests, nil
}

// updateReferrersFallback implements the OCI Distribution referrers tag schema
// when a registry does not acknowledge subject processing with OCI-Subject.
// Preserve unknown metadata, deduplicate descriptors, and never replace a
// non-index object. Publishers of one subject should serialize across clients;
// this mutable tag cannot provide distributed transactions without registry CAS.
func (c *Client) updateReferrersFallback(ctx context.Context, target Reference, subject, digest string, size int64, artifactType string) error {
	c.referrersMu.Lock()
	defer c.referrersMu.Unlock()
	tag := "sha256-" + strings.TrimPrefix(subject, "sha256:")
	response, err := c.doAccept(ctx, http.MethodGet, target, "/manifests/"+url.PathEscape(tag), nil, "", indexMediaType, -1)
	if err != nil {
		return err
	}
	index := map[string]json.RawMessage{"schemaVersion": json.RawMessage("2"), "mediaType": json.RawMessage(`"` + indexMediaType + `"`)}
	var manifests []json.RawMessage
	switch response.StatusCode {
	case http.StatusNotFound:
		response.Body.Close()
	case http.StatusOK:
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if len(body) > maxResponseBody {
			return errors.New("registry: referrers index exceeds size limit")
		}
		if err := json.Unmarshal(body, &index); err != nil {
			return fmt.Errorf("registry: decode referrers index: %w", err)
		}
		var schema int
		var mediaType string
		if json.Unmarshal(index["schemaVersion"], &schema) != nil || schema != 2 || json.Unmarshal(index["mediaType"], &mediaType) != nil || mediaType != indexMediaType {
			return errors.New("registry: refusing to replace a non-index referrers tag")
		}
		if err := json.Unmarshal(index["manifests"], &manifests); err != nil || manifests == nil {
			return errors.New("registry: invalid referrers descriptor list")
		}
	default:
		defer response.Body.Close()
		return responseError("get referrers fallback", response)
	}
	found := false
	for _, raw := range manifests {
		var existing struct {
			descriptor
			ArtifactType string `json:"artifactType"`
		}
		if err := json.Unmarshal(raw, &existing); err != nil {
			return errors.New("registry: malformed referrers descriptor")
		}
		if _, err := digestHex(existing.Digest); err != nil || existing.Size < 0 {
			return errors.New("registry: invalid referrers descriptor")
		}
		if existing.Digest == digest {
			if existing.Size != size || existing.MediaType != artifactMediaType || existing.ArtifactType != artifactType {
				return errors.New("registry: conflicting referrers descriptor")
			}
			found = true
		}
	}
	if found {
		return nil
	}
	encodedDescriptor, err := json.Marshal(struct {
		descriptor
		ArtifactType string `json:"artifactType"`
	}{descriptor{MediaType: artifactMediaType, Digest: digest, Size: size}, artifactType})
	if err != nil {
		return err
	}
	manifests = append(manifests, encodedDescriptor)
	index["manifests"], err = json.Marshal(manifests)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if len(encoded) > maxResponseBody {
		return errors.New("registry: referrers index exceeds size limit")
	}
	if err := c.putManifest(ctx, target, tag, indexMediaType, encoded); err != nil {
		return fmt.Errorf("registry: publish referrers fallback: %w", err)
	}
	installed, _, err := c.GetManifest(ctx, target, tag)
	if err != nil {
		return err
	}
	if !bytes.Equal(installed, encoded) {
		return errors.New("registry: referrers tag changed during publication; reconcile before retry")
	}
	return nil
}
