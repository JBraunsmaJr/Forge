// Package main is the entry point for the Forge CLI.
//
// Commands:
//
//	forge run [file]          - run a pipeline locally (Phase 1/2)
//	forge validate [file]     - validate without running
//	forge scheduler           - start the scheduler server
//	forge agent               - start an agent worker
//	forge submit [file]       - submit a pipeline to a running scheduler
//	forge status <run-id>     - poll a run's status
//	forge version             - print version
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JBraunsmaJr/forge/internal/agent"
	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/compiler"
	"github.com/JBraunsmaJr/forge/internal/executor"
	"github.com/JBraunsmaJr/forge/internal/localenv"
	"github.com/JBraunsmaJr/forge/internal/runner"
	"github.com/JBraunsmaJr/forge/internal/scheduler"
	"github.com/JBraunsmaJr/forge/internal/secrets"
	"github.com/JBraunsmaJr/forge/internal/store"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCommand()
	case "validate":
		validateCommand()
	case "scheduler":
		schedulerCommand()
	case "agent":
		agentCommand()
	case "submit":
		submitCommand()
	case "status":
		statusCommand()
	case "secret":
		secretCommand()
	case "cancel":
		cancelCommand()
	case "rerun":
		rerunCommand()
	case "runs":
		runsCommand()
	case "token":
		tokenCommand()
	case "org":
		orgCommand()
	case "policy":
		policyCommand()
	case "project":
		projectCommand()
	case "trigger":
		triggerCommand()
	case "version":
		fmt.Printf("forge %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// ── Local run (Phase 1/2, unchanged) ─────────────────────────────────────────

func runCommand() {
	// ── Parse args: pipeline path + optional flags ─────────────────────
	pipelinePath := ".forge/pipeline.json"
	var secretFlags []string
	var envFile string

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--secret" && i+1 < len(args):
			i++
			secretFlags = append(secretFlags, args[i])
		case strings.HasPrefix(args[i], "--secret="):
			secretFlags = append(secretFlags, strings.TrimPrefix(args[i], "--secret="))
		case args[i] == "--env-file" && i+1 < len(args):
			i++
			envFile = args[i]
		case strings.HasPrefix(args[i], "--env-file="):
			envFile = strings.TrimPrefix(args[i], "--env-file=")
		case !strings.HasPrefix(args[i], "-"):
			pipelinePath = args[i]
		}
	}

	fmt.Printf("📋 Compiling %s\n", pipelinePath)
	p, err := compiler.Compile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ compile error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   %d steps compiled\n", len(p.Steps))

	// ── Build secret resolver ──────────────────────────────────────────
	// Priority chain: --secret flags → --env-file → .env → Vault
	workspaceDir, _ := os.Getwd()

	resolverOpts := []localenv.Option{
		localenv.WithSecretFlags(secretFlags),
		localenv.WithAutoEnvFile(workspaceDir), // .env in cwd, optional
	}
	if envFile != "" {
		// WithEnvFile overrides WithAutoEnvFile for the same key
		// Insert before WithAutoEnvFile so explicit file wins
		resolverOpts = []localenv.Option{
			localenv.WithSecretFlags(secretFlags),
			localenv.WithEnvFile(envFile),
			localenv.WithAutoEnvFile(workspaceDir),
		}
	}

	// Vault is a fallback if configured.
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

	// ── Resolve secrets for each step ─────────────────────────────────
	for _, step := range p.Steps {
		if len(step.Secrets) == 0 {
			continue
		}
		if step.Env == nil {
			step.Env = make(map[string]string)
		}
		for _, name := range step.Secrets {
			val, err := resolver.Resolve(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
				os.Exit(1)
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

	exec, err := executor.New(workspaceDir, logDir, cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ executor setup: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintf(os.Stderr, "\n⚡ interrupted — canceling...\n")
		cancel()
	}()

	result, err := exec.RunPipeline(ctx, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ pipeline error: %v\n", err)
		os.Exit(1)
	}

	runner.PrintSummary(result)
	if !result.Passed {
		os.Exit(1)
	}
}

func validateCommand() {
	pipelinePath := ".forge/pipeline.json"
	if len(os.Args) >= 3 {
		pipelinePath = os.Args[2]
	}
	_, err := compiler.Compile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ invalid: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ pipeline is valid")
}

// ── Scheduler ─────────────────────────────────────────────────────────────────

func schedulerCommand() {
	addr := ":8080"
	if len(os.Args) >= 3 {
		addr = os.Args[2]
	}

	// Open the Postgres database.
	// FORGE_DB_URL format: postgres://user:pass@host:port/dbname?sslmode=disable
	dbURL := os.Getenv("FORGE_DB_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "✗ FORGE_DB_URL is required")
		fmt.Fprintln(os.Stderr, "\nQuick start — run Postgres in Docker:")
		fmt.Fprintln(os.Stderr, "  docker run --rm -p 5432:5432 -e POSTGRES_PASSWORD=forge -e POSTGRES_DB=forge postgres:16-alpine")
		fmt.Fprintln(os.Stderr, "\nThen set:")
		fmt.Fprintln(os.Stderr, "  $env:FORGE_DB_URL = 'postgres://postgres:forge@localhost:5432/forge?sslmode=disable'")
		os.Exit(1)
	}

	fmt.Println("[scheduler] connecting to database...")
	var (
		db  *sql.DB
		err error
	)
	for attempt := 1; attempt <= 15; attempt++ {
		db, err = store.Open(dbURL)
		if err == nil {
			break
		}
		// Print the real postgres error every time so it's visible in logs.
		fmt.Printf("[scheduler] database attempt %d/15 failed: %v\n", attempt, err)
		if attempt == 15 {
			fmt.Fprintf(os.Stderr, "✗ database: giving up after 15 attempts\n")
			os.Exit(1)
		}
		wait := time.Duration(attempt) * time.Second
		fmt.Printf("[scheduler] retrying in %s...\n", wait)
		time.Sleep(wait)
	}
	defer db.Close()
	fmt.Println("[scheduler] database ready")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\n[scheduler] shutting down...")
		cancel()
	}()

	baseURL := os.Getenv("FORGE_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost" + addr
	}

	srv := scheduler.NewServer(addr, db, baseURL)
	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scheduler error: %v\n", err)
		os.Exit(1)
	}
}

