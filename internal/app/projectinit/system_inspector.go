package projectinit

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// InspectSystem derives a conservative system proposal from project markers
// already present on disk. It never treats a missing observation as absence:
// connections, shared resources, and runtime selection remain explicit
// unknowns until another source or the operator proves them.
func InspectSystem(dir string) (SystemProposal, error) {
	if err := validateRoot(dir); err != nil {
		return SystemProposal{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return SystemProposal{}, fmt.Errorf("inspect project components: %w", err)
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })

	proposal := SystemProposal{Name: filepath.Base(filepath.Clean(dir))}
	for _, entry := range entries {
		source := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			if target, statErr := os.Stat(filepath.Join(dir, source)); statErr == nil && target.IsDir() {
				proposal.Unknowns = append(proposal.Unknowns, Unknown{Subject: "component." + source, Reason: "symlinked directory was not traversed during application inspection"})
			}
			continue
		}
		// Ordinary subdirectories are deliberately not classified here.
		// Component and language recognition belongs to language plugins.
	}
	if len(proposal.Components) > 0 {
		proposal.Unknowns = append(proposal.Unknowns,
			Unknown{Subject: "connections", Reason: "no explicit connection evidence was observed"},
			Unknown{Subject: "resources", Reason: "no explicit external or shared resource evidence was observed"})
	}
	canonicalizeSystemProposal(&proposal)
	if err := proposal.Validate(); err != nil {
		return SystemProposal{}, err
	}
	return proposal, nil
}

// Descriptions returns stable, human-readable proposal evidence for dry-run
// and confirmation UX. It deliberately describes unknowns as unknowns.
func (p SystemProposal) Descriptions() []string {
	canonical := cloneSystemProposal(p)
	canonicalizeSystemProposal(&canonical)
	result := []string{"system proposal " + canonical.Name}
	for _, component := range canonical.Components {
		result = append(result, fmt.Sprintf("component %s from %s: recommended runtime %s (%s); selected runtime unknown", component.Name, component.Source, component.Runtime.Recommended, strings.Join(component.Runtime.Reasons, ", ")))
		for _, unknown := range component.Runtime.Unknowns {
			result = append(result, "component "+component.Name+": "+unknown.Description())
		}
	}
	for _, unknown := range canonical.Unknowns {
		result = append(result, unknown.Description())
	}
	return result
}
