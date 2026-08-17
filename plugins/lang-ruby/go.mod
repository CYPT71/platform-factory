// This module depends on the main github.com/CYPT71/platform-factory
// module only for sdk/langplugin (the shared deterministic-tar writer -
// see docs/language-plugin-layers.md), the same require+replace pattern
// plugins/containerd and plugins/kubevirt already use for their own sdk
// dependencies.
module github.com/CYPT71/platform-factory/plugins/lang-ruby

go 1.25.12

require github.com/CYPT71/platform-factory v0.0.2

replace github.com/CYPT71/platform-factory => ../..
