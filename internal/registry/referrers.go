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
