package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JBraunsmaJr/forge/internal/compiler"
	"github.com/JBraunsmaJr/forge/internal/executor"
	"github.com/JBraunsmaJr/forge/internal/localenv"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
	"github.com/JBraunsmaJr/forge/internal/secrets"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

var (
	secretFlags []string
	envFile     string
	watch       bool
	targetStep  string
)

func init() {
	runCmd := &cobra.Command{
		Use:   "run [pipeline.yml]",
		Short: "run a pipeline locally",
		Args:  cobra.MaximumNArgs(1),
		Run:   runRun,
	}
	runCmd.Flags().StringSliceVar(&secretFlags, "secret", []string{}, "inject a secret value (repeatable)")
	runCmd.Flags().StringVar(&envFile, "env-file", "", "load secrets from a .env file")
	runCmd.Flags().BoolVar(&watch, "watch", false, "watch for changes and re-run")
	rootCmd.AddCommand(runCmd)

	validateCmd := &cobra.Command{
		Use:   "validate [pipeline.yml]",
		Short: "validate without running",
		Args:  cobra.MaximumNArgs(1),
		Run:   runValidate,
	}
	rootCmd.AddCommand(validateCmd)

	healthCmd := &cobra.Command{
		Use:   "health [pipeline.yml]",
		Short: "score a pipeline's configuration health (issue #46)",
		Args:  cobra.MaximumNArgs(1),
		Run:   runHealth,
	}
	rootCmd.AddCommand(healthCmd)

	previewCmd := &cobra.Command{
		Use:   "generate-preview [pipeline.yml]",
		Short: "preview dynamic steps from a generator",
		Args:  cobra.MaximumNArgs(1),
		Run:   runGeneratePreview,
	}
	previewCmd.Flags().StringVar(&targetStep, "step", "", "target a specific generator step")
	previewCmd.Flags().StringSliceVar(&secretFlags, "secret", []string{}, "inject a secret value (repeatable)")
	previewCmd.Flags().StringVar(&envFile, "env-file", "", "load secrets from a .env file")
	rootCmd.AddCommand(previewCmd)
}

// getPipelinePath - return path of pipeline
func getPipelinePath(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	defaults := []string{".forge/pipeline.yml", ".forge/pipeline.yaml", ".forge/pipeline.json"}
	for _, p := range defaults {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ".forge/pipeline.yml"
}

func runRun(cmd *cobra.Command, args []string) {
	pipelinePath := getPipelinePath(args)
	ref := currentGitRef()
	commitSHA := currentGitCommit()
	workspaceDir, _ := os.Getwd()

	if !watch {
		if !runOnce(pipelinePath, workspaceDir, envFile, secretFlags, ref, commitSHA) {
			os.Exit(1)
		}
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ watcher setup: %v\n", err)
		os.Exit(1)
	}
	defer watcher.Close()

	filepath.Walk(workspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == ".forge" || name == "node_modules" {
				return filepath.SkipDir
			}
			return watcher.Add(path)
		}
		return nil
	})

	fmt.Printf("👀 Watching for changes in %s...\n", workspaceDir)
	runOnce(pipelinePath, workspaceDir, envFile, secretFlags, ref, commitSHA)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	debounceTimer := time.NewTimer(0)
	<-debounceTimer.C

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				debounceTimer.Reset(500 * time.Millisecond)
			}
		case <-debounceTimer.C:
			fmt.Printf("\n🔄 Change detected, re-running...\n")
			ref = currentGitRef()
			commitSHA = currentGitCommit()
			runOnce(pipelinePath, workspaceDir, envFile, secretFlags, ref, commitSHA)
			fmt.Printf("\n👀 Watching for changes...\n")
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
		}
	}
}

func runHealth(cmd *cobra.Command, args []string) {
	pipelinePath := getPipelinePath(args)

	data, err := os.ReadFile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}

	lastModified := gitLastModified(pipelinePath)

	report, err := compiler.Score(pipelinePath, data, lastModified)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}

	name := report.PipelineName
	if name == "" {
		name = pipelinePath
	}
	fmt.Printf("Pipeline Health — %s\n", name)
	fmt.Printf("Score: %d/100\n\n", report.Score)

	if len(report.Findings) == 0 {
		fmt.Println("✓ No issues found")
		return
	}

	fmt.Println("Issues found:")
	if len(report.Critical) > 0 {
		fmt.Println("  CRITICAL (blocks score above 80):")
		for _, f := range report.Critical {
			fmt.Printf("  ✗ %s\n", f.Message)
		}
		fmt.Println()
	}
	if len(report.Warnings) > 0 {
		fmt.Println("  WARNINGS (blocks score above 90):")
		for _, f := range report.Warnings {
			fmt.Printf("  ⚠ %s\n", f.Message)
		}
		fmt.Println()
	}
	if len(report.Suggestions) > 0 {
		fmt.Println("  SUGGESTIONS (informational):")
		for _, f := range report.Suggestions {
			fmt.Printf("  ℹ %s\n", f.Message)
		}
		fmt.Println()
	}

	fmt.Println("Note: week-over-week trend and org-average comparison are computed by")
	fmt.Println("the scheduler's weekly health check once this project is registered —")
	fmt.Println("this local run only has this one snapshot to go on.")
}

