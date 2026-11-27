package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/dockerutil"
	"github.com/JBraunsmaJr/forge/internal/imagebuild"
	forgelog "github.com/JBraunsmaJr/forge/internal/log"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// The secrets a docker_build step reads its registry credentials from. They
// are the same names docker_publish uses, so one pair of secrets serves both
// the build and the later promotion.
const (
	registryUsernameSecret = "REGISTRY_USERNAME"
	registryPasswordSecret = "REGISTRY_PASSWORD"
)

// commandRunner runs the container CLI with args, calling onLine for every
// line of combined output. It is a field of the build so tests can replace
// the real CLI.
type commandRunner func(ctx context.Context, args []string, onLine func(string)) error

// runDockerBuild executes a docker_build step: it builds the image from the
// job's workspace and pushes it under every configured tag. The agent runs
// the container CLI itself, so the step needs neither an image nor access to
// the container socket from inside a job container.
func (e *Executor) runDockerBuild(ctx context.Context, start time.Time, step *pipeline.Step, logPath string) (*pipeline.StepResult, error) {
	logger, err := forgelog.NewLogger(step.ID, logPath)
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}
	defer logger.Close()

	if e.StreamCallback != nil {
		cb := e.StreamCallback
		id := step.ID
		logger.StreamCallback = func(ts time.Time, level, message string) {
			cb(id, ts, level, message)
		}
	}
	if len(step.RedactValues) > 0 {
		logger.RegisterSecrets(step.RedactValues)
	}

	forgelog.StepHeader(step.ID, "(image build)", "build and push")

	build := imageBuild{
		workspaceDir: e.WorkspaceDir,
		cli:          containerCLI(),
		run:          runContainerCLI,
		info:         func(msg string) { logger.Info(msg) },
		output:       logger.Output,
	}
	err = build.execute(ctx, step)

	duration := time.Since(start)
	result := &pipeline.StepResult{
		Step:     step,
		Status:   pipeline.StatusPassed,
		Duration: duration,
		LogFile:  logPath,
	}
	if err != nil {
		logger.Error(fmt.Sprintf("image build failed: %v", err), map[string]any{"error": err.Error()})
		result.Status = pipeline.StatusFailed
		result.ExitCode = 1
	} else {
		logger.Info("step passed", map[string]any{"duration_ms": duration.Milliseconds()})
	}
	forgelog.StepFooter(step.ID, err == nil, duration)
	return result, err
}

// containerCLI reports which CLI flavor the agent drives.
func containerCLI() imagebuild.CLI {
	if dockerutil.IsPodman() {
		return imagebuild.Podman
	}
	return imagebuild.Docker
}

// runContainerCLI is the commandRunner that invokes the real container CLI.
func runContainerCLI(ctx context.Context, args []string, onLine func(string)) error {
	cmd := dockerutil.CommandContext(ctx, args...)

	// Interrupt before killing, as for task steps, so the CLI can stop
	// cleanly when the step times out or is canceled.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 15 * time.Second

	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating output pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", dockerutil.Runtime(), err)
	}

	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		onLine(scanner.Text())
	}

	err = cmd.Wait()
	// Report a timeout or cancellation as such, not as the exit status of
	// the interrupted CLI, so the caller can tell the two apart.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// imageBuild carries what one docker_build step needs. It is separate from
// Executor so the sequence of commands can be tested without a container
// runtime or a registry.
type imageBuild struct {
	workspaceDir string
	cli          imagebuild.CLI
	run          commandRunner
	info         func(string)
	output       func(string)
}

// execute resolves the step's configuration, then builds and pushes.
func (b imageBuild) execute(ctx context.Context, step *pipeline.Step) error {
	cfg, err := imagebuild.Decode(step.With[imagebuild.WithKey])
	if err != nil {
		return err
	}
	cfg = cfg.ExpandEnv(step.Env)
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Credentials and the Podman digest file live outside the workspace so
	// that they can never end up in the build context or in an artifact.
	scratchDir, err := os.MkdirTemp("", "forge-image-build-")
	if err != nil {
		return fmt.Errorf("creating a scratch directory: %w", err)
	}
	defer os.RemoveAll(scratchDir)

	authDir, err := writeRegistryAuth(scratchDir, cfg.Registry, step.Env)
	if err != nil {
		return err
	}
	if authDir == "" {
		b.info("no registry credentials given; pushing anonymously")
	}

	b.info(fmt.Sprintf("building %s", strings.Join(cfg.References(), ", ")))
	if err := b.run(ctx, imagebuild.BuildCommand(b.cli, cfg, b.workspaceDir, authDir), b.output); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	for _, reference := range cfg.References() {
		digest, err := b.push(ctx, reference, authDir, scratchDir)
		if err != nil {
			return fmt.Errorf("push %s: %w", reference, err)
		}
		if digest != "" {
			b.info(fmt.Sprintf("pushed %s@%s", reference, digest))
		} else {
			b.info(fmt.Sprintf("pushed %s", reference))
		}
	}
	return nil
}

// push pushes one reference and returns the digest the registry stored it
// under, or "" if the CLI did not report one.
func (b imageBuild) push(ctx context.Context, reference, authDir, scratchDir string) (string, error) {
	digestFile := filepath.Join(scratchDir, "digest")
	_ = os.Remove(digestFile)

	var pushOutput strings.Builder
	onLine := func(line string) {
		pushOutput.WriteString(line)
		pushOutput.WriteByte('\n')
		b.output(line)
	}
	if err := b.run(ctx, imagebuild.PushCommand(b.cli, reference, authDir, digestFile), onLine); err != nil {
		return "", err
	}

	// Podman writes the digest to a file; Docker prints it.
	if data, err := os.ReadFile(digestFile); err == nil {
		if digest := imagebuild.ParsePushDigest(string(data)); digest != "" {
			return digest, nil
		}
	}
	return imagebuild.ParsePushDigest(pushOutput.String()), nil
}

// writeRegistryAuth writes a credentials file for registry into a new
// directory under scratchDir and returns that directory. It returns "" when
// the step was given no credentials, which is valid for a registry that
// accepts anonymous pushes.
func writeRegistryAuth(scratchDir, registry string, env map[string]string) (string, error) {
	username, password := env[registryUsernameSecret], env[registryPasswordSecret]
	if username == "" && password == "" {
		return "", nil
	}
	if username == "" || password == "" {
		return "", errors.New("registry credentials are incomplete: set both " +
			registryUsernameSecret + " and " + registryPasswordSecret + ", or neither")
	}

	content, err := imagebuild.AuthFile(registry, username, password)
	if err != nil {
		return "", fmt.Errorf("encoding registry credentials: %w", err)
	}
	authDir := filepath.Join(scratchDir, "auth")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		return "", fmt.Errorf("creating the credentials directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, imagebuild.AuthFileName), content, 0o600); err != nil {
		return "", fmt.Errorf("writing registry credentials: %w", err)
	}
	return authDir, nil
}
