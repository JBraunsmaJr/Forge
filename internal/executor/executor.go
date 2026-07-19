package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/cache"
	forgelog "github.com/JBraunsmaJr/forge/internal/log"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// Executor runs pipeline steps inside Docker containers.
type Executor struct {
	WorkspaceDir string
	LogDir       string
	Cache        cache.Storer
	IsLocal      bool // skip checkout steps if true
	UseCopy      bool // use docker cp instead of bind mounts

	// DisableCacheStore prevents the executor from automatically storing results
	// in the CAS. The caller (e.g. the agent) may want to handle storage itself
	// to include extra metadata like artifacts.
	DisableCacheStore bool

	// StreamCallback is set by the agent to receive log events in real time.
	// Each event is forwarded to the scheduler as it's produced — not buffered
	// until the step finishes. nil = no streaming (local runs).
	StreamCallback func(stepID string, ts time.Time, level, message string)

	// AgentID is used for Docker label scoping.
	AgentID string

	// ProxyAgentID is used for the forge.agent_id label to satisfy the security proxy.
	// If empty, AgentID is used.
	ProxyAgentID string
}

// New creates an Executor. cas may be nil to disable caching.
func New(workspaceDir, logDir, agentID string, cas cache.Storer) (*Executor, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	return &Executor{
		WorkspaceDir: workspaceDir,
		LogDir:       logDir,
		AgentID:      agentID,
		ProxyAgentID: os.Getenv("FORGE_PROXY_AGENT_ID"),
		Cache:        cas,
	}, nil
}

func (e *Executor) getLabelAgentID() string {
	if e.ProxyAgentID != "" {
		return e.ProxyAgentID
	}
	return e.AgentID
}

