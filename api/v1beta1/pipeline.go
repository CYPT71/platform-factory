// Package v1beta1 is the compatibility-promoted pipeline API. Its wire fields
// are intentionally identical to v1alpha1; only api_version changes. Type
// aliases let existing plugins migrate without source conversions while the
// conformance suite verifies both versions through the same engine.
package v1beta1

import alpha "github.com/CYPT71/secure-oci-base/api/v1alpha1"

const (
	APIVersion = "platform-factory.dev/v1beta1"
	// LegacyAPIVersion is the pre-rebrand identifier - see
	// v1alpha1.LegacyAPIVersion's doc comment.
	LegacyAPIVersion = "secure-oci.dev/v1beta1"
)

type Pipeline = alpha.Pipeline
type Input = alpha.Input
type Stage = alpha.Stage
type Command = alpha.Command
type ImageReference = alpha.ImageReference
type Mount = alpha.Mount
type SecretReference = alpha.SecretReference
type CacheMount = alpha.CacheMount
type ArtifactReference = alpha.ArtifactReference
type ArtifactDeclaration = alpha.ArtifactDeclaration
type Output = alpha.Output
type NetworkPolicy = alpha.NetworkPolicy
type ResourceLimits = alpha.ResourceLimits
type SandboxPolicy = alpha.SandboxPolicy

const (
	NetworkNone    = alpha.NetworkNone
	NetworkResolve = alpha.NetworkResolve
	NetworkFull    = alpha.NetworkFull
)
