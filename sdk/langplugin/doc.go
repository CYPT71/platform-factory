// Package langplugin is the supported SDK for platform-factory-lang-*
// binaries (plugins/lang-python, plugins/lang-node, and the rest of the
// built-in language plugins - see docs/language-plugin-layers.md) and
// for the host's own management of them.
//
// It has two jobs. WriteDeterministicTar turns a directory of installed
// dependencies into the same deterministic, validated tar shape every
// built-in language plugin's build-layer subcommand needs to produce,
// so that shape lives in one place instead of being copy-pasted per
// language. The host independently re-validates and re-hashes whatever
// a plugin produces (internal/oci/extralayers.go) - nothing here is
// trusted just because a plugin used it.
//
// Dir, Resolve, Load, Unload, and List implement the "pf plugin
// load/unload/list" registry: a single directory of loaded plugin
// binaries that both the CLI and the host's own dispatch
// (cmd/platform-factory/language_plugin.go) read through this package
// rather than duplicating path logic - a plugin is "loaded" exactly
// when Resolve can find it, nothing more.
package langplugin
