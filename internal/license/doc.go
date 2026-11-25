// Package license issues and verifies commercial license tokens for the
// capabilities layered on top of the Business Source License's Additional
// Use Grant (see /LICENSE and docs/licensing.md) - a support SLA, a
// hosting exception, a seat count, or any future Enterprise-gated
// feature. It is deliberately not a new cryptographic scheme: a license
// is just an attestation.Envelope (the same DSSE envelope provenance
// predicates, SBOM signatures, and release subjects already use) wrapping
// a License predicate, so it inherits that pattern's already-reviewed
// signing/verification code instead of adding a second one. See
// docs/legal/pricing.md for what, if anything, is actually sold today -
// this package only provides the primitive; nothing in this codebase
// calls Verify to gate a capability yet.
package license
