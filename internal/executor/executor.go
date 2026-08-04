package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/cache"
	"github.com/JBraunsmaJr/forge/internal/dockerutil"
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

	// Run-level context for generators and policies
	PipelineName string
	OrgID        string
	ProjectID    string
	Ref          string
	CommitSHA    string
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
		containerID, err = dockerutil.RunDockerCreate(ctx, logger.Output, args)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to create container: %v", err), map[string]any{"error": err.Error()})
			forgelog.StepFooter(step.ID, false, time.Since(start))
			return &pipeline.StepResult{
				Step:     step,
				Status:   pipeline.StatusFailed,
				ExitCode: 1,
				Duration: time.Since(start),
				LogFile:  logPath,
			}, err
		}

		// 2. Copy workspace IN
		// We copy the host's "workspace" directory into the container's root.
		// Since the host directory is named "workspace", it will become "/workspace" in the container.
		src := filepath.Clean(e.WorkspaceDir)
		if err := dockerutil.DockerCp(ctx, src+"/.", containerID+":/workspace"); err != nil {
			logger.Error(fmt.Sprintf("failed to copy workspace into container: %v", err), map[string]any{"error": err.Error(), "src": e.WorkspaceDir})
			exec.Command("docker", "rm", "-f", containerID).Run()
			forgelog.StepFooter(step.ID, false, time.Since(start))
			return &pipeline.StepResult{
				Step:     step,
				Status:   pipeline.StatusFailed,
				ExitCode: 1,
				Duration: time.Since(start),
				LogFile:  logPath,
			}, err
		}

		// 3. Prepare start command
		cmd = exec.CommandContext(ctx, "docker", "start", "-a", containerID)
	} else {
		args := e.buildDockerArgs(step, e.WorkspaceDir, false)
		cmd = exec.CommandContext(ctx, "docker", append([]string{"run", "--rm"}, args...)...)
	}

	/*
		On step timeout/cancellation,  an interrupt (SIGINT on
		Unix; the closest portable equivalent os/exec supports on
		Windows too - see the runtime.GOOS=="windows" branches elsewhere in this file

		Instead of Go's default SIGKILL, so docker's signal-proxy has a chance to forward
		the shutdown into the container and let --rm actually clean it up. SIGKILL can't be
		caught: the local docker CLI process dies instantly without ever telling the daemon to
		stop the container, which orphans it - and anything it spawned,
		e.g. a nested docker compose stack for an integration-test step
		- running indefinitely after Forge has already marked the step as timed
		out. WaitDelay caps how long we give it before Go escalates to a hard kill of its
		own, so a container that ignores the interrupt can't hang the agent.
	*/

	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 15 * time.Second

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
		}, err
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
		}, err
	}

	scanner := bufio.NewScanner(outputPipe)
	// Support lines up to 10MB (default is 64KB)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		logger.Output(scanner.Text())
	}
	if serr := scanner.Err(); serr != nil {
		logger.Error(fmt.Sprintf("log scanner error: %v", serr), map[string]any{"error": serr.Error()})
	}

	err = cmd.Wait()
	duration := time.Since(start)

	if e.UseCopy && containerID != "" {
		// 4. Copy workspace OUT (to capture any changes/artifacts)
		// We copy "/workspace" from the container back to the host's job directory.
		//
		// The copy gets its own grace period rather than the step context:
		// by the time we get here the step budget is often nearly spent, and
		// after a timeout it is *fully* spent — so copying with ctx failed
		// unconditionally with "context deadline exceeded" and threw away
		// exactly the output (test reports, artifacts) needed to debug the
		// timeout. WithoutCancel keeps ctx values but detaches the deadline.
		copyCtx, copyCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		src := containerID + ":/workspace/."
		dst := e.WorkspaceDir
		if err := dockerutil.DockerCp(copyCtx, src, dst); err != nil {
			logger.Error(fmt.Sprintf("failed to copy workspace out of container: %v", err), map[string]any{"error": err.Error(), "src": src, "dst": dst})
		}
		copyCancel()

		// 5. Cleanup container
		exec.Command("docker", "rm", "-f", containerID).Run()
	}

	exitCode := 0
	passed := true
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			logger.Error(fmt.Sprintf("step failed: %v", err), map[string]any{"exit_code": exitCode})
		} else {
			exitCode = 1
			logger.Error(fmt.Sprintf("step failed: %v", err), map[string]any{"error": err.Error()})
		}
		passed = false
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
	}, err
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
		if hostname, _ := os.Hostname(); hostname != "" && dockerutil.IsRunningInContainer() {
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

	// Inject /workspace/.forge/bin into PATH if it's already defined in Env.
	// We don't provide a default PATH here to avoid overriding the image's default PATH.
	// For shell commands (run:), we prepend it in the command itself at the end of this function.
	for k, v := range step.Env {
		val := v
		if k == "PATH" {
			val = "/workspace/.forge/bin:" + v
		}
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, val))
	}

	// Always inject the current agent's ID so steps (like integration tests)
	// can label their own Docker resources correctly for the proxy.
	// We also inject FORGE_PROXY_AGENT_ID so child agents know what label to use.
	args = append(args, "-e", "FORGE_AGENT_ID="+e.AgentID)
	args = append(args, "-e", "FORGE_PROXY_AGENT_ID="+e.getLabelAgentID())

	args = append(args, step.Image)

	cmd := step.Command
	if len(cmd) >= 3 && cmd[0] == "sh" && cmd[1] == "-c" {
		// Prepend PATH to the shell command so forge is available even if we didn't
		// override the image's PATH (which we don't if step.Env["PATH"] was empty).
		newCmd := make([]string, len(cmd))
		copy(newCmd, cmd)
		newCmd[2] = "export PATH=/workspace/.forge/bin:$PATH; " + newCmd[2]
		cmd = newCmd
	}
	args = append(args, cmd...)
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

	// Prepare generator input for stdin
	input := api.GeneratorInput{
		PipelineName: e.PipelineName,
		WorkspaceDir: e.WorkspaceDir,
		OrgID:        e.OrgID,
		ProjectID:    e.ProjectID,
		Ref:          e.Ref,
		CommitSHA:    e.CommitSHA,
		Env:          step.Env,
		With:         step.With,
	}
	inputJSON, _ := json.Marshal(input)

	var cmdGen *exec.Cmd
	var containerID string

	if e.UseCopy {
		args := e.buildDockerArgs(step, "", true)
		// Add --interactive so we can pipe stdin to the container
		args = append([]string{"--interactive"}, args...)

		containerID, err = dockerutil.RunDockerCreate(context.Background(), logger.Output, args)
		if err != nil {
			return nil, fmt.Errorf("creating generator container: %w", err)
		}

		src := filepath.Clean(e.WorkspaceDir)
		if err := dockerutil.DockerCp(context.Background(), src+"/.", containerID+":/workspace"); err != nil {
			exec.Command("docker", "rm", "-f", containerID).Run()
			return nil, fmt.Errorf("copying workspace into generator: %w", err)
		}

		cmdGen = exec.Command("docker", "start", "-a", "-i", containerID)
	} else {
		args := e.buildDockerArgs(step, e.WorkspaceDir, false)
		cmdGen = exec.Command("docker", append([]string{"run", "--rm", "-i", "-a", "stdin", "-a", "stdout", "-a", "stderr"}, args...)...)
	}

	cmdGen.Stdin = bytes.NewReader(inputJSON)

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
	// Support lines up to 10MB (default is 64KB)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		logger.Output("[stderr] " + scanner.Text())
	}
	if serr := scanner.Err(); serr != nil {
		logger.Error(fmt.Sprintf("generator log scanner error: %v", serr), map[string]any{"error": serr.Error()})
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
	}, err
}

// Cleanup removes dangling Docker containers started by Forge.
// If agentID is provided, only containers for that agent are removed.
// Otherwise, all Forge-managed temporary containers (jobs, policies) are removed.
func Cleanup(agentID string) error {
	// Find all containers with the forge.managed label.
	args := []string{"ps", "-a", "--filter", "label=forge.managed=true"}
	if agentID != "" {
		args = append(args, "--filter", "label=forge.agent_id="+agentID)
	}
	args = append(args, "--format", "{{.ID}}|{{.Labels}}")

	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("listing forge containers: %v: %s", err, string(out))
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

		// If no agentID specified, we perform a broad cleanup but still restrict to
		// temporary job/policy containers to avoid stopping the scheduler/agent themselves
		// if they are running in the same Docker daemon (e.g. during dev).
		if agentID == "" {
			// We only want to clean up containers that have either a run_id or a policy label.
			if !strings.Contains(labels, "forge.run_id") && !strings.Contains(labels, "forge.policy") {
				continue
			}
		}

		toRemove = append(toRemove, id)
	}

	if len(toRemove) == 0 {
		return nil
	}

	for _, id := range toRemove {
		dockerutil.DockerStopAndRm(context.Background(), id)
	}

	return nil
}
