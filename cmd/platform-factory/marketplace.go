package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/CYPT71/platform-factory/cmd/tui/marketplacetui"
	marketplaceapp "github.com/CYPT71/platform-factory/internal/app/marketplace"
	"github.com/CYPT71/platform-factory/internal/marketplace"
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
  platform-factory marketplace sources add [--format text|json] REPO_URL
  platform-factory marketplace sources remove [--format text|json] REPO_URL
  platform-factory marketplace sources list [--format text|json]
  platform-factory marketplace sync [--key PUBLIC.pem] [--catalog-url URL] [--format text|json]
  platform-factory marketplace publish [--catalog-url URL] [--dir DIR] [--format text|json]
  platform-factory marketplace search [QUERY] [--tag TAG] [--verified] [--sort relevance|popularity|verified|name|date] [--page N] [--format text|json]
  platform-factory marketplace install [--allow-unsigned] [--key PUBLIC.pem] [--format text|json] NAME[@VERSION]
  platform-factory marketplace update [--allow-unsigned] [--key PUBLIC.pem] [--format text|json] NAME[@VERSION]
  platform-factory marketplace remove [--format text|json] NAME
  platform-factory marketplace list [--format text|json]
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

func runMarketplaceSync(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	catalogURL := flags.String("catalog-url", marketplace.DefaultCatalogURL(),
		"public catalog URL for repository discovery (untrusted - see PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL)")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace sync [--key PUBLIC.pem] [--catalog-url URL] [--format text|json]")
		return 2
	}
	indexPath, sourcesPath, _, err := marketplaceapp.Paths()
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
		if *format == "json" {
			return writeMarketplaceSyncJSON(stdout, stderr, 0, len(sources.Repositories), catalogDiscovered, catalogRejected, 0, nil)
		}
		fmt.Fprintln(stdout, "no sources tracked and no catalog configured; add one with: "+
			"platform-factory marketplace sources add REPO_URL, or --catalog-url")
		return 0
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: %v\n", err)
		return 1
	}
	keys, err := marketplaceapp.LoadKeys(keyFiles)
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
	if *format == "json" {
		return writeMarketplaceSyncJSON(stdout, stderr, len(results), len(sources.Repositories), catalogDiscovered, catalogRejected, newTags, failures)
	}
	fmt.Fprintf(stdout, "synced %d repositories (%d from marketplace-sources.json, %d discovered via catalog), %d new release(s)\n", len(results), len(sources.Repositories), catalogDiscovered, newTags)
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
	_, sourcesPath, _, err := marketplaceapp.Paths()
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
		format, values, ok := parseMarketplaceSourceArgs("add", args[1:], stderr)
		if !ok || len(values) != 1 {
			fmt.Fprintln(stderr, "usage: platform-factory marketplace sources add [--format text|json] REPO_URL")
			return 2
		}
		repository := values[0]
		if !sources.Add(repository) {
			if format == "json" {
				return writeMarketplaceMutationJSON(stdout, stderr, "source_add", repository, "", "already_tracked")
			}
			fmt.Fprintf(stdout, "%s is already tracked\n", repository)
			return 0
		}
		if err := sources.Save(sourcesPath); err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace sources add: %v\n", err)
			return 1
		}
		if format == "json" {
			return writeMarketplaceMutationJSON(stdout, stderr, "source_add", repository, "", "tracking")
		}
		fmt.Fprintf(stdout, "tracking %s\n", repository)
		return 0
	case "remove":
		format, values, ok := parseMarketplaceSourceArgs("remove", args[1:], stderr)
		if !ok || len(values) != 1 {
			fmt.Fprintln(stderr, "usage: platform-factory marketplace sources remove [--format text|json] REPO_URL")
			return 2
		}
		repository := values[0]
		if !sources.Remove(repository) {
			fmt.Fprintf(stderr, "%s is not tracked\n", repository)
			return 1
		}
		if err := sources.Save(sourcesPath); err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace sources remove: %v\n", err)
			return 1
		}
		if format == "json" {
			return writeMarketplaceMutationJSON(stdout, stderr, "source_remove", repository, "", "untracked")
		}
		fmt.Fprintf(stdout, "untracked %s\n", repository)
		return 0
	case "list":
		format, values, ok := parseMarketplaceSourceArgs("list", args[1:], stderr)
		if !ok || len(values) != 0 {
			fmt.Fprintln(stderr, "usage: platform-factory marketplace sources list [--format text|json]")
			return 2
		}
		if format == "json" {
			if err := json.NewEncoder(stdout).Encode(map[string]any{"api_version": cliOutputAPIVersion, "sources": sources.Repositories}); err != nil {
				fmt.Fprintf(stderr, "platform-factory marketplace sources list: encode output: %v\n", err)
				return 1
			}
			return 0
		}
		for _, repository := range sources.Repositories {
			fmt.Fprintln(stdout, repository)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "platform-factory marketplace sources: unknown subcommand %q\n", args[0])
		return 2
	}
}

