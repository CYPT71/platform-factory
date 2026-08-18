package project

import (
	"fmt"
	"path"
	"regexp"

	api "github.com/CYPT71/platform-factory/internal/core"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Pipeline translates a project configuration into a language-neutral
// v1alpha1 Pipeline: a freeze stage (when the language has a built-in
// or configured freeze command) feeds a build stage, both with network
// none. This connects the imperative project flow to the DAG engine so
// a project can be planned and executed as a pipeline. It does not
// replace the imperative project path; it exposes the same work as a
// validated graph.
//
// freezeCommands are the resolved freeze argv lists (the caller derives
// them the same way secure-oci project freeze does), so this package
// does not duplicate the language adapter table.
func (loaded Loaded) Pipeline(freezeCommands [][]string) (api.Pipeline, error) {
	config := loaded.Config
	artifact := config.Runtime
	if artifact == "" {
		artifact = config.Artifact
	}
	if artifact == "" {
		return api.Pipeline{}, fmt.Errorf(`project pipeline: platform-factory.yaml has no "artifact" or "runtime" set, ` +
			"so there's nothing to build a pipeline for - this is expected for a legacy-VM-disk-only project, " +
			"which doesn't go through this path yet")
	}

	pipeline := api.Pipeline{
		APIVersion: api.PipelineAPIVersion,
		Name:       pipelineName(config.Image),
	}

	var dependsOn []string
	for index, command := range freezeCommands {
		if len(command) == 0 {
			continue
		}
		id := fmt.Sprintf("freeze-%d", index)
		pipeline.Stages = append(pipeline.Stages, api.Stage{
			ID:      id,
			Command: api.Command{Executable: command[0], Args: command[1:]},
			Network: api.NetworkNone,
		})
		dependsOn = append(dependsOn, id)
	}

	if len(config.BuildCommand) > 0 {
		pipeline.Stages = append(pipeline.Stages, api.Stage{
			ID:        "build",
			DependsOn: dependsOn,
			Command:   api.Command{Executable: config.BuildCommand[0], Args: config.BuildCommand[1:]},
			Network:   api.NetworkNone,
		})
	} else if len(pipeline.Stages) == 0 {
		// A prebuilt artifact still needs one stage so the pipeline is
		// non-empty and executable.
		pipeline.Stages = append(pipeline.Stages, api.Stage{
			ID:      "assemble",
			Command: api.Command{Executable: "/bin/true"},
			Network: api.NetworkNone,
		})
	}
	return pipeline, nil
}

func pipelineName(image string) string {
	name := path.Base(image)
	if !idPattern.MatchString(name) {
		return "project"
	}
	return name
}
