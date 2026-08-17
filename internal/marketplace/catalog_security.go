package marketplace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Threat model for everything in this file: a catalog is UNTRUSTED
// DISCOVERY, never a trust decision (see catalog.go's package-level
// note). Two different things get validated here, deliberately with
// different rules:
//
//   - The catalog ENDPOINT itself (the URL an operator configures via
//     PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL or --catalog-url) is
//     operator-chosen configuration, the same trust level as a
//     hand-added marketplace-sources.json entry. It is never checked
//     against isBlockedIP - doing so would make an internal "catalogue
//     d'entreprise" impossible to configure at all.
//   - Everything that comes back FROM the network afterward - every
//     repository URL listed inside the catalog's JSON body, and every
//     redirect target the catalog endpoint's own response points at -
//     is attacker-reachable content (anyone who can write to a public
//     catalog can put anything in these) and gets the full check below.
//
// Known, honest limitation: this only guards HTTP calls this process's
// own client makes (the catalog GET/PUT). It cannot intercept the
// separate `git` subprocess's own DNS resolution for `git clone`/`git
// ls-remote` against a repository URL - a TOCTOU window exists between
// this validation and git's own, later resolution. These are reasonable
// limits against a hostile catalog, not a guarantee against a
// sufficiently adversarial DNS setup.

var errCredentialsInURL = errors.New("marketplace: URL must not embed credentials")

// ValidateCatalogRepositoryURL parses raw, requires it to be a plain
// https URL with no embedded credentials, resolves its host, and
// rejects it if any resolved address is loopback, private, link-local,
// unspecified, or multicast - the same class of destination a cloud
// metadata endpoint (169.254.169.254, fd00:ec2::254, ...) falls into.
// It returns raw trimmed of surrounding whitespace on success.
func ValidateCatalogRepositoryURL(ctx context.Context, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("marketplace: repository URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("marketplace: invalid repository URL %q: %w", trimmed, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("marketplace: repository URL %q must use https", trimmed)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("marketplace: repository URL %q: %w", trimmed, errCredentialsInURL)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("marketplace: repository URL %q has no host", trimmed)
	}
	if err := checkHostSafe(ctx, host); err != nil {
		return "", fmt.Errorf("marketplace: repository URL %q: %w", trimmed, err)
	}
	return trimmed, nil
}

// checkHostSafe rejects host (an IP literal or a DNS name) if it names,
// or resolves to, any blocked destination. A DNS name must have at
// least one address, and every address it resolves to must be safe -
// failing closed on a single unsafe answer rather than trusting the
// first safe one a hostile or rebinding-capable resolver hands back.
func checkHostSafe(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("host %s is not a permitted destination", host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve host %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("host %s did not resolve to any address", host)
	}
	for _, addr := range addrs {
		if isBlockedIP(addr.IP) {
			return fmt.Errorf("host %s resolves to %s, not a permitted destination", host, addr.IP)
		}
	}
	return nil
}

// isBlockedIP reports whether ip is loopback, RFC 1918/4193 private,
// link-local (unicast or multicast - this range is also where cloud
// metadata services like 169.254.169.254 and fd00:ec2::254 live),
// unspecified, or interface-local/global multicast.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
}

// checkRedirectSafe is used as http.Client.CheckRedirect: it caps the
// number of hops and requires every redirect target to be an https URL
// resolving to a non-blocked destination - the response, and therefore
// every Location header in it, is attacker-reachable the moment the
// catalog it came from is (see the package note above).
func checkRedirectSafe(req *http.Request, via []*http.Request) error {
	if len(via) >= maxCatalogRedirects {
		return fmt.Errorf("marketplace: stopped after %d redirects", maxCatalogRedirects)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("marketplace: redirect to non-https URL %q refused", req.URL.Redacted())
	}
	if req.URL.User != nil {
		return fmt.Errorf("marketplace: redirect target %w", errCredentialsInURL)
	}
	host := req.URL.Hostname()
	if host == "" {
		return errors.New("marketplace: redirect target has no host")
	}
	if err := checkHostSafe(req.Context(), host); err != nil {
		return fmt.Errorf("marketplace: redirect target refused: %w", err)
	}
	return nil
}
