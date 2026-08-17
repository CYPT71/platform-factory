package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// DeriveID returns a domain-separated, deterministic identity. Domain names
// are versioned by callers; NUL framing prevents ambiguous part boundaries.
func DeriveID(domain string, parts ...string) string {
	hash := sha256.New()
	hash.Write([]byte(domain))
	for _, part := range parts {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
