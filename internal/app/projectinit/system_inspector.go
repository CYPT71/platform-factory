package projectinit

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/detect"
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

	proposal := SystemProposal{
		Name: filepath.Base(filepath.Clean(dir)),
		Unknowns: []Unknown{
			{Subject: "connections", Reason: "no explicit connection evidence was observed"},
			{Subject: "resources", Reason: "no explicit external or shared resource evidence was observed"},
		},
	}
	for _, entry := range entries {
		source := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			if target, statErr := os.Stat(filepath.Join(dir, source)); statErr == nil && target.IsDir() {
				proposal.Unknowns = append(proposal.Unknowns, Unknown{Subject: "component." + source, Reason: "symlinked directory was not traversed during application inspection"})
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		result, err := detect.Path(filepath.Join(dir, source))
		if err != nil {
			return SystemProposal{}, fmt.Errorf("inspect component %s: %w", source, err)
		}
		if result.Kind == "unknown" {
			appLike, evidence, inspectErr := inspectAppLikeDirectory(filepath.Join(dir, source))
			if inspectErr != nil {
				return SystemProposal{}, fmt.Errorf("inspect possible component %s: %w", source, inspectErr)
			}
			if appLike {
				proposal.Unknowns = append(proposal.Unknowns, Unknown{Subject: "component." + source, Reason: "application-like source was observed without a supported project marker: " + evidence})
			}
			continue
		}
		unknowns := []Unknown{{Subject: "runtime.selected", Reason: "operator confirmation required"}}
		if result.Ambiguous {
			unknowns = append(unknowns, Unknown{Subject: "language", Reason: "multiple project markers were observed: " + strings.Join(result.Candidates, ", ")})
			proposal.Unknowns = append(proposal.Unknowns, Unknown{Subject: "component." + source + ".language", Reason: "multiple project markers were observed: " + strings.Join(result.Candidates, ", ")})
		}
		proposal.Components = append(proposal.Components, ComponentProposal{
			Name:   source,
			Source: source,
			Runtime: RuntimeDecision{
				Recommended: RuntimeContainer,
				Reasons:     []string{"application project markers observed: " + strings.Join(result.Evidence, ", ")},
				Unknowns:    unknowns,
			},
		})
	}
	canonicalizeSystemProposal(&proposal)
	if err := proposal.Validate(); err != nil {
		return SystemProposal{}, err
	}
	return proposal, nil
}

// inspectAppLikeDirectory examines only the immediate directory. Source files
// are evidence that a component scope may exist, but never enough evidence to
// invent its language, build, or runtime contract.
func inspectAppLikeDirectory(dir string) (bool, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, "", err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			continue
		}
		extension := strings.ToLower(path.Ext(entry.Name()))
		switch extension {
		case ".c", ".cc", ".cpp", ".cs", ".go", ".java", ".js", ".kt", ".php", ".py", ".rb", ".rs", ".swift", ".ts":
			return true, entry.Name(), nil
		}
	}
	return false, "", nil
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
