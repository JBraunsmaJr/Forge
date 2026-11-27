package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/childrun"
	"github.com/JBraunsmaJr/forge/internal/placeholder"
)

// executeProjectPipelineStep runs a pipeline step that starts the pipeline
// of another project. The scheduler does the work: it checks that the
// project belongs to the same organization, resolves the branch or tag, and
// compiles that project's own pipeline file. The agent only asks, waits, and
// collects artifacts.
func (a *Agent) executeProjectPipelineStep(ctx context.Context, spec *api.JobSpec, target childrun.Target, jobBaseDir string) error {
	// The child run has its own workspace; this step's is not needed.
	if jobBaseDir != "" {
		defer os.RemoveAll(jobBaseDir)
	}

	start := time.Now()
	ref := spec.PipelineRef
	if ref == nil {
		ref = &api.PipelineRef{Wait: true}
	}

	// Placeholders refer to this step's environment, which holds the
	// parent run's branch, tag, and build number.
	request := childRunRequest(target, ref, spec.Env)
	refLabel := request.Ref
	if refLabel == "" {
		refLabel = "the parent run's branch or tag"
	}
	logs := pipelineLog("INFO", fmt.Sprintf("pipeline step: project %q at %s (wait=%v)", request.Project, refLabel, ref.Wait))

	body, _ := json.Marshal(request)
	resp, err := a.authPost(fmt.Sprintf("%s/api/v1/jobs/%s/child-run", a.schedulerURL, spec.JobID), "application/json", bytes.NewReader(body))
	if err != nil {
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("start child run: %v", err))...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, "", false)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("scheduler refused the child run (HTTP %d): %s", resp.StatusCode, errResp.Error))...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, "", false)
	}

	var child api.ChildRunResponse
	json.NewDecoder(resp.Body).Decode(&child)
	if len(child.RunID) < 8 {
		logs = append(logs, pipelineLog("ERROR", "scheduler returned no run ID for the child run")...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, "", false)
	}
	logs = append(logs, pipelineLog("INFO", fmt.Sprintf("child run %s started: project %q, %s at %.12s",
		child.RunID[:8], child.Project, child.Ref, child.CommitSHA))...)

	if !ref.Wait {
		logs = append(logs, pipelineLog("INFO", "fire-and-forget — not waiting for child run")...)
		return a.reportComplete(spec, 0, time.Since(start).Milliseconds(), logs, "", false)
	}
	return a.awaitChildRun(ctx, spec, ref, child.RunID, start, logs)
}

// childRunRequest builds the request for a cross-project child run,
// resolving ${{ env.NAME }} placeholders in the refs and variable values
// from the step's environment.
func childRunRequest(target childrun.Target, ref *api.PipelineRef, env map[string]string) api.ChildRunRequest {
	request := api.ChildRunRequest{
		Project:       target.Project,
		Ref:           placeholder.ExpandEnv(target.Ref, env),
		FallbackRef:   placeholder.ExpandEnv(target.FallbackRef, env),
		ArtifactsSend: ref.ArtifactsSend,
	}
	if len(ref.Variables) > 0 {
		request.Variables = make(map[string]string, len(ref.Variables))
		for name, value := range ref.Variables {
			request.Variables[name] = placeholder.ExpandEnv(value, env)
		}
	}
	return request
}