func writeMarketplaceSyncJSON(stdout, stderr io.Writer, synced, configured, discovered, rejected, releases int, failures map[string]error) int {
	type failureOutput struct {
		Repository string `json:"repository"`
		Error      string `json:"error"`
	}
	failureList := make([]failureOutput, 0, len(failures))
	for repository, err := range failures {
		failureList = append(failureList, failureOutput{repository, err.Error()})
	}
	sort.Slice(failureList, func(i, j int) bool { return failureList[i].Repository < failureList[j].Repository })
	result := map[string]any{
		"api_version": cliOutputAPIVersion, "operation": "sync", "resource": "marketplace",
		"synced": synced, "configured_sources": configured, "catalog_discovered": discovered,
		"catalog_rejected": rejected, "new_releases": releases, "failures": failureList,
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace sync: encode output: %v\n", err)
		return 1
	}
	if len(failures) > 0 {
		return 1
	}
	return 0
}

func parseMarketplaceSourceArgs(operation string, args []string, stderr io.Writer) (string, []string, bool) {
	flags := flag.NewFlagSet("marketplace sources "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil || !validOutputFormat(*format) {
		return "", nil, false
	}
	return *format, flags.Args(), true
}

func runMarketplaceSearch(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tag := flags.String("tag", "", "filter by exact tag")
	verified := flags.Bool("verified", false, "only plugins with at least one verified release")
	sortBy := flags.String("sort", "relevance", "relevance|popularity|verified|name|date")
	page := flags.Int("page", 1, "1-based page number")
	pageSize := flags.Int("page-size", 20, "results per page")
	format := flags.String("format", "text", "output format: text or json")
	flagArgs, queryArgs, err := splitMarketplaceSearchArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace search: %v\n", err)
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(stderr, "platform-factory marketplace search: --format must be text or json")
		return 2
	}
	query := strings.Join(queryArgs, " ")

	indexPath, _, _, err := marketplaceapp.Paths()
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
	if *format == "json" {
		if err := json.NewEncoder(stdout).Encode(map[string]any{
			"api_version": cliOutputAPIVersion, "query": query, "hits": result.Hits,
			"page": result.Page, "total_pages": result.TotalPages, "total": result.Total,
		}); err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace search: encode output: %v\n", err)
			return 1
		}
		return 0
	}
	if len(result.Hits) == 0 {
		fmt.Fprintln(stdout, "no matching plugins")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLATEST\tVERIFIED\tDOWNLOADS\tDESCRIPTION")
	for _, hit := range result.Hits {
		verifiedMark := ""
		if marketplaceapp.AnyReleaseVerified(hit.Plugin) {
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

func runMarketplaceInstall(args []string, stdout, stderr io.Writer, isUpdate bool) int {
	verb := "install"
	if isUpdate {
		verb = "update"
	}
	flags := flag.NewFlagSet("marketplace "+verb, flag.ContinueOnError)
	flags.SetOutput(stderr)
	allowUnsigned := flags.Bool("allow-unsigned", false, "accept an unsigned manifest; checksum verification remains required")
	format := flags.String("format", "text", "output format: text or json")
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*format) {
		fmt.Fprintf(stderr, "usage: platform-factory marketplace %s [--allow-unsigned] [--key PUBLIC.pem] [--format text|json] NAME[@VERSION]\n", verb)
		return 2
	}
	name, version := marketplaceapp.SplitNameVersion(flags.Arg(0))

	indexPath, _, pluginsDir, err := marketplaceapp.Paths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace install: %v\n", err)
		return 1
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace install: %v\n", err)
		return 1
	}
	keys, err := marketplaceapp.LoadKeys(keyFiles)
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
	if *format == "json" {
		return writeMarketplaceMutationJSON(stdout, stderr, verb, installed.Name, installed.Version, pastTense)
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
	flags := flag.NewFlagSet("marketplace remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace remove [--format text|json] NAME")
		return 2
	}
	name := flags.Arg(0)
	_, _, pluginsDir, err := marketplaceapp.Paths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace remove: %v\n", err)
		return 1
	}
	manager := &marketplace.Manager{Dir: pluginsDir}
	if err := manager.Remove(name); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace remove: %v\n", err)
		return 1
	}
	if *format == "json" {
		return writeMarketplaceMutationJSON(stdout, stderr, "remove", name, "", "removed")
	}
	fmt.Fprintf(stdout, "removed %s\n", name)
	return 0
}

