// Package v1 defines the stable platform-factory pipeline API.
//
// Its wire contract is the compatibility-promoted v1beta1 contract. The type
// aliases are intentional: existing Go integrations can migrate without data
// conversion, while api_version identifies the stable wire version.
package v1

import beta "github.com/CYPT71/platform-factory/api/pipeline/v1beta1"

const APIVersion = "platform-factory.dev/v1"

type Pipeline = beta.Pipeline
type Input = beta.Input
type Stage = beta.Stage
type Command = beta.Command
type ImageReference = beta.ImageReference
type Mount = beta.Mount
type SecretReference = beta.SecretReference
type CacheMount = beta.CacheMount
type ArtifactReference = beta.ArtifactReference
type ArtifactDeclaration = beta.ArtifactDeclaration
type Output = beta.Output
type NetworkPolicy = beta.NetworkPolicy
type ResourceLimits = beta.ResourceLimits
type SandboxPolicy = beta.SandboxPolicy

const (
	NetworkNone    = beta.NetworkNone
	NetworkResolve = beta.NetworkResolve
	NetworkFull    = beta.NetworkFull
)