// RunStep executes a single pipeline step, checking the CAS first.
func (e *Executor) RunStep(ctx context.Context, step *pipeline.Step) (*pipeline.StepResult, error) {
	start := time.Now()

	// Skip checkout steps in local mode.
	if e.IsLocal && (step.Type == "checkout" || step.ID == "checkout" || step.ID == "_forge_checkout") {
		forgelog.StepHeader(step.ID, "(skipped)", "local run: using current directory as workspace")
		return &pipeline.StepResult{
			Step:     step,
			Status:   pipeline.StatusPassed,
			Duration: time.Since(start),
		}, nil
	}

	if e.Cache != nil && len(step.Inputs) > 0 && step.Type != "generator" {
		taskHash, err := cache.ComputeTaskHash(step, e.WorkspaceDir)
		if err == nil {

			step.CacheKey = taskHash

			if entry, hit := e.Cache.Lookup(taskHash); hit {

				forgelog.StepHeader(step.ID, step.Image, strings.Join(step.Command, " "))
				fmt.Printf("  %s◎ cache hit%s  (hash: %s...)\n",
					"\033[36m", "\033[0m", taskHash[:12])

				return &pipeline.StepResult{
					Step:     step,
					Status:   pipeline.StatusSkipped,
					ExitCode: entry.ExitCode,
					Duration: time.Since(start),
					CacheHit: true,
				}, nil
			}
		}
	}

	// Generator step
	logPath := filepath.Join(e.LogDir, step.ID+".jsonl")
	if step.Type == "generator" {
		return e.runGenerator(start, step, logPath)
	}

	// Execute (regular task).
	logger, err := forgelog.NewLogger(step.ID, logPath)
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}
	defer logger.Close()

	// Wire the real-time stream callback if one is configured.
	if e.StreamCallback != nil {
		cb := e.StreamCallback // capture for closure.
		id := step.ID
		logger.StreamCallback = func(ts time.Time, level, message string) {
			cb(id, ts, level, message)
		}
	}

	// Register secret values for log redaction.
	if len(step.RedactValues) > 0 {
		logger.RegisterSecrets(step.RedactValues)
	}

	forgelog.StepHeader(step.ID, step.Image, strings.Join(step.Command, " "))
	logger.Info("step starting", map[string]any{"image": step.Image})

	var cmd *exec.Cmd
	var containerID string

	if e.UseCopy {
		// 1. Create container
		args := e.buildDockerArgs(step, "", true)
		out, err := exec.CommandContext(ctx, "docker", append([]string{"create"}, args...)...).Output()
		if err != nil {
			logger.Error("failed to create container", map[string]any{"error": err.Error()})
			forgelog.StepFooter(step.ID, false, time.Since(start))
			return &pipeline.StepResult{
				Step:     step,
				Status:   pipeline.StatusFailed,
				ExitCode: 1,
				Duration: time.Since(start),
				LogFile:  logPath,
			}, nil
		}
		containerID = strings.TrimSpace(string(out))

		// 2. Copy workspace IN
		// We copy the host's "workspace" directory into the container's root.
		// Since the host directory is named "workspace", it will become "/workspace" in the container.
		src := filepath.Clean(e.WorkspaceDir)
		cpIn := exec.CommandContext(ctx, "docker", "cp", src+"/.", containerID+":/workspace")
		if out, err := cpIn.CombinedOutput(); err != nil {
			logger.Error(fmt.Sprintf("failed to copy workspace into container: %v: %s", err, string(out)), map[string]any{"error": err.Error(), "src": e.WorkspaceDir})
			exec.Command("docker", "rm", "-f", containerID).Run()
			forgelog.StepFooter(step.ID, false, time.Since(start))
			return &pipeline.StepResult{
				Step:     step,
				Status:   pipeline.StatusFailed,
				ExitCode: 1,
				Duration: time.Since(start),
				LogFile:  logPath,
			}, nil
		}

		// 3. Prepare start command
		cmd = exec.CommandContext(ctx, "docker", "start", "-a", containerID)
	} else {
		args := e.buildDockerArgs(step, e.WorkspaceDir, false)
		cmd = exec.CommandContext(ctx, "docker", append([]string{"run", "--rm"}, args...)...)
	}

	outputPipe, err := cmd.StdoutPipe()
	if err != nil {
		logger.Error("failed to create output pipe", map[string]any{"error": err.Error()})
		forgelog.StepFooter(step.ID, false, time.Since(start))
		return &pipeline.StepResult{
			Step:     step,
			Status:   pipeline.StatusFailed,
			ExitCode: 1,
			Duration: time.Since(start),
			LogFile:  logPath,
		}, nil
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		/*
			Container failed to start - log the reason so it appears in the UI.
			Common causes: image not found, empty --workdir, Docker daemon not running.
		*/
		logger.Error("failed to start container", map[string]any{"error": err.Error()})
		forgelog.StepFooter(step.ID, false, time.Since(start))
		return &pipeline.StepResult{
			Step:     step,
			Status:   pipeline.StatusFailed,
			ExitCode: 1,
			Duration: time.Since(start),
			LogFile:  logPath,
		}, nil
	}

	scanner := bufio.NewScanner(outputPipe)
	for scanner.Scan() {
		logger.Output(scanner.Text())
	}

	err = cmd.Wait()
	duration := time.Since(start)

	if e.UseCopy && containerID != "" {
		// 4. Copy workspace OUT (to capture any changes/artifacts)
		// We copy "/workspace" from the container back to the host's job directory.
		src := containerID + ":/workspace/."
		dst := e.WorkspaceDir
		cpOut := exec.CommandContext(ctx, "docker", "cp", src, dst)
		if err := cpOut.Run(); err != nil {
			logger.Error("failed to copy workspace out of container", map[string]any{"error": err.Error(), "src": src, "dst": dst})
		}

		// 5. Cleanup container
		exec.Command("docker", "rm", "-f", containerID).Run()
	}

	exitCode := 0
	passed := true
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		passed = false
		logger.Error("step failed", map[string]any{"exit_code": exitCode})
	} else {
		logger.Info("step passed", map[string]any{"duration_ms": duration.Milliseconds()})
	}

	forgelog.StepFooter(step.ID, passed, duration)

	status := pipeline.StatusPassed
	if !passed {
		status = pipeline.StatusFailed
	}

	// Store result in cache (only on pass).
	if passed && e.Cache != nil && step.CacheKey != "" && !e.DisableCacheStore {
		entry := &cache.Entry{
			TaskHash:  step.CacheKey,
			StepID:    step.ID,
			ExitCode:  exitCode,
			Duration:  duration,
			CreatedAt: time.Now(),
			Image:     step.Image,
			Command:   step.Command,
		}

		// Cache write errors are non-fatal - a failed write just means
		// the next run won't get a hit. Don't fail the build over it.
		if err := e.Cache.Store(entry); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: cache write failed: %v\n", err)
		}
	}

	return &pipeline.StepResult{
		Step:     step,
		Status:   status,
		ExitCode: exitCode,
		Duration: duration,
		LogFile:  logPath,
		CacheHit: false,
	}, nil
}