func writeMarketplaceMutationJSON(stdout, stderr io.Writer, operation, name, version, status string) int {
	result := struct {
		APIVersion string `json:"api_version"`
		Operation  string `json:"operation"`
		Resource   string `json:"resource"`
		Name       string `json:"name"`
		Version    string `json:"version,omitempty"`
		Status     string `json:"status"`
	}{cliOutputAPIVersion, operation, "marketplace_plugin", name, version, status}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace %s: encode output: %v\n", operation, err)
		return 1
	}
	return 0
}

func runMarketplaceList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marketplace list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*format != "text" && *format != "json") {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace list [--format text|json]")
		return 2
	}
	indexPath, _, pluginsDir, err := marketplaceapp.Paths()
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
	if len(installedList) == 0 && *format == "text" {
		fmt.Fprintln(stdout, "no plugins installed")
		return 0
	}
	index, err := marketplace.LoadIndex(indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace list: %v\n", err)
		return 1
	}
	type installedOutput struct {
		Name            string `json:"name"`
		Installed       string `json:"installed"`
		Latest          string `json:"latest"`
		UpdateAvailable bool   `json:"update_available"`
	}
	output := make([]installedOutput, 0, len(installedList))
	for _, plugin := range installedList {
		latest := plugin.Version
		updateAvailable := false
		if entry, ok := index.Plugin(plugin.Name); ok {
			latest = entry.LatestVersion
			if entry.LatestVersion != plugin.Version {
				updateAvailable = true
			}
		}
		output = append(output, installedOutput{Name: plugin.Name, Installed: plugin.Version, Latest: latest, UpdateAvailable: updateAvailable})
	}
	if *format == "json" {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"api_version": cliOutputAPIVersion, "plugins": output}); err != nil {
			fmt.Fprintf(stderr, "platform-factory marketplace list: encode output: %v\n", err)
			return 1
		}
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tINSTALLED\tLATEST\tUPDATE AVAILABLE")
	for _, plugin := range output {
		update := ""
		if plugin.UpdateAvailable {
			update = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", plugin.Name, plugin.Installed, plugin.Latest, update)
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
	indexPath, _, pluginsDir, err := marketplaceapp.Paths()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace tui: %v\n", err)
		return 1
	}
	keys, err := marketplaceapp.LoadKeys(keyFiles)
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
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory marketplace publish [--catalog-url URL] [--dir DIR] [--format text|json]")
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
	if *format == "text" {
		fmt.Fprintf(stdout, "repository: %s\n", repository)
	}

	manifest, err := marketplace.ValidatePluginForPublish(ctx, *dir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace publish: %v\n", err)
		return 1
	}
	if *format == "text" {
		fmt.Fprintf(stdout, "plugin: %s@%s (plugin.yaml verified, tag matches)\n", manifest.Name, manifest.Version)
	}

	added, err := marketplace.PublishRepository(ctx, *catalogURL, nil, repository)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace publish: %v\n", err)
		return 1
	}
	if !added {
		if *format == "json" {
			return writeMarketplacePublishJSON(stdout, stderr, repository, manifest.Name, manifest.Version, *catalogURL, "already_listed")
		}
		fmt.Fprintf(stdout, "%s is already listed in the catalog\n", repository)
		return 0
	}
	if *format == "json" {
		return writeMarketplacePublishJSON(stdout, stderr, repository, manifest.Name, manifest.Version, *catalogURL, "published")
	}
	fmt.Fprintf(stdout, "published %s to %s\n", repository, *catalogURL)
	return 0
}

func writeMarketplacePublishJSON(stdout, stderr io.Writer, repository, name, version, catalogURL, status string) int {
	result := struct {
		APIVersion string `json:"api_version"`
		Operation  string `json:"operation"`
		Resource   string `json:"resource"`
		Repository string `json:"repository"`
		Name       string `json:"name"`
		Version    string `json:"version"`
		CatalogURL string `json:"catalog_url"`
		Status     string `json:"status"`
	}{cliOutputAPIVersion, "publish", "marketplace", repository, name, version, catalogURL, status}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "platform-factory marketplace publish: encode output: %v\n", err)
		return 1
	}
	return 0
}