// ── Agent ─────────────────────────────────────────────────────────────────────

func agentCommand() {
	schedulerURL := "http://localhost:8080"
	if len(os.Args) >= 3 {
		schedulerURL = os.Args[2]
	}

	workspaceDir, _ := os.Getwd()
	cacheDir := filepath.Join(workspaceDir, ".forge", "cache")

	vaultAddr := os.Getenv("FORGE_VAULT_ADDR")
	vaultToken := os.Getenv("FORGE_VAULT_TOKEN")
	if vaultAddr != "" {
		fmt.Printf("[agent] Vault configured at %s\n", vaultAddr)
	}

	// FORGE_AGENT_WS_PORT is the public address browsers use to reach this
	// agent's WebSocket terminal server. Defaults to localhost:8082.
	wsPort := os.Getenv("FORGE_AGENT_WS_PORT")
	if wsPort == "" {
		wsPort = "8082"
	}
	fmt.Printf("[agent] terminal server port: %s (scheduler-proxied)\n", wsPort)

	apiToken := os.Getenv("FORGE_API_TOKEN")
	if apiToken == "" {
		fmt.Fprintln(os.Stderr, "⚠ FORGE_API_TOKEN not set — scheduler requests will be rejected")
	}

	agentID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\n[agent] shutting down...")
		cancel()
	}()

	a := agent.New(agentID, schedulerURL, workspaceDir, cacheDir, vaultAddr, vaultToken, wsPort, apiToken)
	if err := a.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
		os.Exit(1)
	}
}

// ── Submit ────────────────────────────────────────────────────────────────────

func submitCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge submit <pipeline.json> [scheduler-url]")
		os.Exit(1)
	}
	pipelinePath := os.Args[2]
	schedulerURL := "http://localhost:8080"
	if len(os.Args) >= 4 {
		schedulerURL = os.Args[3]
	}

	// Compile locally first — catch errors before sending to scheduler.
	p, err := compiler.Compile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ compile error: %v\n", err)
		os.Exit(1)
	}

	workspaceDir, _ := os.Getwd()

	// Convert pipeline.Steps → api.StepDefs for the HTTP request.
	steps := make([]api.StepDef, len(p.Steps))
	for i, s := range p.Steps {
		steps[i] = api.StepDef{
			ID:          s.ID,
			Image:       s.Image,
			Command:     s.Command,
			WorkDir:     s.WorkDir,
			Env:         s.Env,
			DependsOn:   s.DependsOn,
			Inputs:      s.Inputs,
			Timeout:     s.Timeout,
			SecretNames: s.Secrets, // names only — values never leave the agent
		}
	}

	body, _ := json.Marshal(api.SubmitRunRequest{
		PipelineName: p.Name,
		Steps:        steps,
		WorkspaceDir: workspaceDir,
		OrgID:        os.Getenv("FORGE_ORG"),
	})

	resp, err := cliPost(
		schedulerURL+"/api/v1/runs",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ could not reach scheduler at %s: %v\n", schedulerURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	// Check status BEFORE decoding — error responses have a different body shape.
	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		fmt.Fprintf(os.Stderr, "✗ submission failed (HTTP %d): %s\n", resp.StatusCode, errResp.Error)
		os.Exit(1)
	}

	var submitResp api.SubmitRunResponse
	json.NewDecoder(resp.Body).Decode(&submitResp)

	fmt.Printf("✓ submitted: run ID %s\n", submitResp.RunID)
	fmt.Printf("  %s\n", submitResp.Message)
	fmt.Printf("\n  poll status with:\n")
	fmt.Printf("  forge status %s\n", submitResp.RunID)
}

// ── Status ────────────────────────────────────────────────────────────────────

func statusCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge status <run-id> [scheduler-url]")
		os.Exit(1)
	}
	runID := os.Args[2]
	schedulerURL := "http://localhost:8080"
	if len(os.Args) >= 4 {
		schedulerURL = os.Args[3]
	}

	resp, err := cliGet(schedulerURL + "/api/v1/runs/" + runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "✗ run not found: %s\n", runID)
		os.Exit(1)
	}

	var status api.RunStatus
	json.NewDecoder(resp.Body).Decode(&status)

	fmt.Printf("\nRun: %s  (%s)\n", status.RunID[:8], status.Name)
	fmt.Printf("Status: %s\n", status.Status)
	fmt.Printf("Jobs:\n")
	for i, js := range status.Jobs {
		icon := jobIcon(js)
		id := ""
		if i < len(status.JobIDs) {
			id = status.JobIDs[i][:8]
		}
		fmt.Printf("  %s  %s  [%s]\n", icon, js, id)
	}
}

func jobIcon(s api.JobStatus) string {
	switch s {
	case api.JobStatusPassed:
		return "✓"
	case api.JobStatusFailed:
		return "✗"
	case api.JobStatusRunning:
		return "●"
	case api.JobStatusQueued:
		return "⏳"
	case api.JobStatusCanceled:
		return "–"
	default:
		return "?"
	}
}

// ── Secret management ─────────────────────────────────────────────────────────

// ── Org management ────────────────────────────────────────────────────────────

// ── Project management ────────────────────────────────────────────────────────

// ── Token management ──────────────────────────────────────────────────────────