// RunPipeline executes all steps in dependency order.
func (e *Executor) RunPipeline(ctx context.Context, p *pipeline.Pipeline) (*pipeline.RunResult, error) {
	start := time.Now()
	results := make([]*pipeline.StepResult, 0, len(p.Steps))

	fmt.Printf("\n🔨 Running pipeline: %s\n\n", p.Name)

	for _, step := range p.Steps {
		if ctx.Err() != nil {
			results = append(results, &pipeline.StepResult{
				Step:   step,
				Status: pipeline.StatusCanceled,
			})
			continue
		}

		stepCtx, cancel := context.WithTimeout(ctx, step.Timeout)
		result, err := e.RunStep(stepCtx, step)
		cancel()

		if err != nil {
			return nil, fmt.Errorf("executing step %s: %w", step.ID, err)
		}

		results = append(results, result)

		if result.Status == pipeline.StatusFailed {
			var cancelRemaining context.CancelFunc
			ctx, cancelRemaining = context.WithCancel(ctx)
			cancelRemaining()
		}
	}

	passed := true
	for _, r := range results {
		if r.Status == pipeline.StatusFailed {
			passed = false
			break
		}
	}

	return &pipeline.RunResult{
		Pipeline: p,
		Steps:    results,
		Passed:   passed,
		Duration: time.Since(start),
	}, nil
}

func (e *Executor) buildDockerArgs(step *pipeline.Step, workspaceDir string, useCopy bool) []string {
	workDir := step.WorkDir
	if workDir == "" {
		workDir = "/workspace"
	}

	args := []string{
		"--label", "forge.managed=true",
		"--label", "forge.agent_id=" + e.getLabelAgentID(),
		"--label", "forge.run_id=" + step.RunID,
		"--label", "forge.job_id=" + step.JobID,
		"--workdir", workDir,
		"--memory", "2g",
		"--stop-timeout", "10",
	}

	if !useCopy {
		args = append(args, "--volume", workspaceDir+":/workspace:rw")
	}

	// if FORGE_DOCKER_NETWORK is set, join that network. This is needed for job containers to reach the
	// scheduler in Compose environments.
	if net := os.Getenv("FORGE_DOCKER_NETWORK"); net != "" {
		args = append(args, "--network", net)
	}

	if step.DockerSocket {
		hostSocket := "/var/run/docker.sock"
		if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
			hostSocket = strings.TrimPrefix(h, "unix://")
		} else if runtime.GOOS == "windows" {
			hostSocket = `\\.\pipe\docker_engine`
		}

		// If running in a container, use --volumes-from to inherit the proxied socket mount.
		// This avoids issues with host paths not matching container paths for named volumes.
		if hostname, _ := os.Hostname(); hostname != "" && isRunningInContainer() {
			args = append(args, "--volumes-from", hostname)
			args = append(args, "-e", "DOCKER_HOST=unix://"+hostSocket)
		} else {
			args = append(args, "--volume", hostSocket+":/var/run/docker.sock")
			args = append(args, "-e", "DOCKER_HOST=unix:///var/run/docker.sock")
		}
	}

	if len(step.Entrypoint) > 0 {
		// Docker expects a single string for --entrypoint if using it this way,
		// or we can use the array form in a JSON if we were using a different API.
		// Via CLI: --entrypoint "/bin/sh"
		// If they provided multiple, we take the first and put the rest in Command?
		// Actually, Docker CLI --entrypoint only takes the binary.
		args = append(args, "--entrypoint", step.Entrypoint[0])
		// If there are more parts to Entrypoint, they should technically be at the
		// start of the command.
		if len(step.Entrypoint) > 1 {
			step.Command = append(step.Entrypoint[1:], step.Command...)
		}
	}

	if step.OIDCToken != "" {
		args = append(args, "-e", "FORGE_ID_TOKEN="+step.OIDCToken)
	}

	for k, v := range step.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Always inject the current agent's ID so steps (like integration tests)
	// can label their own Docker resources correctly for the proxy.
	// We also inject FORGE_PROXY_AGENT_ID so child agents know what label to use.
	args = append(args, "-e", "FORGE_AGENT_ID="+e.AgentID)
	args = append(args, "-e", "FORGE_PROXY_AGENT_ID="+e.getLabelAgentID())

	args = append(args, step.Image)
	args = append(args, step.Command...)
	return args
}

