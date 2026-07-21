package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/JBraunsmaJr/forge/internal/agent"
	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/compiler"
	"github.com/JBraunsmaJr/forge/internal/proxy"
	"github.com/JBraunsmaJr/forge/internal/scheduler"
	"github.com/JBraunsmaJr/forge/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	schedulerCmd := &cobra.Command{
		Use:   "scheduler [addr]",
		Short: "start the scheduler",
		Args:  cobra.MaximumNArgs(1),
		Run:   runScheduler,
	}
	rootCmd.AddCommand(schedulerCmd)

	agentCmd := &cobra.Command{
		Use:   "agent [scheduler-url]",
		Short: "start a worker agent",
		Args:  cobra.MaximumNArgs(1),
		Run:   runAgent,
	}
	rootCmd.AddCommand(agentCmd)

	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "start the Docker socket proxy",
		Run:   runProxy,
	}
	proxyCmd.Flags().String("port", "9090", "port for the proxy management server")
	proxyCmd.Flags().String("socket-dir", "/run/forge-sockets", "directory for agent sockets")
	proxyCmd.Flags().String("docker-socket", "/var/run/docker.sock", "path to Docker socket")
	rootCmd.AddCommand(proxyCmd)

	submitCmd := &cobra.Command{
		Use:   "submit <pipeline.yml> [scheduler-url]",
		Short: "submit a pipeline to the scheduler",
		Args:  cobra.MinimumNArgs(1),
		Run:   runSubmit,
	}
	rootCmd.AddCommand(submitCmd)

	statusCmd := &cobra.Command{
		Use:   "status <run-id> [scheduler-url]",
		Short: "check a run's status",
		Args:  cobra.MinimumNArgs(1),
		Run:   runStatus,
	}
	rootCmd.AddCommand(statusCmd)

	triggerCmd := &cobra.Command{
		Use:   "trigger <project-id> [scheduler-url]",
		Short: "manually trigger a project pipeline",
		Args:  cobra.MinimumNArgs(1),
		Run:   runTrigger,
	}
	triggerCmd.Flags().String("branch", "main", "branch to trigger")
	triggerCmd.Flags().String("commit", "", "specific commit SHA to trigger")
	rootCmd.AddCommand(triggerCmd)

	pruneCmd := &cobra.Command{
		Use:   "prune [age]",
		Short: "prune old runs and artifacts (e.g. 7d, 30m)",
		Args:  cobra.MaximumNArgs(1),
		Run:   runPrune,
	}
	rootCmd.AddCommand(pruneCmd)

	loginCmd := &cobra.Command{
		Use:   "login [provider]",
		Short: "login via SSO (default: github)",
		Args:  cobra.MaximumNArgs(1),
		Run:   runLogin,
	}
	rootCmd.AddCommand(loginCmd)
}

