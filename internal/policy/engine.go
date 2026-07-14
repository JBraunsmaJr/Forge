package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func Apply(
	userSteps []api.StepDef,
	policies []api.PolicyInfo,
	pipelineName, workspaceDir, orgID string,
	previouslyApplied []string,
) ([]api.StepDef, []string, error) {

	steps := userSteps
	var appliedPolicies []string

	/*
		Track existing IDs for ForbidOverride checks during static injection.
		- We rebuild this map after each transformer since it can add/remove steps.
	*/
	existingIDs := buildIDSet(steps)
	appliedMap := make(map[string]bool)
	for _, name := range previouslyApplied {
		appliedMap[name] = true
	}

	for _, pol := range policies {
		if appliedMap[pol.Name] {
			continue // Already applied to parent/previous run
		}
		policyApplied := false

		// Static injection
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
					continue // user has their own version; skip injection.
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

		// Transformer ------------------------------------------------------------------
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

			// Mark any step the transformer added with the policy name so the UI can add a badge to it.
			for i := range newSteps {
				if !existingIDs[newSteps[i].ID] && newSteps[i].PolicySource == "" {
					newSteps[i].PolicySource = pol.Name
				}
			}

			steps = newSteps
			existingIDs = buildIDSet(steps)
			policyApplied = true
		}

		if policyApplied {
			appliedPolicies = append(appliedPolicies, pol.Name)
		}
	}

	return steps, appliedPolicies, nil
}

// runTransformer executes a policy transformer, piping the input JSON to its stdin and parsing
// the output JSON as the new step list.
func runTransformer(t *api.PolicyTransformer, _ []api.StepDef, input api.TransformerInput) ([]api.StepDef, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("serialising transformer input: %w", err)
	}

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
		// 1. Create the container
		createArgs := []string{
			"create", "--interactive",
			"--label", "forge.managed=true",
			"--label", "forge.policy=true",
		}
		if len(t.Command) > 0 {
			createArgs = append(createArgs, "--entrypoint", t.Command[0])
			createArgs = append(createArgs, t.Image)
			createArgs = append(createArgs, t.Command[1:]...)
		} else {
			createArgs = append(createArgs, t.Image)
		}

		createCmd := exec.CommandContext(ctx, "docker", createArgs...)
		createOut, err := createCmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("docker create failed: %w\n%s", err, string(createOut))
		}
		containerID := strings.TrimSpace(string(createOut))

		// Ensure cleanup
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerID).Run()
		}()

		// 2. Copy workspace if provided
		if input.WorkspaceDir != "" {
			src := filepath.Clean(input.WorkspaceDir)
			// Ensure trailing slash and dot for content-only copy
			srcPath := src + string(filepath.Separator) + "."

			cpCmd := exec.CommandContext(ctx, "docker", "cp", srcPath, containerID+":/workspace")
			if cpOut, err := cpCmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("docker cp failed: %w\n%s", err, string(cpOut))
			}
		}

		// 3. Prepare the start command (will be streamed below)
		cmd = exec.CommandContext(ctx, "docker", "start", "--attach", "--interactive", containerID)

	case t.Script != "":
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
