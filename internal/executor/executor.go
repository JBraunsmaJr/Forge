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
	Cache        *cache.Store

	// StreamCallback is set by the agent to receive log events in real time.
	// Each event is forwarded to the scheduler as it's produced — not buffered
	// until the step finishes. nil = no streaming (local runs).
	StreamCallback func(stepID string, ts time.Time, level, message string)
}

// New creates an Executor. cacheDir may be empty to disable caching.
func New(workspaceDir, logDir, cacheDir string) (*Executor, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	var cas *cache.Store
	if cacheDir != "" {
		var err error
		cas, err = cache.New(cacheDir)
		if err != nil {
			return nil, fmt.Errorf("creating cache store: %w", err)
		}
	}

	return &Executor{
		WorkspaceDir: workspaceDir,
		LogDir:       logDir,
		Cache:        cas,
	}, nil
}

// RunStep executes a single pipeline step, checking the CAS first.
func (e *Executor) RunStep(ctx context.Context, step *pipeline.Step) (*pipeline.StepResult, error) {
	start := time.Now()

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

	args := buildDockerArgs(step, e.WorkspaceDir)
	cmd := exec.CommandContext(ctx, "docker", args...)

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
	if passed && e.Cache != nil && step.CacheKey != "" {
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

func buildDockerArgs(step *pipeline.Step, workspaceDir string) []string {
	args := []string{
		"run", "--rm",
		"--label", "forge.managed=true",
		"--workdir", step.WorkDir,
		"--volume", workspaceDir + ":/workspace:rw",
		"--memory", "2g",
		"--stop-timeout", "10",
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
		args = append(args, "--volume", hostSocket+":/var/run/docker.sock")
		args = append(args, "-e", "DOCKER_HOST=unix:///var/run/docker.sock")
	}

	for k, v := range step.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
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

	args := buildDockerArgs(step, e.WorkspaceDir)
	cmd := exec.Command("docker", args...)

	// Capture stdout (the generated step JSON) into a buffer.
	// Stream stderr to the logger so errors are visible.
	var stdoutBuf bytes.Buffer
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = &stdoutBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting generator container: %w", err)
	}

	// Stream stderr lines to the log while the generator runs.
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		logger.Output("[stderr] " + scanner.Text())
	}

	err = cmd.Wait()
	duration := time.Since(start)

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
	out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=forge.managed=true").Output()
	if err != nil {
		return fmt.Errorf("listing forge containers: %w", err)
	}

	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}

	fmt.Printf("Cleaning up %d dangling Forge containers...\n", len(ids))

	// Stop them first (gracefully).
	args := append([]string{"stop"}, ids...)
	exec.Command("docker", args...).Run()

	// Remove them.
	args = append([]string{"rm", "-f"}, ids...)
	if err := exec.Command("docker", args...).Run(); err != nil {
		return fmt.Errorf("removing forge containers: %w", err)
	}

	return nil
}