func runScheduler(cmd *cobra.Command, args []string) {
	addr := ":8080"
	if len(args) > 0 {
		addr = args[0]
	}
	dbURL := os.Getenv("FORGE_DB_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "✗ FORGE_DB_URL is required")
		os.Exit(1)
	}

	db, err := store.Open(dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
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

func runAgent(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	if len(args) > 0 {
		schedulerURL = args[0]
	}

	workspaceDir := os.Getenv("FORGE_WORKSPACE")
	if workspaceDir == "" {
		workspaceDir, _ = os.Getwd()
	}
	cacheDir := os.Getenv("FORGE_CACHE_DIR")
	if cacheDir == "" {
		cacheDir = filepath.Join(workspaceDir, ".forge", "cache")
	}
	logDir := os.Getenv("FORGE_LOG_DIR")
	if logDir == "" {
		logDir = filepath.Join(workspaceDir, ".forge", "logs")
	}

	vaultAddr := os.Getenv("FORGE_VAULT_ADDR")
	vaultToken := os.Getenv("FORGE_VAULT_TOKEN")
	apiToken := os.Getenv("FORGE_API_TOKEN")
	proxyURL := os.Getenv("FORGE_PROXY_URL")
	agentID := os.Getenv("FORGE_AGENT_ID")
	if agentID == "" {
		agentID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}

	maxGB := 10.0
	maxPercent := 80.0
	pruneSchedule := "0 0 * * *"
	concurrency := 1
	if c := os.Getenv("FORGE_CONCURRENCY"); c != "" {
		if val, err := strconv.Atoi(c); err == nil {
			concurrency = val
		}
	}

	a := agent.New(agentID, schedulerURL, workspaceDir, cacheDir, logDir, vaultAddr, vaultToken, apiToken, proxyURL, maxGB, maxPercent, pruneSchedule, concurrency)
	if err := a.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
		os.Exit(1)
	}
}

func runProxy(cmd *cobra.Command, args []string) {
	port, _ := cmd.Flags().GetString("port")
	socketDir, _ := cmd.Flags().GetString("socket-dir")
	dockerSocket, _ := cmd.Flags().GetString("docker-socket")

	if runtime.GOOS == "windows" {
		if socketDir == "/run/forge-sockets" {
			socketDir = filepath.Join(os.TempDir(), "forge-sockets")
		}
		if dockerSocket == "/var/run/docker.sock" {
			dockerSocket = `\\.\pipe\docker_engine`
		}
	}

	p := proxy.NewProxyServer(dockerSocket, socketDir)
	defer p.Shutdown()

	fmt.Printf("✓ Docker proxy management server starting on :%s\n", port)
	if err := http.ListenAndServe(":"+port, p); err != nil {
		fmt.Fprintf(os.Stderr, "✗ proxy server: %v\n", err)
		os.Exit(1)
	}
}

func runSubmit(cmd *cobra.Command, args []string) {
	ref := currentGitRef()
	commitSHA := currentGitCommit()
	pipelinePath := args[0]
	schedulerURL := cliSchedulerURL()
	if len(args) > 1 {
		schedulerURL = args[1]
	}

	p, err := compiler.Compile(pipelinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ compile error: %v\n", err)
		os.Exit(1)
	}

	workspaceDir, _ := os.Getwd()
	body, _ := json.Marshal(api.SubmitRunRequest{
		PipelineName: p.Name,
		Steps:        p.ToAPISteps(nil),
		WorkspaceDir: workspaceDir,
		OrgID:        os.Getenv("FORGE_ORG"),
		Ref:          ref,
		CommitSHA:    commitSHA,
	})

	resp, err := cliPost(schedulerURL+"/api/v1/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "✗ submission failed (HTTP %d)\n", resp.StatusCode)
		os.Exit(1)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	fmt.Printf("✓ pipeline submitted: %s\n", res["id"])
}

func runStatus(cmd *cobra.Command, args []string) {
	runID := args[0]
	schedulerURL := cliSchedulerURL()
	if len(args) > 1 {
		schedulerURL = args[1]
	}

	resp, err := cliGet(schedulerURL + "/api/v1/runs/" + runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var run api.RunStatus
	json.NewDecoder(resp.Body).Decode(&run)

	fmt.Printf("Run %s: %s\n", run.RunID, run.Status)
	for i, job := range run.Jobs {
		id := ""
		if i < len(run.JobIDs) {
			id = run.JobIDs[i]
		}
		fmt.Printf("  %s %-20s %s\n", jobIcon(job), id, job)
	}
}

func runTrigger(cmd *cobra.Command, args []string) {
	projectID := args[0]
	schedulerURL := cliSchedulerURL()
	if len(args) > 1 {
		schedulerURL = args[1]
	}

	branch, _ := cmd.Flags().GetString("branch")
	commit, _ := cmd.Flags().GetString("commit")

	req := api.ManualTriggerRequest{
		Branch: branch,
		Commit: commit,
	}
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/api/v1/projects/%s/trigger", schedulerURL, projectID)

	resp, err := cliPost(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	fmt.Println("✓ run triggered")
}

func runPrune(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	arg := "30d"
	if len(args) > 0 {
		arg = args[0]
	}

	resp, err := cliPost(schedulerURL+"/api/v1/runs/prune?age="+arg, "application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	fmt.Println("✓ pruned")
}

func runLogin(cmd *cobra.Command, args []string) {
	fmt.Println("login not implemented in this preview")
}
