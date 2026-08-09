// Package attestation creates and verifies project-owned DSSE envelopes.
package attestation

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/CYPT71/secure-oci-base/internal/signing"
)

const EnvelopeMediaType = "application/vnd.dsse.envelope.v1+json"

type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// Sign wraps canonical predicate bytes in a DSSE envelope.
func Sign(store signing.KeyStore, keyName, keyID, payloadType string, predicate any) (Envelope, error) {
	if store == nil || keyName == "" || keyID == "" || payloadType == "" {
		return Envelope{}, errors.New("attestation: key store, key name, key id and payload type are required")
	}
	payload, err := json.Marshal(predicate)
	if err != nil {
		return Envelope{}, fmt.Errorf("attestation: encode predicate: %w", err)
	}
	signature, err := store.Sign(keyName, pae(payloadType, payload))
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(signature)}},
	}, nil
}

// Verify authenticates one envelope and returns its predicate bytes.
func Verify(envelope Envelope, keys map[string]ed25519.PublicKey) ([]byte, error) {
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, errors.New("attestation: invalid payload encoding")
	}
	if envelope.PayloadType == "" || len(envelope.Signatures) == 0 {
		return nil, errors.New("attestation: payload type and signature are required")
	}
	message := pae(envelope.PayloadType, payload)
	for _, candidate := range envelope.Signatures {
		key, ok := keys[candidate.KeyID]
		if !ok {
			continue
		}
		signature, err := base64.StdEncoding.DecodeString(candidate.Sig)
		if err == nil && signing.Verify(key, message, signature) == nil {
			return payload, nil
		}
	}
	return nil, errors.New("attestation: no trusted signature verified")
}

// pae is DSSE v1 pre-authentication encoding.
func pae(payloadType string, payload []byte) []byte {
	result := []byte("DSSEv1 ")
	result = strconv.AppendInt(result, int64(len(payloadType)), 10)
	result = append(result, ' ')
	result = append(result, payloadType...)
	result = append(result, ' ')
	result = strconv.AppendInt(result, int64(len(payload)), 10)
	result = append(result, ' ')
	return append(result, payload...)
}