func tokenCommand() {
	schedulerURL := "http://localhost:8080"
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge token <create|list|revoke> [args]")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: forge token create <name> [--role admin|agent]")
			os.Exit(1)
		}
		role := "admin"
		for i := 4; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--role" {
				role = os.Args[i+1]
			}
		}
		body, _ := json.Marshal(api.CreateTokenRequest{Name: os.Args[3], Role: role})
		resp, err := cliPost(schedulerURL+"/api/v1/tokens", "application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		checkResp(resp, http.StatusCreated)
		var result api.CreateTokenResponse
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("✓ token created\n")
		fmt.Printf("  ID:    %s\n", result.Info.ID)
		fmt.Printf("  Name:  %s\n", result.Info.Name)
		fmt.Printf("  Role:  %s\n", result.Info.Role)
		fmt.Printf("\n  Token (shown once — store this now):\n  %s\n", result.Token)
		fmt.Printf("\nSet for this session:\n  $env:FORGE_API_TOKEN = '%s'\n", result.Token)

	case "list":
		resp, err := cliGet(schedulerURL + "/api/v1/tokens")
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		checkResp(resp, http.StatusOK)
		var tokens []api.TokenInfo
		json.NewDecoder(resp.Body).Decode(&tokens)
		if len(tokens) == 0 {
			fmt.Println("no tokens")
			return
		}
		fmt.Printf("%-14s  %-20s  %-8s  %s\n", "ID", "NAME", "ROLE", "CREATED")
		for _, t := range tokens {
			fmt.Printf("%-14s  %-20s  %-8s  %s\n",
				t.ID, t.Name, t.Role, t.CreatedAt.Format("2006-01-02 15:04"))
		}

	case "revoke":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: forge token revoke <token-id>")
			os.Exit(1)
		}
		resp, err := cliDelete(fmt.Sprintf("%s/api/v1/tokens/%s", schedulerURL, os.Args[3]))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		resp.Body.Close()
		fmt.Printf("✓ token %s revoked\n", os.Args[3])

	default:
		fmt.Fprintf(os.Stderr, "unknown token subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// ── Cancel / Rerun / Runs management ─────────────────────────────────────────

func cancelCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge cancel <run-id>")
		os.Exit(1)
	}
	schedulerURL := "http://localhost:8080"
	resp, err := cliPost(
		fmt.Sprintf("%s/api/v1/runs/%s/cancel", schedulerURL, os.Args[2]),
		"application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintln(os.Stderr, "✗ run not found or already finished")
		os.Exit(1)
	}
	var result map[string]int64
	json.NewDecoder(resp.Body).Decode(&result)
	fmt.Printf("✓ run %s canceled (%d jobs)\n", os.Args[2][:8], result["canceled_jobs"])
}

func rerunCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge rerun <run-id>")
		os.Exit(1)
	}
	schedulerURL := "http://localhost:8080"
	resp, err := cliPost(
		fmt.Sprintf("%s/api/v1/runs/%s/rerun", schedulerURL, os.Args[2]),
		"application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusCreated)
	var result api.SubmitRunResponse
	json.NewDecoder(resp.Body).Decode(&result)
	fmt.Printf("✓ rerun submitted → run %s\n", result.RunID)
}

