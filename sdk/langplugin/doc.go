// Package langplugin implements the supported protocol and utilities for
// platform-factory-lang-* plugins.
//
// It provides deterministic dependency layers, language inspection, command
// dispatch, and the loaded-plugin registry. Hosts must still validate every
// layer and protocol result received from a plugin.
package langplugin