// runGenerator executes a generator step, capturing stdout as JSON step definitions.
// stderr goes to the log file so script authors can see errors.
// stdout is the machine-readable output - an array of step definition objects.
func (e *Executor) runGenerator(start time.Time, step *pipeline.Step, logPath string) (*pipeline.StepResult, error) {
	logger, err := forgelog.NewLogger(step.ID, logPath)
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}
	defer logger.Close()

	if len(step.RedactValues) > 0 {
		logger.RegisterSecrets(step.RedactValues)
	}

	forgelog.StepHeader(step.ID, step.Image, strings.Join(step.Command, " "))
	logger.Info("generator step starting", map[string]any{"image": step.Image})

	var cmdGen *exec.Cmd
	var containerID string

	if e.UseCopy {
		args := e.buildDockerArgs(step, "", true)
		out, err := exec.Command("docker", append([]string{"create"}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("creating generator container: %w", err)
		}
		containerID = strings.TrimSpace(string(out))

		src := filepath.Clean(e.WorkspaceDir)
		cpIn := exec.Command("docker", "cp", src+"/.", containerID+":/workspace")
		if err := cpIn.Run(); err != nil {
			exec.Command("docker", "rm", "-f", containerID).Run()
			return nil, fmt.Errorf("copying workspace into generator: %w", err)
		}

		cmdGen = exec.Command("docker", "start", "-a", containerID)
	} else {
		args := e.buildDockerArgs(step, e.WorkspaceDir, false)
		cmdGen = exec.Command("docker", append([]string{"run", "--rm"}, args...)...)
	}

	// Capture stdout (the generated step JSON) into a buffer.
	// Stream stderr to the logger so errors are visible.
	var stdoutBuf bytes.Buffer
	stderrPipe, err := cmdGen.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmdGen.Stdout = &stdoutBuf

	if err := cmdGen.Start(); err != nil {
		return nil, fmt.Errorf("starting generator container: %w", err)
	}

	// Stream stderr lines to the log while the generator runs.
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		logger.Output("[stderr] " + scanner.Text())
	}

	err = cmdGen.Wait()
	duration := time.Since(start)

	if e.UseCopy && containerID != "" {
		exec.Command("docker", "rm", "-f", containerID).Run()
	}

	exitCode := 0
	passed := true
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		passed = false
		logger.Error("generator failed", map[string]any{"exit_code": exitCode})
	} else {
		raw := bytes.TrimSpace(stdoutBuf.Bytes())
		logger.Info("generator completed",
			map[string]any{"output_bytes": len(raw)})
	}

	forgelog.StepFooter(step.ID, passed, duration)

	status := pipeline.StatusPassed
	if !passed {
		status = pipeline.StatusFailed
	}

	return &pipeline.StepResult{
		Step:               step,
		Status:             status,
		ExitCode:           exitCode,
		Duration:           duration,
		LogFile:            logPath,
		GeneratedStepsJSON: bytes.TrimSpace(stdoutBuf.Bytes()),
	}, nil
}

// Cleanup removes all dangling Docker containers started by Forge.
func Cleanup() error {
	// Find all containers with the forge.managed label.
	// We use --format to get labels for filtering in Go.
	out, err := exec.Command("docker", "ps", "-a", "--filter", "label=forge.managed=true", "--format", "{{.ID}}|{{.Labels}}").Output()
	if err != nil {
		return fmt.Errorf("listing forge containers: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil
	}

	// Exclude ourselves and only target temporary containers (jobs/policies).
	var toRemove []string
	self := ""
	if _, err := os.Stat("/.dockerenv"); err == nil {
		self, _ = os.Hostname()
	}

	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		id := parts[0]
		labels := parts[1]

		// Docker IDs can be short or long. os.Hostname() in a container is usually the short ID.
		if self != "" && (strings.HasPrefix(self, id) || strings.HasPrefix(id, self)) {
			continue
		}

		// We only want to clean up temporary containers:
		// - Job containers (have forge.run_id)
		// - Policy transformers (have forge.policy=true)
		// We EXCLUDE stack services like scheduler, agent, postgres, etc.
		// These typically have com.docker.compose.project label, or just lack the run_id/policy label.
		if !strings.Contains(labels, "forge.run_id") && !strings.Contains(labels, "forge.policy") {
			continue
		}

		toRemove = append(toRemove, id)
	}

	if len(toRemove) == 0 {
		return nil
	}

	fmt.Printf("Cleaning up %d dangling Forge containers...\n", len(toRemove))

	// Stop them first (gracefully).
	stopArgs := append([]string{"stop"}, toRemove...)
	exec.Command("docker", stopArgs...).Run()

	// Remove them.
	rmArgs := append([]string{"rm", "-f"}, toRemove...)
	if err := exec.Command("docker", rmArgs...).Run(); err != nil {
		return fmt.Errorf("removing forge containers: %w", err)
	}

	return nil
}

func isRunningInContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