// gitLastModified returns the commit time of the last change to path, or
// nil if that can't be determined (not in a git repo, path not tracked,
// git not installed). Best-effort only — Score's staleness check is
// skipped entirely when this returns nil, not treated as an error.
func gitLastModified(path string) *time.Time {
	out, err := exec.Command("git", "log", "-1", "--format=%ct", "--", path).Output()
	if err != nil {
		return nil
	}
	ts := strings.TrimSpace(string(out))
	if ts == "" {
		return nil
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil
	}
	t := time.Unix(sec, 0)
	return &t
}

func runValidate(cmd *cobra.Command, args []string) {
	pipelinePath := getPipelinePath(args)
	fmt.Printf("📋 Validating %s\n", pipelinePath)

	report, err := compiler.Lint(pipelinePath)
	if err != nil {
		// Couldn't even read the file — not a lint finding, a hard failure.
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}

	if report.CompileFailed {
		for _, f := range report.Findings {
			fmt.Printf("✗ %s\n", f.Message)
		}
		os.Exit(1)
	}

	fmt.Printf("✓ Parsed successfully (%d steps)\n", report.StepCount)

	errorCount, warnCount := 0, 0
	for _, f := range report.Findings {
		switch f.Severity {
		case compiler.SeverityError:
			errorCount++
			if f.Step != "" {
				fmt.Printf("✗ Step %q %s\n", f.Step, f.Message)
			} else {
				fmt.Printf("✗ %s\n", f.Message)
			}
		case compiler.SeverityWarning:
			warnCount++
			if f.Step != "" {
				fmt.Printf("⚠ Step %q %s\n", f.Step, f.Message)
			} else {
				fmt.Printf("⚠ %s\n", f.Message)
			}
		}
	}

	if errorCount == 0 && warnCount == 0 {
		fmt.Println("✓ No issues found")
	}

	if report.HasErrors() {
		os.Exit(1)
	}
}

func runGeneratePreview(cmd *cobra.Command, args []string) {
	pipelinePath := getPipelinePath(args)
	workspaceDir, _ := os.Getwd()
	ref := currentGitRef()
	commitSHA := currentGitCommit()

	fmt.Printf("📋 Compiling %s\n", pipelinePath)
	p, err := compiler.Compile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}

	var generators []*pipeline.Step
	for _, s := range p.Steps {
		if s.Type == "generator" {
			if targetStep == "" || s.ID == targetStep {
				generators = append(generators, s)
			}
		}
	}

	if len(generators) == 0 {
		if targetStep != "" {
			fmt.Fprintf(os.Stderr, "✗ no generator step found with ID %q\n", targetStep)
		} else {
			fmt.Fprintf(os.Stderr, "✗ no generator steps found in pipeline\n")
		}
		os.Exit(1)
	}

	resolverOpts := []localenv.Option{
		localenv.WithSecretFlags(secretFlags),
		localenv.WithAutoEnvFile(workspaceDir),
	}
	if envFile != "" {
		resolverOpts = []localenv.Option{
			localenv.WithSecretFlags(secretFlags),
			localenv.WithEnvFile(envFile),
			localenv.WithAutoEnvFile(workspaceDir),
		}
	}

	vaultAddr := os.Getenv("FORGE_VAULT_ADDR")
	vaultToken := os.Getenv("FORGE_VAULT_TOKEN")
	if vaultAddr != "" && vaultToken != "" {
		vaultClient := secrets.NewClient(vaultAddr, vaultToken)
		resolverOpts = append(resolverOpts, localenv.WithVault(func(name string) (string, error) {
			return vaultClient.Get(secrets.GlobalScopePath(), name)
		}))
	}

	resolver, err := localenv.New(resolverOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}

	logDir := filepath.Join(workspaceDir, ".forge", "logs")
	exec, err := executor.New(workspaceDir, logDir, "local-preview", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ executor setup: %v\n", err)
		os.Exit(1)
	}
	exec.IsLocal = true
	exec.PipelineName = p.Name
	exec.Ref = ref
	exec.CommitSHA = commitSHA

	for _, step := range generators {
		if step.Env == nil {
			step.Env = make(map[string]string)
		}
		step.Env["FORGE_REF"] = ref
		step.Env["FORGE_COMMIT_SHA"] = commitSHA

		for _, name := range step.Secrets {
			val, err := resolver.Resolve(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "✗ secret %s: %v\n", name, err)
				os.Exit(1)
			}
			step.Env[name] = val
			step.RedactValues = append(step.RedactValues, val)
		}

		fmt.Printf("🚀 Running generator: %s\n", step.ID)
		result, err := exec.RunStep(context.Background(), step)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ execution error: %v\n", err)
			os.Exit(1)
		}

		if result.Status == pipeline.StatusFailed {
			fmt.Fprintf(os.Stderr, "✗ generator failed (exit %d)\n", result.ExitCode)
			os.Exit(1)
		}

		fmt.Printf("\n✨ Generated steps from %s:\n", step.ID)
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, result.GeneratedStepsJSON, "", "  "); err != nil {
			fmt.Println(string(result.GeneratedStepsJSON))
		} else {
			fmt.Println(pretty.String())
		}
		fmt.Println()
	}
}
