package plugins

import "context"

// pluginManifestSchema documents plugins/*/plugin.json's real schema, as
// enforced by internal/plugin.Manifest.Validate() (internal/plugin/manifest.go).
// It is hand-written rather than reflected from the Go struct because
// the interesting constraints - the name/capability regexes, the
// digest length, the language-family network restriction - live in
// Validate()'s logic, not in struct tags a reflection-based generator
// could see.
const pluginManifestSchema = `{
  "$comment": "Describes plugins/<name>/plugin.json, enforced by internal/plugin.Manifest.Validate().",
  "type": "object",
  "required": ["api_version", "name", "version", "capabilities", "executable", "digest"],
  "additionalProperties": false,
  "properties": {
    "api_version": {"type": "string", "const": "platform-factory.dev/plugin-manifest/v1"},
    "name": {"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
    "version": {"type": "string", "description": "non-empty, single-line"},
    "family": {"type": "string", "enum": ["language", "analyzer", "build", "runtime", "deployment", "capability"]},
    "capabilities": {
      "type": "array", "minItems": 1, "uniqueItems": true,
      "items": {"type": "string", "pattern": "^[a-z][a-z0-9-]*(\\.[a-z][a-z0-9-]*)?$"}
    },
    "platforms": {
      "type": "array",
      "items": {"type": "string", "enum": ["linux/amd64", "linux/arm64"]}
    },
    "permissions": {
      "type": "object",
      "properties": {
        "network": {"type": "array", "items": {"type": "string"}, "description": "must be empty when family is \"language\""},
        "filesystem": {"type": "array", "items": {"type": "string"}},
        "secrets": {"type": "array", "items": {"type": "string"}}
      }
    },
    "executable": {"type": "string", "description": "clean relative path inside the plugin directory, not absolute, no .. segments"},
    "digest": {"type": "string", "pattern": "^sha256:[0-9a-f]{64}$", "description": "sha256 of the built executable; a freshly scaffolded plugin has no build yet, so pf_plugin_create writes the same all-zero stand-in digest plugins/containerd/plugin.json also ships with in source control"},
    "signature": {
      "type": "object",
      "required": ["algorithm", "value"],
      "properties": {
        "algorithm": {"type": "string", "const": "ed25519"},
        "key_id": {"type": "string"},
        "value": {"type": "string", "description": "base64 Ed25519 signature over the manifest with signature omitted"}
      }
    }
  }
}`

// SchemaResourceHandler returns the pf://plugins/schema resource
// handler.
func SchemaResourceHandler() func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		return pluginManifestSchema, "application/json", nil
	}
}
