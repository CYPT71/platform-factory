package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/CYPT71/platform-factory/cmd/tui/marketplacetui"
	"github.com/CYPT71/platform-factory/internal/marketplace"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

func runMarketplace(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printMarketplaceUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--help", "help":
		printMarketplaceUsage(stdout)
		return 0
	case "sync":
		return runMarketplaceSync(rest, stdout, stderr)
	case "publish":
		return runMarketplacePublish(rest, stdout, stderr)
	case "search":
		return runMarketplaceSearch(rest, stdout, stderr)
	case "install":
		return runMarketplaceInstall(rest, stdout, stderr, false)
	case "update":
		return runMarketplaceInstall(rest, stdout, stderr, true)
	case "remove":
		return runMarketplaceRemove(rest, stdout, stderr)
	case "list":
		return runMarketplaceList(rest, stdout, stderr)
	case "sources":
		return runMarketplaceSources(rest, stdout, stderr)
	case "tui":
		return runMarketplaceTUI(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "platform-factory marketplace: unknown subcommand %q\n", sub)
		printMarketplaceUsage(stderr)
		return 2
	}
}

func printMarketplaceUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory marketplace — discover, install, and update plugins hosted in Git repositories

Usage:
  platform-factory marketplace sources add REPO_URL
  platform-factory marketplace sources remove REPO_URL
  platform-factory marketplace sources list
  platform-factory marketplace sync [--key PUBLIC.pem] [--catalog-url URL]
  platform-factory marketplace publish [--catalog-url URL] [--dir DIR]
  platform-factory marketplace search [QUERY] [--tag TAG] [--verified] [--sort relevance|popularity|verified|name|date] [--page N]
  platform-factory marketplace install [--allow-unsigned] [--key PUBLIC.pem] NAME[@VERSION]
  platform-factory marketplace update [--allow-unsigned] [--key PUBLIC.pem] NAME[@VERSION]
  platform-factory marketplace remove NAME
  platform-factory marketplace list
  platform-factory marketplace tui [--key PUBLIC.pem] [--allow-unsigned]

Plugins are never hosted by platform-factory itself: each lives in its own
Git repository, tags releases with SemVer (v1.2.0), and commits a
plugin.yaml manifest at its root. "sync" refreshes the local index from
every tracked repository's tags; "search"/"tui" read that index; "install"
re-fetches the exact tagged commit and verifies its checksum (and
signature, unless --allow-unsigned) before placing it.

Marketplace catalog = discovery, never trust. A catalog is a public JSON
document listing repository URLs; "sync" merges its entries with
marketplace-sources.json in memory only, never writing them there, and
every catalog-discovered repository goes through the exact same manifest,
SemVer, checksum, and signature verification as a manually-added source.
A catalog entry can never make a plugin verified, trusted, or installable
without checks. Publishing to a catalog currently requires no
authentication - it is experimental and unprotected against spam or
tampering by anyone who can reach the endpoint; do not treat catalog
membership itself as any kind of endorsement.

"sync"'s --catalog-url defaults to this repository's own
marketplace-catalog.json, served read-only via raw.githubusercontent.com
(override with PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL). "publish" has
no such default: raw.githubusercontent.com only ever answers GET, so
publishing to it would just fail - point --catalog-url (or the same env
var) at a real writable endpoint you control instead.

Examples:
  platform-factory marketplace sources add https://github.com/acme/runtime-plugin.git
  platform-factory marketplace sync --catalog-url https://example.com/catalog.json
  platform-factory marketplace search scaleway --verified
  platform-factory marketplace install acme-runtime@v1.2.0
  platform-factory marketplace tui

  # from a plugin's own repository, after tagging a release:
  git tag v1.0.0 && git push origin v1.0.0
  platform-factory marketplace publish --catalog-url https://example.com/catalog.json`)
}

func marketplacePaths() (indexPath, sourcesPath, pluginsDir string, err error) {
	indexPath, err = marketplace.DefaultIndexPath()
	if err != nil {
		return "", "", "", err
	}
	sourcesPath, err = marketplace.DefaultSourcesPath()
	if err != nil {
		return "", "", "", err
	}
	dir := os.Getenv("PLATFORM_FACTORY_MARKETPLACE_DIR")
	if dir == "" {
		config, cfgErr := os.UserConfigDir()
		if cfgErr != nil {
			return "", "", "", cfgErr
		}
		dir = config + string(os.PathSeparator) + "platform-factory" + string(os.PathSeparator) + "marketplace"
	}
	pluginsDir = dir + string(os.PathSeparator) + "plugins"
	return indexPath, sourcesPath, pluginsDir, nil
}

