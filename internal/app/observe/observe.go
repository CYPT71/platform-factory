// Package observe is the application-layer service behind `pf logs`,
// `pf events`, `pf rollback`, and `pf status`'s shared need to locate a
// project's most recent deployment: discovering the project root and
// decoding+validating its persisted .platform-factory/deployed.json.
// Everything else those commands do - flag parsing, invoking kubectl,
// formatting output - is genuine CLI-adapter concern and stays in
// cmd/platform-factory.
package observe

import (
	"errors"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/publicationtarget"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

// DeployedProject is the persisted identity of a project's most recent
// `pf deploy`.
type DeployedProject struct {
	APIVersion string `json:"api_version"`
	Image      string `json:"image"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Workload   string `json:"workload"`
}

// LoadDeployedProject discovers the project rooted at ".", decodes its
// deployed.json, and validates it against the same rules pf deploy
// itself enforces before persisting it, so a corrupted or hand-edited
// file fails closed rather than being trusted.
func LoadDeployedProject() (DeployedProject, error) {
	loaded, err := project.Discover(".", "")
	if err != nil {
		return DeployedProject{}, err
	}
	var state DeployedProject
	if err := strictjson.DecodeFile(filepath.Join(loaded.Root, ".platform-factory", "deployed.json"), &state); err != nil {
		return DeployedProject{}, err
	}
	if state.APIVersion != "platform-factory.dev/deployment/v1" ||
		!publicationtarget.ValidKubernetesName(state.Name) || !publicationtarget.ValidKubernetesName(state.Namespace) ||
		(state.Workload != "job" && state.Workload != "service") || !publicationtarget.ValidDigestReference(state.Image) {
		return DeployedProject{}, errors.New("persisted deployment identity is invalid")
	}
	return state, nil
}
