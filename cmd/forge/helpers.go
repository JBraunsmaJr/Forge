package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/buildnumber"
	"github.com/JBraunsmaJr/forge/internal/cache"
	"github.com/JBraunsmaJr/forge/internal/compiler"
	"github.com/JBraunsmaJr/forge/internal/executor"
	"github.com/JBraunsmaJr/forge/internal/localenv"
	"github.com/JBraunsmaJr/forge/internal/secrets"
)

// cliToken retrieve token from FORGE_API_TOKEN environment variable
func cliToken() string {
	return os.Getenv("FORGE_API_TOKEN")
}

// cliSchedulerURL retrieve URL of scheduler from FORGE_SCHEDULER_URL, or
// FORGE_URL environment variable
func cliSchedulerURL() string {
	if u := os.Getenv("FORGE_SCHEDULER_URL"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	if u := os.Getenv("FORGE_URL"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return "http://localhost:8080"
}

// cliPost make a POST request
func cliPost(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if t := cliToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return http.DefaultClient.Do(req)
}

// cliPut make a PUT request
func cliPut(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("PUT", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if t := cliToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return http.DefaultClient.Do(req)
}

// cliGet make a GET request
func cliGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if t := cliToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return http.DefaultClient.Do(req)
}

// cliDelete make a DELETE request
func cliDelete(url string) (*http.Response, error) {
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return nil, err
	}
	if t := cliToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return http.DefaultClient.Do(req)
}

// currentGitRef get current ref for git repo
func currentGitRef() string {
	cmd := exec.Command("git", "symbolic-ref", "HEAD")
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	cmd = exec.Command("git", "describe", "--tags", "--exact-match")
	if out, err := cmd.Output(); err == nil {
		return "refs/tags/" + strings.TrimSpace(string(out))
	}
	return ""
}

// currentGitCommit retrieve the git commit
func currentGitCommit() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// checkResp validate http response returned expected status code.
func checkResp(resp *http.Response, expected int) {
	if resp.StatusCode != expected {
		var e api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&e)
		msg := e.Error
		if msg == "" {
			msg = fmt.Sprintf("unexpected HTTP %d", resp.StatusCode)
		}
		fmt.Fprintf(os.Stderr, "✗ %s\n", msg)
		os.Exit(1)
	}
}

// jobIcon - consolidated logic for icon based on status
func jobIcon(s api.JobStatus) string {
	switch s {
	case api.JobStatusPassed:
		return "✅"
	case api.JobStatusFailed:
		return "❌"
	case api.JobStatusRunning:
		return "🔵"
	case api.JobStatusPending:
		return "⚪"
	case api.JobStatusCanceled:
		return "⚪"
	case api.JobStatusSkipped:
		return "⏭️"
	default:
		return "❓"
	}
}

// runOnce - run a pipeline once.
func runOnce(pipelinePath, workspaceDir, envFile string, secretFlags []string, ref, commitSHA string) bool {
	fmt.Printf("📋 Compiling %s\n", pipelinePath)
	p, err := compiler.Compile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ compile error: %v\n", err)
		return false
	}
	fmt.Printf("   %d steps compiled\n", len(p.Steps))

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
		return false
	}

	for _, step := range p.Steps {
		if step.Env == nil {
			step.Env = make(map[string]string)
		}
		step.Env["FORGE_REF"] = ref
		step.Env["FORGE_COMMIT_SHA"] = commitSHA
		if after, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
			step.Env["FORGE_COMMIT_TAG"] = after
		}
		// No scheduler/project context locally, so this can't be a real,
		// scheduler-assigned build number — use the fallback value
		// (issue #57), which is visibly distinguishable from one.
		step.Env["FORGE_BUILD_NUMBER"] = buildnumber.LocalFallback
		step.Env["FORGE_BUILD_COUNTER"] = "0"

		if len(step.Secrets) == 0 {
			continue
		}
		for _, name := range step.Secrets {
			val, err := resolver.Resolve(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
				return false
			}
			step.Env[name] = val
			step.RedactValues = append(step.RedactValues, val)
		}
		source := "local"
		if vaultAddr != "" {
			source = "local/vault"
		}
		fmt.Printf("   resolved %d secret(s) for step %s [%s]\n",
			len(step.Secrets), step.ID, source)
	}

	logDir := filepath.Join(workspaceDir, ".forge", "logs")
	cacheDir := filepath.Join(workspaceDir, ".forge", "cache")

	var cas cache.Storer
	if cacheDir != "" {
		var err error
		cas, err = cache.NewLocal(cacheDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to initialize cache: %v\n", err)
		}
	}

	agentID := os.Getenv("FORGE_AGENT_ID")
	if agentID == "" {
		agentID = "local"
	}
	exec, err := executor.New(workspaceDir, logDir, agentID, cas)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ executor setup: %v\n", err)
		return false
	}
	exec.IsLocal = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigs:
			fmt.Fprintf(os.Stderr, "\n⚡ interrupted — canceling...\n")
			cancel()
		case <-ctx.Done():
		}
	}()

	result, err := exec.RunPipeline(ctx, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ pipeline error: %v\n", err)
		return false
	}

	return result.Passed
}