func runMarketplaceSync(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	catalogURL := flags.String("catalog-url", marketplace.DefaultCatalogURL(),
		"public catalog URL for repository discovery (untrusted - see PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	indexPath, sourcesPath, _, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: %v\n", err)
		return 1
	}
	sources, err := marketplace.LoadSources(sourcesPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: %v\n", err)
		return 1
	}

	// The catalog only ever discovers repository URLs; it is merged in
	// memory and never written back to marketplace-sources.json, which
	// stays the one file an operator curates by hand. Every repository
	// below - explicit or catalog-discovered - goes through the exact
	// same SyncAllWithOptions verification.
	merged := &marketplace.Sources{}
	for _, repository := range sources.Repositories {
		merged.Add(repository)
	}
	catalogDiscovered := 0
	catalogRejected := 0
	if *catalogURL != "" {
		catalog, _, skipped, err := marketplace.FetchCatalog(context.Background(), *catalogURL, nil)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace sync: fetch catalog: %v\n", err)
		} else {
			catalogRejected = len(skipped)
			for _, repository := range catalog.Repositories {
				if merged.Add(repository) {
					catalogDiscovered++
				}
			}
		}
	}

	if len(merged.Repositories) == 0 {
		fmt.Fprintln(stdout, "no sources tracked and no catalog configured; add one with: "+
			"platform-factory marketplace sources add REPO_URL, or --catalog-url")
		return 0
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: %v\n", err)
		return 1
	}
	keys, err := loadMarketplaceKeys(keyFiles)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: %v\n", err)
		return 1
	}
	results, failures := marketplace.SyncAllWithOptions(context.Background(), index, merged, keys, marketplace.DefaultPerRepositorySyncTimeout)
	if err := index.Save(indexPath); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: save index: %v\n", err)
		return 1
	}
	newTags := 0
	for _, result := range results {
		newTags += len(result.NewTags)
	}
	fmt.Fprintf(stdout, "synced %d repositories (%d from marketplace-sources.json, %d discovered via catalog), %d new release(s)\n",
		len(results), len(sources.Repositories), catalogDiscovered, newTags)
	if catalogRejected > 0 {
		fmt.Fprintf(stdout, "catalog listed %d additional repositories rejected as unsafe or invalid\n", catalogRejected)
	}
	for repository, syncErr := range failures {
		fmt.Fprintf(stderr, "  %s: %v\n", repository, syncErr)
	}
	if len(failures) > 0 {
		return 1
	}
	return 0
}

func runMarketplaceSources(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace sources add|remove|list [REPO_URL]")
		return 2
	}
	_, sourcesPath, _, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sources: %v\n", err)
		return 1
	}
	sources, err := marketplace.LoadSources(sourcesPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sources: %v\n", err)
		return 1
	}
	switch args[0] {
	case "add":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: platform-factory marketplace sources add REPO_URL")
			return 2
		}
		if !sources.Add(args[1]) {
			fmt.Fprintf(stdout, "%s is already tracked\n", args[1])
			return 0
		}
		if err := sources.Save(sourcesPath); err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace sources add: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "tracking %s\n", args[1])
		return 0
	case "remove":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: platform-factory marketplace sources remove REPO_URL")
			return 2
		}
		if !sources.Remove(args[1]) {
			fmt.Fprintf(stderr, "%s is not tracked\n", args[1])
			return 1
		}
		if err := sources.Save(sourcesPath); err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace sources remove: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "untracked %s\n", args[1])
		return 0
	case "list":
		for _, repository := range sources.Repositories {
			fmt.Fprintln(stdout, repository)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "platform-factory marketplace sources: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runMarketplaceSearch(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tag := flags.String("tag", "", "filter by exact tag")
	verified := flags.Bool("verified", false, "only plugins with at least one verified release")
	sortBy := flags.String("sort", "relevance", "relevance|popularity|verified|name|date")
	page := flags.Int("page", 1, "1-based page number")
	pageSize := flags.Int("page-size", 20, "results per page")
	flagArgs, queryArgs, err := splitMarketplaceSearchArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace search: %v\n", err)
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	query := strings.Join(queryArgs, " ")

	indexPath, _, _, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace search: %v\n", err)
		return 1
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace search: %v\n", err)
		return 1
	}
	result := marketplace.Search(index, marketplace.Request{
		Query:    query,
		Filter:   marketplace.Filter{VerifiedOnly: *verified, Tag: *tag},
		Sort:     marketplace.SortBy(*sortBy),
		Page:     *page,
		PageSize: *pageSize,
	})
	if len(result.Hits) == 0 {
		fmt.Fprintln(stdout, "no matching plugins")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLATEST\tVERIFIED\tDOWNLOADS\tDESCRIPTION")
	for _, hit := range result.Hits {
		verifiedMark := ""
		if anyReleaseVerified(hit.Plugin) {
			verifiedMark = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", hit.Plugin.Name, hit.Plugin.LatestVersion, verifiedMark,
			hit.Plugin.Downloads, hit.Plugin.Description)
	}
	tw.Flush()
	fmt.Fprintf(stdout, "page %d/%d (%d total)\n", result.Page, max(result.TotalPages, 1), result.Total)
	return 0
}

