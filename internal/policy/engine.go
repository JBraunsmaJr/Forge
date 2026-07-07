// Package policy implements the Forge policy engine.
//
// Two injection modes, composable per policy:
//
//  1. Static injection: policy declares steps directly. Engine prepends them.
//  2. Transformer: policy declares an executable. Engine pipes the full
//     pipeline as JSON to its stdin and reads the modified pipeline from stdout.
//
// Transformers run at submit time on the scheduler host — before any jobs
// are queued. They receive the workspace path and can inspect source files
// (Dockerfile, requirements.txt, go.mod, etc.) to decide what to inject.
//
// # Transformer protocol
//
//	stdin:  TransformerInput JSON
//	stdout: []StepDef JSON  (the complete step list after transformation)
//	stderr: logged for diagnostics; non-zero exit aborts the submission
//
// # Ordering
//
// For each policy, static injection runs first (prepending mandatory steps),
// then the transformer runs on the result. If multiple policies define
// transformers, they chain: each sees the output of the previous one.
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// Apply injects and transforms steps for all org policies.
// Returns the final step list, the names of policies that were applied,
// and an error if any ForbidOverride conflict or transformer failure occurs.
func Apply(
	userSteps []api.StepDef,
	policies []api.PolicyInfo,
	pipelineName, workspaceDir, orgID string,
) ([]api.StepDef, []string, error) {

	steps := userSteps
	var appliedPolicies []string

	// Track existing IDs for ForbidOverride checks during static injection.
	// We rebuild this map after each transformer since it can add/remove steps.
	existingIDs := buildIDSet(steps)

	for _, pol := range policies {
		policyApplied := false

		//  Static injection ----------------------------------──────────
		if len(pol.Steps) > 0 {
			var injected []api.StepDef
			for _, step := range pol.Steps {
				step.PolicySource = pol.Name
				if existingIDs[step.ID] {
					if pol.ForbidOverride {
						return nil, nil, fmt.Errorf(
							"policy %q: step %q is mandatory and cannot be overridden",
							pol.Name, step.ID,
						)
					}
					continue // user has their own version; skip injection
				}
				injected = append(injected, step)
				existingIDs[step.ID] = true
				policyApplied = true
			}
			if len(injected) > 0 {
				// Policy steps first, then the rest.
				steps = append(injected, steps...)
			}
		}

		//  Transformer ----------------------------------────────────────
		if pol.Transformer != nil {
			newSteps, err := runTransformer(pol.Transformer, steps, api.TransformerInput{
				PipelineName: pipelineName,
				Steps:        steps,
				WorkspaceDir: workspaceDir,
				OrgID:        orgID,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("policy %q transformer: %w", pol.Name, err)
			}

			// Mark any step the transformer added (not already in the old list)
			// with the policy name so the web UI can badge it.
			for i := range newSteps {
				if !existingIDs[newSteps[i].ID] && newSteps[i].PolicySource == "" {
					newSteps[i].PolicySource = pol.Name
				}
			}

			steps = newSteps
			existingIDs = buildIDSet(steps) // rebuild for next policy
			policyApplied = true
		}

		if policyApplied {
			appliedPolicies = append(appliedPolicies, pol.Name)
		}
	}

	return steps, appliedPolicies, nil
}

// runTransformer executes a policy transformer, piping the input JSON to its
// stdin and parsing the output JSON as the new step list.
func runTransformer(t *api.PolicyTransformer, _ []api.StepDef, input api.TransformerInput) ([]api.StepDef, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("serialising transformer input: %w", err)
	}

	// Parse timeout, default 30s.
	timeout := 30 * time.Second
	if t.Timeout != "" {
		if d, err := time.ParseDuration(t.Timeout); err == nil {
			timeout = d
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd

	switch {
	case t.Image != "":
		args := []string{"run", "--rm", "-i"}
		if input.WorkspaceDir != "" {
			// Convert Windows backslash paths to forward slashes.
			// D:\Forge → D:/Forge — Docker Desktop accepts both but
			// forward slashes are safer in concatenated strings.
			wsDir := strings.ReplaceAll(input.WorkspaceDir, `\`, `/`)
			args = append(args, "--volume", wsDir+":/workspace:ro")
		}

		// Use --entrypoint to override the Dockerfile ENTRYPOINT explicitly.
		// Without this, Docker prepends the image's ENTRYPOINT to our command,
		// causing double-execution (e.g. "python3 python3 /script.py").
		if len(t.Command) > 0 {
			args = append(args, "--entrypoint", t.Command[0])
			args = append(args, t.Image)
			args = append(args, t.Command[1:]...)
		} else {
			// No command override — use the image's default CMD.
			args = append(args, t.Image)
		}

		cmd = exec.CommandContext(ctx, "docker", args...)

	case t.Script != "":
		// Inline shell script — runs on the scheduler host directly.
		// Good for development; Image is preferred for production since
		// it gives the transformer a predictable, isolated environment.
		cmd = exec.CommandContext(ctx, "sh", "-c", t.Script)

	default:
		return nil, fmt.Errorf("transformer must specify either 'image' or 'script'")
	}

	cmd.Stdin = bytes.NewReader(data)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errDetail := strings.TrimSpace(stderr.String())
		if errDetail != "" {
			return nil, fmt.Errorf("%w\nstderr: %s", err, errDetail)
		}
		return nil, err
	}

	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, fmt.Errorf("transformer produced no output on stdout")
	}

	var result []api.StepDef
	if err := json.Unmarshal(out, &result); err != nil {
		preview := string(out)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("transformer output is not valid JSON: %w\noutput: %s", err, preview)
	}

	return result, nil
}

func buildIDSet(steps []api.StepDef) map[string]bool {
	m := make(map[string]bool, len(steps))
	for _, s := range steps {
		m[s.ID] = true
	}
	return m
}