func runsCommand() {
	schedulerURL := "http://localhost:8080"
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge runs <prune> [--days N]")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "prune":
		days := "30"
		for i := 3; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--days" {
				days = os.Args[i+1]
			}
		}
		resp, err := cliPost(
			fmt.Sprintf("%s/api/v1/runs/prune?days=%s", schedulerURL, days),
			"application/json", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		checkResp(resp, http.StatusOK)
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("✓ pruned %.0f runs older than %s days\n", result["pruned"], days)
	default:
		fmt.Fprintf(os.Stderr, "unknown runs subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func projectCommand() {
	schedulerURL := "http://localhost:8080"
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge project <add|list> [args]")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "add":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "usage: forge project add <name> <repo-url> [--org <org-id>] [--token <scm-token>] [--pipeline <path>]")
			os.Exit(1)
		}
		req := api.CreateProjectRequest{
			Name:    os.Args[3],
			RepoURL: os.Args[4],
		}
		orgID := os.Getenv("FORGE_ORG")
		for i := 5; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--org":
				i++
				if i < len(os.Args) {
					orgID = os.Args[i]
				}
			case "--token":
				i++
				if i < len(os.Args) {
					req.SCMToken = os.Args[i]
				}
			case "--pipeline":
				i++
				if i < len(os.Args) {
					req.PipelinePath = os.Args[i]
				}
			}
		}
		body, _ := json.Marshal(req)
		url := schedulerURL + "/api/v1/projects"
		if orgID != "" {
			url += "?org_id=" + orgID
		}
		resp, err := cliPost(url, "application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			var errResp api.ErrorResponse
			json.NewDecoder(resp.Body).Decode(&errResp)
			fmt.Fprintf(os.Stderr, "✗ project creation failed (HTTP %d): %s\n", resp.StatusCode, errResp.Error)
			os.Exit(1)
		}
		var proj api.ProjectInfo
		json.NewDecoder(resp.Body).Decode(&proj)

		fmt.Printf("✓ project created\n")
		fmt.Printf("  ID:     %s\n", proj.ID)
		fmt.Printf("  Repo:   %s\n", proj.RepoURL)
		fmt.Printf("\n  GitHub webhook URL:\n")
		fmt.Printf("  %s/api/v1/webhook/github/%s\n", schedulerURL, proj.ID)
		fmt.Printf("\n  GitLab webhook URL:\n")
		fmt.Printf("  %s/api/v1/webhook/gitlab/%s\n", schedulerURL, proj.ID)
		fmt.Printf("\n  ⚠ Webhook secret (save this — it won't be shown again):\n")
		fmt.Printf("  %s\n", proj.WebhookSecret)

	case "list":
		orgID := os.Getenv("FORGE_ORG")
		url := schedulerURL + "/api/v1/projects"
		if orgID != "" {
			url += "?org_id=" + orgID
		}
		resp, err := cliGet(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		var projects []api.ProjectInfo
		json.NewDecoder(resp.Body).Decode(&projects)
		if len(projects) == 0 {
			fmt.Println("no projects")
			return
		}
		for _, p := range projects {
			fmt.Printf("%s  %-20s  %s\n", p.ID, p.Name, p.RepoURL)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown project subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func triggerCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge trigger <project-id> [--branch <name>] [--commit <sha>] [scheduler-url]")
		os.Exit(1)
	}
	projectID := os.Args[2]

	branch := "main"
	commit := ""
	schedulerURL := "http://localhost:8080"

	for i := 3; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--branch" && i+1 < len(os.Args) {
			branch = os.Args[i+1]
			i++
		} else if arg == "--commit" && i+1 < len(os.Args) {
			commit = os.Args[i+1]
			i++
		} else if !strings.HasPrefix(arg, "--") {
			schedulerURL = arg
		}
	}

	req := api.ManualTriggerRequest{
		Branch: branch,
		Commit: commit,
	}
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/api/v1/projects/%s/trigger", schedulerURL, url.PathEscape(projectID))

	resp, err := cliPost(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		fmt.Fprintf(os.Stderr, "✗ trigger failed (HTTP %d): %s\n", resp.StatusCode, errResp.Error)
		os.Exit(1)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	fmt.Printf("✓ run triggered: %s\n", res["run_id"])
}

