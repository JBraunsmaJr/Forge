package executor

import (
	"fmt"

	"github.com/JBraunsmaJr/forge/internal/catrust"
)

// stageCABundle places the process's CA bundle in the step's workspace, from
// where the step's container reads it. It does nothing when no extra CA
// certificates are configured.
//
// A failure is reported but does not fail the step: buildDockerArgs only
// points a container at the bundle when the file is really there, so the
// step runs with the image's own trust store instead.
func (e *Executor) stageCABundle() {
	if err := catrust.StageInWorkspace(e.WorkspaceDir); err != nil {
		fmt.Printf("[executor] could not stage the CA bundle in %s: %v\n", e.WorkspaceDir, err)
	}
}
