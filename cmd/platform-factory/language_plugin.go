package main

import "github.com/CYPT71/platform-factory/sdk/langplugin"

// resolveLoadedPlugin is the production internal/app/project.LanguagePluginResolver,
// backed by the same registry `pf plugin load`/`pf plugin unload` manage.
// internal/app/project cannot import sdk/langplugin itself, so this is
// the one place that binds the two together.
func resolveLoadedPlugin(language string) (string, error) {
	return langplugin.Resolve(language)
}