func splitMarketplaceSearchArgs(args []string) (options, query []string, err error) {
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-h" || argument == "--help" || argument == "--verified" || strings.HasPrefix(argument, "--verified=") {
			options = append(options, argument)
			continue
		}
		if strings.HasPrefix(argument, "-") {
			if strings.Contains(argument, "=") {
				options = append(options, argument)
				continue
			}
			if index+1 >= len(args) {
				return nil, nil, fmt.Errorf("option %s requires a value", argument)
			}
			options = append(options, argument, args[index+1])
			index++
			continue
		}
		query = append(query, argument)
	}
	return options, query, nil
}

func anyReleaseVerified(plugin marketplace.PluginEntry) bool {
	for _, release := range plugin.Releases {
		if release.Verified {
			return true
		}
	}
	return false
}

func splitNameVersion(arg string) (name, version string) {
	if idx := strings.LastIndex(arg, "@"); idx > 0 {
		return arg[:idx], arg[idx+1:]
	}
	return arg, ""
}

func loadMarketplaceKeys(files []string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(files))
	for _, filename := range files {
		key, err := hostplugin.LoadPublicKey(filename)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func runMarketplaceInstall(args []string, stdout, stderr io.Writer, isUpdate bool) int {
	verb := "install"
	if isUpdate {
		verb = "update"
	}
	flags := flag.NewFlagSet("marketplace "+verb, flag.ContinueOnError)
	flags.SetOutput(stderr)
	allowUnsigned := flags.Bool("allow-unsigned", false, "accept an unsigned manifest; checksum verification remains required")
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: platform-factory marketplace %s [--allow-unsigned] [--key PUBLIC.pem] NAME[@VERSION]\n", verb)
		return 2
	}
	name, version := splitNameVersion(flags.Arg(0))

	indexPath, _, pluginsDir, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace install: %v\n", err)
		return 1
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace install: %v\n", err)
		return 1
	}
	keys, err := loadMarketplaceKeys(keyFiles)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace %s: %v\n", verb, err)
		return 1
	}
	manager := &marketplace.Manager{
		Dir: pluginsDir, HostVersion: hostVersionForCompatibility(),
		TrustedKeys: keys, AllowUnsigned: *allowUnsigned,
	}

	var installed marketplace.InstalledPlugin
	if isUpdate {
		installed, err = manager.Update(context.Background(), index, name, version)
	} else {
		installed, err = manager.Install(context.Background(), index, name, version)
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace %s: %v\n", verb, err)
		return 1
	}
	pastTense := "installed"
	if isUpdate {
		pastTense = "updated"
	}
	fmt.Fprintf(stdout, "%s %s@%s\n", pastTense, installed.Name, installed.Version)
	return 0
}

// hostVersionForCompatibility reports the CLI's own version for
// Manifest.Compatibility gating. A "dev" build (the default when not
// built with -ldflags) is not valid SemVer, so Manager skips the gate
// rather than blocking every install for anyone running from source.
func hostVersionForCompatibility() string {
	return version
}