func orgCommand() {
	schedulerURL := "http://localhost:8080"
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge org <create|list> [args]")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: forge org create <name>")
			os.Exit(1)
		}
		body, _ := json.Marshal(api.CreateOrgRequest{Name: os.Args[3]})
		resp, err := cliPost(schedulerURL+"/api/v1/orgs", "application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		checkResp(resp, http.StatusCreated)
		var org api.OrgInfo
		json.NewDecoder(resp.Body).Decode(&org)
		fmt.Printf("✓ org created\n  ID:   %s\n  Name: %s\n\n  Set for this session:\n  $env:FORGE_ORG = '%s'\n", org.ID, org.Name, org.ID)

	case "list":
		resp, err := cliGet(schedulerURL + "/api/v1/orgs")
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		var orgs []api.OrgInfo
		json.NewDecoder(resp.Body).Decode(&orgs)
		if len(orgs) == 0 {
			fmt.Println("no orgs")
			return
		}
		for _, o := range orgs {
			fmt.Printf("%s  %s\n", o.ID, o.Name)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown org subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// ── Policy management ─────────────────────────────────────────────────────────

func policyCommand() {
	schedulerURL := "http://localhost:8080"
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge policy <create|list|delete> [args]")
		os.Exit(1)
	}

	orgID := os.Getenv("FORGE_ORG")
	if orgID == "" {
		fmt.Fprintln(os.Stderr, "✗ FORGE_ORG must be set\n  $env:FORGE_ORG = '<org-id>'")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "create":
		// forge policy create <name> <pipeline.json>
		// The pipeline.json defines the mandatory steps for this policy.
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "usage: forge policy create <name> <steps.json>")
			os.Exit(1)
		}
		name := os.Args[3]
		stepsPath := os.Args[4]

		p, err := compiler.Compile(stepsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		steps := make([]api.StepDef, len(p.Steps))
		for i, s := range p.Steps {
			steps[i] = api.StepDef{
				ID: s.ID, Image: s.Image, Command: s.Command,
				WorkDir: s.WorkDir, Env: s.Env, DependsOn: s.DependsOn,
				Inputs: s.Inputs, Timeout: s.Timeout, Type: s.Type,
			}
		}
		req := api.CreatePolicyRequest{
			Name:           name,
			Description:    fmt.Sprintf("Policy %s (%d steps)", name, len(steps)),
			Steps:          steps,
			ForbidOverride: true,
		}
		body, _ := json.Marshal(req)
		resp, err := cliPost(
			fmt.Sprintf("%s/api/v1/orgs/%s/policies", schedulerURL, orgID),
			"application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		checkResp(resp, http.StatusCreated)
		var pol api.PolicyInfo
		json.NewDecoder(resp.Body).Decode(&pol)
		fmt.Printf("✓ policy %q created (ID: %s)\n  Steps: %d  ForbidOverride: %v\n",
			pol.Name, pol.ID, len(pol.Steps), pol.ForbidOverride)

	case "list":
		resp, err := cliGet(fmt.Sprintf("%s/api/v1/orgs/%s/policies", schedulerURL, orgID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		var pols []api.PolicyInfo
		json.NewDecoder(resp.Body).Decode(&pols)
		if len(pols) == 0 {
			fmt.Printf("no policies for org %s\n", orgID)
			return
		}
		for _, p := range pols {
			kind := "static"
			if p.Transformer != nil {
				kind = "transformer"
				if p.Transformer.Image != "" {
					kind = "transformer (image: " + p.Transformer.Image + ")"
				} else {
					kind = "transformer (inline script)"
				}
			}
			fmt.Printf("%s  %-20s  %s  forbid_override=%v\n",
				p.ID, p.Name, kind, p.ForbidOverride)
		}

	case "transformer":
		// forge policy transformer <name> --image <image> [--command <cmd>]
		// forge policy transformer <name> --script <file>
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: forge policy transformer <name> --image <image> | --script <file.sh>")
			os.Exit(1)
		}
		name := os.Args[3]
		transformer := &api.PolicyTransformer{}

		for i := 4; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--image":
				i++
				if i < len(os.Args) {
					transformer.Image = os.Args[i]
				}
			case "--command":
				// Consume all subsequent args until the next --flag.
				// Allows: --command python3 /policies/script.py
				transformer.Command = []string{}
				for i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "--") {
					i++
					transformer.Command = append(transformer.Command, os.Args[i])
				}
			case "--script":
				i++
				if i < len(os.Args) {
					scriptBytes, err := os.ReadFile(os.Args[i])
					if err != nil {
						fmt.Fprintf(os.Stderr, "✗ reading script: %v\n", err)
						os.Exit(1)
					}
					transformer.Script = string(scriptBytes)
				}
			case "--timeout":
				i++
				if i < len(os.Args) {
					transformer.Timeout = os.Args[i]
				}
			}
		}

		if transformer.Image == "" && transformer.Script == "" {
			fmt.Fprintln(os.Stderr, "✗ must provide --image <image> or --script <file.sh>")
			os.Exit(1)
		}

		req := api.CreatePolicyRequest{
			Name:        name,
			Description: fmt.Sprintf("Transformer policy: %s", name),
			Transformer: transformer,
		}
		body, _ := json.Marshal(req)
		resp, err := cliPost(
			fmt.Sprintf("%s/api/v1/orgs/%s/policies", schedulerURL, orgID),
			"application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		checkResp(resp, http.StatusCreated)
		var pol api.PolicyInfo
		json.NewDecoder(resp.Body).Decode(&pol)
		fmt.Printf("✓ transformer policy %q created (ID: %s)\n", pol.Name, pol.ID)
		if pol.Transformer != nil && pol.Transformer.Image != "" {
			fmt.Printf("  Image: %s\n", pol.Transformer.Image)
		} else {
			fmt.Println("  Mode: inline script")
		}

	case "delete":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: forge policy delete <policy-id>")
			os.Exit(1)
		}
		resp, err := cliDelete(
			fmt.Sprintf("%s/api/v1/orgs/%s/policies/%s", schedulerURL, orgID, os.Args[3]))
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		resp.Body.Close()
		fmt.Println("✓ policy deleted")

	default:
		fmt.Fprintf(os.Stderr, "unknown policy subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func secretCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: forge secret <set|get|list> [args]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Scope flags (optional):")
		fmt.Fprintln(os.Stderr, "  --org     <id>   store/fetch at org scope")
		fmt.Fprintln(os.Stderr, "  --project <id>   store/fetch at project scope (highest priority)")
		fmt.Fprintln(os.Stderr, "  (no flag)        global scope")
		os.Exit(1)
	}

	addr := os.Getenv("FORGE_VAULT_ADDR")
	token := os.Getenv("FORGE_VAULT_TOKEN")
	if addr == "" || token == "" {
		fmt.Fprintln(os.Stderr, "✗ FORGE_VAULT_ADDR and FORGE_VAULT_TOKEN must be set")
		fmt.Fprintln(os.Stderr, "\nQuick start — run Vault in dev mode:")
		fmt.Fprintln(os.Stderr, "  docker run --rm -p 8200:8200 -e VAULT_DEV_ROOT_TOKEN_ID=forge-dev-token hashicorp/vault")
		fmt.Fprintln(os.Stderr, "\nThen set:")
		fmt.Fprintln(os.Stderr, "  $env:FORGE_VAULT_ADDR  = 'http://localhost:8200'")
		fmt.Fprintln(os.Stderr, "  $env:FORGE_VAULT_TOKEN = 'forge-dev-token'")
		os.Exit(1)
	}

	client := secrets.NewClient(addr, token)

	if err := client.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ cannot connect to Vault at %s\n", addr)
		fmt.Fprintf(os.Stderr, "  error: %v\n\n", err)
		fmt.Fprintln(os.Stderr, "Troubleshooting:")
		fmt.Fprintln(os.Stderr, "  1. Is Vault running?")
		fmt.Fprintln(os.Stderr, "     docker run --rm -p 8200:8200 -e VAULT_DEV_ROOT_TOKEN_ID=forge-dev-token hashicorp/vault")
		fmt.Fprintln(os.Stderr, "  2. Test connectivity from PowerShell:")
		fmt.Fprintln(os.Stderr, "     Invoke-WebRequest http://localhost:8200/v1/sys/health")
		fmt.Fprintln(os.Stderr, "  3. Is FORGE_VAULT_ADDR set correctly?")
		fmt.Fprintf(os.Stderr, "     Current value: %q\n", addr)
		os.Exit(1)
	}

	// Parse scope flags from remaining args.
	// Flags can appear after the subcommand or after the positional args.
	orgID := os.Getenv("FORGE_ORG")
	projectID := ""
	var positional []string
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--org":
			if i+1 < len(args) {
				i++
				orgID = args[i]
			}
		case "--project":
			if i+1 < len(args) {
				i++
				projectID = args[i]
			}
		default:
			positional = append(positional, args[i])
		}
	}

	// Resolve the Vault path prefix based on scope.
	prefix := secrets.GlobalScopePath()
	scopeLabel := "global"
	if projectID != "" {
		prefix = secrets.ProjectScopePath(projectID)
		scopeLabel = fmt.Sprintf("project %s", projectID)
	} else if orgID != "" {
		prefix = secrets.OrgScopePath(orgID)
		scopeLabel = fmt.Sprintf("org %s", orgID)
	}

	switch os.Args[2] {
	case "set":
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "usage: forge secret set <NAME> <VALUE> [--org <id>] [--project <id>]")
			os.Exit(1)
		}
		name, value := positional[0], positional[1]
		if err := client.Set(prefix, name, value); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ secret %q stored at scope: %s\n", name, scopeLabel)

	case "get":
		if len(positional) < 1 {
			fmt.Fprintln(os.Stderr, "usage: forge secret get <NAME> [--org <id>] [--project <id>]")
			os.Exit(1)
		}
		name := positional[0]
		// When getting with an explicit scope, fetch from that scope only.
		// When no scope is given, walk the full resolution chain.
		var val string
		var err error
		if projectID != "" || orgID != "" {
			val, err = client.Get(prefix, name)
		} else {
			// No scope: show global but note that at runtime the chain is walked.
			val, err = client.Get(prefix, name)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		fmt.Println(val)

	case "list":
		names, err := client.List(prefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			os.Exit(1)
		}
		if len(names) == 0 {
			fmt.Printf("no secrets at scope: %s\n", scopeLabel)
			return
		}
		fmt.Printf("Secrets at scope: %s\n", scopeLabel)
		for _, n := range names {
			fmt.Println(" ", n)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown secret subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// checkResp checks the HTTP status and exits with the error body if not the expected code.
// Use this everywhere the CLI makes a POST/PUT/DELETE and needs to decode the response.
func checkResp(resp *http.Response, expect int) {
	if resp.StatusCode != expect {
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

// cliToken returns the API token from the environment.
func cliToken() string {
	return os.Getenv("FORGE_API_TOKEN")
}

// cliPost makes an authenticated POST from the CLI.
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

// cliGet makes an authenticated GET from the CLI.
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

// cliDelete makes an authenticated DELETE from the CLI.
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

func printUsage() {
	fmt.Print(`forge — a CI/CD pipeline runner

Local execution:
  forge run [pipeline.json]              run a pipeline locally
    --secret KEY=VALUE                   inject a secret value (repeatable)
    --env-file .secrets.env              load secrets from a .env file
    (auto) .env                          auto-loaded from current directory
  forge validate [pipeline.json]         validate without running

Distributed execution:
  forge scheduler [addr]                 start the scheduler (default :8080)
  forge agent [scheduler-url]            start a worker agent
  forge submit <pipeline.json>           submit a pipeline to the scheduler
  forge trigger <project-id>             manually trigger a project pipeline
  forge status <run-id>                  check a run's status

Org and policy management:
  forge org create <name>                         create an organisation
  forge org list                                  list all organisations
  forge policy create <name> <steps.json>         create a static policy
  forge policy transformer <name> --image <img>   create a transformer policy
  forge policy transformer <name> --script <file> create a transformer from a shell script
  forge policy list                               list org's policies (needs FORGE_ORG)
  forge policy delete <policy-id>                 remove a policy

Secret management:
  forge secret set <NAME> <VALUE>        store a secret in Vault
  forge secret get <NAME>                fetch a secret value
  forge secret list                      list stored secret names

Other:
  forge version                          print version

Environment variables:
  FORGE_ORG          Org ID — enables policy injection on forge submit
  FORGE_VAULT_ADDR   Vault server address (e.g. http://localhost:8200)
  FORGE_VAULT_TOKEN  Vault token for authentication

`)
}
