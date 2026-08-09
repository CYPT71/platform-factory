package mtls

import "crypto/x509"

// HasRole reports whether leaf's Subject Organization values include
// role.
//
// A verified certificate proves two independent facts, and callers must
// not conflate them: CommonName is conventionally each peer's own unique
// identity (for example a specific worker's ID, which
// cmd/platform-factory-control-plane keys its state by), while Organization is
// the deliberate place to encode which kind of component a certificate
// is allowed to authenticate as (for example "worker" versus a future
// "operator" or "scheduler"). Checking only that a certificate chains to
// the trusted CA - without also checking its declared role - lets any
// certificate that CA ever issued authenticate as any component; HasRole
// is the missing half of that check.
func HasRole(leaf *x509.Certificate, role string) bool {
	if leaf == nil {
		return false
	}
	for _, organization := range leaf.Subject.Organization {
		if organization == role {
			return true
		}
	}
	return false
}