func runMarketplaceRemove(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace remove NAME")
		return 2
	}
	_, _, pluginsDir, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace remove: %v\n", err)
		return 1
	}
	manager := &marketplace.Manager{Dir: pluginsDir}
	if err := manager.Remove(args[0]); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace remove: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", args[0])
	return 0
}

func runMarketplaceList(args []string, stdout, stderr io.Writer) int {
	indexPath, _, pluginsDir, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace list: %v\n", err)
		return 1
	}
	manager := &marketplace.Manager{Dir: pluginsDir}
	installedList, err := manager.Installed()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace list: %v\n", err)
		return 1
	}
	if len(installedList) == 0 {
		fmt.Fprintln(stdout, "no plugins installed")
		return 0
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace list: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tINSTALLED\tLATEST\tUPDATE AVAILABLE")
	for _, plugin := range installedList {
		latest := plugin.Version
		updateAvailable := ""
		if entry, ok := index.Plugin(plugin.Name); ok {
			latest = entry.LatestVersion
			if entry.LatestVersion != plugin.Version {
				updateAvailable = "yes"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", plugin.Name, plugin.Version, latest, updateAvailable)
	}
	tw.Flush()
	return 0
}

func runMarketplaceTUI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace tui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	allowUnsigned := flags.Bool("allow-unsigned", false, "permit unsigned development plugins")
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace tui [--key PUBLIC.pem] [--allow-unsigned]")
		return 2
	}
	indexPath, _, pluginsDir, err := marketplacePaths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace tui: %v\n", err)
		return 1
	}
	keys, err := loadMarketplaceKeys(keyFiles)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace tui: %v\n", err)
		return 1
	}
	if err := marketplacetui.Run(marketplacetui.Config{
		IndexPath:     indexPath,
		PluginsDir:    pluginsDir,
		HostVersion:   hostVersionForCompatibility(),
		TrustedKeys:   keys,
		AllowUnsigned: *allowUnsigned,
	}); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace tui: %v\n", err)
		return 1
	}
	return 0
}

// runMarketplacePublish is meant to run from inside a plugin's own Git
// repository, after its release has been tagged and pushed: it detects
// the repository's canonical origin URL, validates plugin.yaml and its
// tag/version consistency entirely locally, and only then registers the
// repository in the public catalog if it is not already listed. This
// first, experimental publish path requires no authentication - see
// printMarketplaceUsage's own warning about that - so it can never make
// what it publishes trusted, only discoverable.
//
// Unlike "sync", this deliberately does NOT default --catalog-url to
// marketplace.DefaultCatalogURL(): that default falls back to this
// repository's own marketplace-catalog.json served over
// raw.githubusercontent.com, which is a read-only mirror of whatever is
// committed - GitHub's raw content endpoint only ever answers GET, it
// has no write API, so a PUT against it fails outright. Publishing
// always requires an explicit --catalog-url (or
// PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL) pointing at a real writable
// endpoint the publisher controls; silently reusing the read-only
// default would just fail confusingly instead of explaining why.
func runMarketplacePublish(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalogURL := flags.String("catalog-url", os.Getenv("PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL"),
		"public catalog URL to publish to - must be a real writable endpoint, "+
			"not the read-only raw.githubusercontent.com default sync falls back to "+
			"(env PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL)")
	dir := flags.String("dir", ".", "plugin repository directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace publish [--catalog-url URL] [--dir DIR]")
		return 2
	}
	if *catalogURL == "" {
		fmt.Fprintln(stderr, "platform-factory marketplace publish: no catalog URL configured "+
			"(pass --catalog-url or set PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL to a writable endpoint you control - "+
			"raw.githubusercontent.com is read-only and cannot be published to)")
		return 2
	}

	ctx := context.Background()
	repository, err := marketplace.DetectPluginRepository(ctx, *dir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace publish: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "repository: %s\n", repository)

	manifest, err := marketplace.ValidatePluginForPublish(ctx, *dir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace publish: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "plugin: %s@%s (plugin.yaml verified, tag matches)\n", manifest.Name, manifest.Version)

	added, err := marketplace.PublishRepository(ctx, *catalogURL, nil, repository)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace publish: %v\n", err)
		return 1
	}
	if !added {
		fmt.Fprintf(stdout, "%s is already listed in the catalog\n", repository)
		return 0
	}
	fmt.Fprintf(stdout, "published %s to %s\n", repository, *catalogURL)
	return 0
}
