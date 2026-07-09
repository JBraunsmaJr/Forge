package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/cache"
	"github.com/JBraunsmaJr/forge/internal/compiler"
	"github.com/JBraunsmaJr/forge/internal/executor"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
	"github.com/JBraunsmaJr/forge/internal/secrets"
	"github.com/JBraunsmaJr/forge/internal/tracing"
)

const (
	pollInterval      = 2 * time.Second
	heartbeatInterval = 10 * time.Second
)

// Agent polls a Forge scheduler and executes jobs.
type Agent struct {
	id           string
	schedulerURL string
	workspaceDir string
	cacheDir     string
	vault        *secrets.Client
	client       *http.Client
	wsListenPort string // port the WS server listens on
	apiToken     string // FORGE_API_TOKEN — sent with every scheduler request
	debugConts   sync.Map
}

// New creates an agent that connects to schedulerURL.
func New(id, schedulerURL, workspaceDir, cacheDir, vaultAddr, vaultToken, wsListenPort, apiToken string) *Agent {
	var vault *secrets.Client
	if vaultAddr != "" && vaultToken != "" {
		vault = secrets.NewClient(vaultAddr, vaultToken)
	}
	return &Agent{
		id:           id,
		schedulerURL: schedulerURL,
		workspaceDir: workspaceDir,
		cacheDir:     cacheDir,
		vault:        vault,
		wsListenPort: wsListenPort,
		apiToken:     apiToken,
		client:       &http.Client{Timeout: 10 * time.Second},
	}
}

func (a *Agent) wsPort() string {
	if a.wsListenPort == "" {
		return "8082"
	}
	return a.wsListenPort
}

// Run starts the agent's poll loop. Blocks until ctx is canceled.
func (a *Agent) Run(ctx context.Context) error {
	fmt.Printf("[agent %s] starting, scheduler: %s\n", a.id[:8], a.schedulerURL)

	// Clean up any dangling containers from previous runs.
	executor.Cleanup()
	defer executor.Cleanup()

	// Start the WebSocket terminal server so browsers can connect directly.
	go a.runTerminalServer(ctx)

	// Run both job polling and debug session polling concurrently.
	go a.debugLoop(ctx)

	for {
		// Check if we've been asked to stop.
		if ctx.Err() != nil {
			fmt.Printf("[agent %s] shutting down\n", a.id[:8])
			return nil
		}

		// Try to lease a job.
		spec, ok, err := a.lease(ctx)
		if err != nil {
			fmt.Printf("[agent %s] lease error: %v\n", a.id[:8], err)
			a.sleep(ctx, pollInterval)
			continue
		}
		if !ok {

			a.sleep(ctx, pollInterval)
			continue
		}

		fmt.Printf("[agent %s] received job %s (step: %s)\n",
			a.id[:8], spec.JobID[:8], spec.StepID)

		if err := a.execute(ctx, spec); err != nil {
			fmt.Printf("[agent %s] execute error: %v\n", a.id[:8], err)
		}
	}
}

// execute runs a single leased job:
//  1. Starts a heartbeat goroutine
//  2. Builds a pipeline.Step from the spec
//  3. Runs it via the executor
//  4. Reports the result to the scheduler
//  5. Stops the heartbeat goroutine
func (a *Agent) execute(ctx context.Context, spec *api.JobSpec) error {
	ctx, span := tracing.Tracer().Start(ctx, "agent.execute")
	defer span.End()

	if spec.Type == "pipeline" {
		return a.executePipelineStep(ctx, spec)
	}

	/*
		Start heartbeat goroutine

		`done` is a channel we close to signal the heartbeat goroutine to stop.
		As a C# dev, this is the equivalent to `CancellationTokenSource.Cancel()`.
	*/
	done := make(chan struct{})
	defer close(done)

	go a.heartbeatLoop(spec.JobID, spec.LeaseID, done)

	/*
		Create isolated workspace

		Each job gets its own unique directory to prevent collisions
		during parallel runs on the same agent.
	*/
	jobWorkspace := filepath.Join(a.workspaceDir, "forge-job-"+spec.JobID)
	if err := os.MkdirAll(jobWorkspace, 0755); err != nil {
		a.reportComplete(spec, 1, 0, nil, nil)
		return fmt.Errorf("creating job workspace: %w", err)
	}
	defer os.RemoveAll(jobWorkspace)

	/*
		If the job belongs to a repository (ProjectID + CommitSHA are set),
		perform an automatic checkout if the workspace is empty.
		This ensures that injected steps (like security scans) and user steps
		always have the source code available without explicit checkout steps
		needing to share state via artifacts.
	*/
	if spec.ProjectID != "" && spec.CommitSHA != "" {
		files, _ := os.ReadDir(jobWorkspace)
		if len(files) == 0 {
			fmt.Printf("[agent %s] workspace empty, performing checkout for %s @ %s\n",
				a.id[:8], spec.ProjectID, spec.CommitSHA)
			a.performDebugCheckout(ctx, jobWorkspace, spec.ProjectID, spec.CommitSHA)
		}
	}

	// Build Executor
	exec, err := executor.New(jobWorkspace,
		filepath.Join(jobWorkspace, ".forge", "logs"),
		a.cacheDir,
	)
	if err != nil {
		a.reportComplete(spec, 1, 0, nil, nil)
		return fmt.Errorf("creating executor: %w", err)
	}

	// Convert API Spec -> pipeline.Step
	step := &pipeline.Step{
		ID:           spec.StepID,
		Name:         spec.StepID,
		Image:        spec.Image,
		Command:      spec.Command,
		WorkDir:      spec.WorkDir,
		Env:          spec.Env,
		Inputs:       spec.Inputs,
		Timeout:      spec.Timeout,
		Secrets:      spec.SecretNames,
		DockerSocket: spec.DockerSocket,
		Type:         spec.Type,
	}

	/*
		Default WorkDir to /workspace if not set
		Policy-injected steps from transformers may often omit WorkDir since then
		transformer JSON doesn't know about Forge's defaults. Without this,
		Docker gets --workdir "" and fails before writing any log output.

		Thus, the default is /workspace to avoid this failure.
	*/
	if step.WorkDir == "" {
		step.WorkDir = "/workspace"
	}

	/*
		Inject FORGE_API_TOKEN, and FORGE_SCHEDULER_URL so steps
		can communicate with the scheduler as needed (e.g. for the injected checkout step).
	*/
	if step.Env == nil {
		step.Env = make(map[string]string)
	}
	step.Env["FORGE_API_TOKEN"] = a.apiToken
	step.Env["FORGE_SCHEDULER_URL"] = a.schedulerURL

	/*
		Fetch secrets from Vault

		Secrets are injected just-in-time here - never stored within the scheduler or passed through the job queue. The
		vault exists only in this function's stack frame and in the container's environment.
	*/
	if len(spec.SecretNames) > 0 {
		if a.vault == nil {
			return fmt.Errorf("step %q requires secrets %v but FORGE_VAULT_ADDR / FORGE_VAULT_TOKEN are not set",
				spec.StepID, spec.SecretNames)
		}
		if step.Env == nil {
			step.Env = make(map[string]string)
		}
		for _, name := range spec.SecretNames {
			val, err := a.vault.GetScoped(name, spec.OrgID, spec.ProjectID)
			if err != nil {
				return fmt.Errorf("fetching secret %q: %w", name, err)
			}

			// We inject the secret value using the same name as the secret
			step.Env[name] = val

			/*
				Register the value for log redaction so it never appears in the log output
				even if the step accidentally echos it.
			*/
			step.RedactValues = append(step.RedactValues, val)
		}
		scopeDesc := "global"
		if spec.ProjectID != "" {
			scopeDesc = fmt.Sprintf("project %s", spec.ProjectID)
		} else if spec.OrgID != "" {
			scopeDesc = fmt.Sprintf("org %s", spec.OrgID)
		}
		fmt.Printf("[agent %s] fetched %d secret(s) for step %s (scope: %s)\n",
			a.id[:8], len(spec.SecretNames), spec.StepID, scopeDesc)
	}

	// Check CAS before running
	if a.cacheDir != "" && len(step.Inputs) > 0 {
		cas, err := cache.New(a.cacheDir)
		if err == nil {
			hash, err := cache.ComputeTaskHash(step, jobWorkspace)
			if err == nil {
				step.CacheKey = hash
				if entry, hit := cas.Lookup(hash); hit {
					fmt.Printf("[agent %s] cache hit for step %s\n",
						a.id[:8], step.ID)
					return a.reportComplete(spec, entry.ExitCode, 0, cacheHitLog(hash), nil)
				}
			}
		}
	}

	// Download artifacts declared by this step
	if len(spec.ArtifactDownloads) > 0 {
		if err := a.downloadArtifacts(spec, jobWorkspace); err != nil {
			fmt.Printf("[agent %s] artifact download failed: %v\n", a.id[:8], err)
			return a.reportComplete(spec, 1, 0, []api.LogEvent{{
				Timestamp: time.Now(),
				Level:     "ERROR",
				Message:   fmt.Sprintf("artifact download failed: %v", err),
			}}, nil)
		}
	}

	// Run the step
	start := time.Now()
	stepCtx, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()

	/*
		Set up real-time log streaming
		A buffered channel decouples the executor's log writes from the
		HTTP POST to the scheduler - the scanner never blocks.
	*/
	logCh := make(chan api.LogEvent, 256)

	exec.StreamCallback = func(stepID string, ts time.Time, level, message string) {
		select {
		case logCh <- api.LogEvent{Timestamp: ts, Level: level, Message: message}:
		default:
		}
	}

	// Streaming goroutine: batches events and POSTs them every 500ms.
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		a.streamJobLogs(spec.JobID, spec.LeaseID, logCh)
	}()

	result, err := exec.RunStep(stepCtx, step)
	elapsed := time.Since(start)

	// Signal the streaming goroutine to flush and exit
	close(logCh)
	<-streamDone

	exitCode := 0
	if err != nil {
		exitCode = 1
	} else if result != nil {
		exitCode = result.ExitCode
	}

	// Read log events to forward to the scheduler.
	var logEvents []api.LogEvent
	if err != nil {
		/*
				Hard error from the executor - e.g. Docker failed to start the container due to workdir being set to "".
			    The result is nil, so there's no log file. We'll sythesize a log event so the user sees what went wrong
			    in the UI rather than "no logs stored for this job"
		*/
		logEvents = []api.LogEvent{{
			Timestamp: time.Now(),
			Level:     "ERROR",
			Message:   fmt.Sprintf("executor error: %v", err),
		}}
	} else if result != nil && result.CacheHit {
		logEvents = cacheHitLog(result.Step.CacheKey)
	} else if result != nil && result.LogFile != "" {
		logEvents = readLogFile(result.LogFile)
	}
	if logEvents == nil {
		logEvents = []api.LogEvent{}
	}

	// For generator steps, parse the emitted step definitions from stdout.
	var emittedSteps []api.StepDef
	if result != nil && len(result.GeneratedStepsJSON) > 0 && exitCode == 0 {
		if err := json.Unmarshal(result.GeneratedStepsJSON, &emittedSteps); err != nil {
			fmt.Printf("[agent %s] failed to parse generator output: %v\n", a.id[:8], err)
		} else {
			fmt.Printf("[agent %s] generator emitting %d steps\n", a.id[:8], len(emittedSteps))
		}
	}

	// Upload artifacts declared by this step (only on success)
	if exitCode == 0 && len(spec.ArtifactUploads) > 0 {
		a.uploadArtifacts(spec, jobWorkspace)
	}

	return a.reportComplete(spec, exitCode, elapsed.Milliseconds(), logEvents, emittedSteps)
}

// heartbeatLoop sends heartbeats every heartbeatInterval until done is closed.
// Runs as a goroutine alongside the executing job.
func (a *Agent) heartbeatLoop(jobID, leaseID string, done <-chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := a.heartbeat(jobID, leaseID); err != nil {
				fmt.Printf("[agent %s] heartbeat failed: %v\n", a.id[:8], err)
				return
			}
		case <-done:
			return // job finished - stop heartbeat
		}
	}
}

// lease asks the scheduler for the next available job.
func (a *Agent) lease(ctx context.Context) (*api.JobSpec, bool, error) {
	body, _ := json.Marshal(api.LeaseRequest{AgentID: a.id})

	req, _ := http.NewRequestWithContext(ctx, "POST",
		a.schedulerURL+"/api/v1/jobs/lease",
		bytes.NewReader(body),
	)
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	// 204 - queue is empty
	if resp.StatusCode == http.StatusNoContent {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("lease: unexpected status %d", resp.StatusCode)
	}

	var leaseResp api.LeaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&leaseResp); err != nil {
		return nil, false, fmt.Errorf("decoding lease response: %w", err)
	}
	return leaseResp.Job, true, nil
}

// heartbeat notifies the scheduler that this agent is still alive.
func (a *Agent) heartbeat(jobID, leaseID string) error {
	body, _ := json.Marshal(api.HeartbeatRequest{
		LeaseID: leaseID,
		AgentID: a.id,
	})

	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/jobs/%s/heartbeat", a.schedulerURL, jobID),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // Drain body to allow connection reuse.

	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("lease reclaimed by scheduler")
	}
	return nil
}

// reportComplete sends the job result to the scheduler.
func (a *Agent) reportComplete(spec *api.JobSpec, exitCode int, durationMs int64, logs []api.LogEvent, emittedSteps []api.StepDef) error {
	body, _ := json.Marshal(api.CompleteRequest{
		LeaseID:      spec.LeaseID,
		AgentID:      a.id,
		ExitCode:     exitCode,
		Duration:     durationMs,
		LogEvents:    logs,
		EmittedSteps: emittedSteps,
	})

	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/jobs/%s/complete", a.schedulerURL, spec.JobID),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("reporting completion: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

// sleep waits for d duration but returns early if ctx is canceled.
// This is preferred due to time.Sleep() not allowing interruption by context cancellation.
func (a *Agent) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// cacheHitLog returns a single synthetic log event for a cache hit.
// This is what the UI panel shows when a step is skipped because its inputs haven't changed.
func cacheHitLog(taskHash string) []api.LogEvent {
	short := taskHash
	if len(short) > 12 {
		short = short[:12]
	}
	return []api.LogEvent{{
		Timestamp: time.Now(),
		Level:     "INFO",
		Message:   fmt.Sprintf("◎ cache hit — step skipped (hash: %s...)", short),
	}}
}

// readLogFile reads a JSONL log file written by the executor and converts
// each line into an api.LogEvent for forwarding to the scheduler.
//
// JSONL (JSON Lines) means one complete JSON object per line. This is far easier to parse.
//
// Errors on individual lines are silently skipped rather than aborting
// a partial log is better than no log if the step crashed mid-write
func readLogFile(path string) []api.LogEvent {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	// A struct that matches only the fields we care about from the
	// logger's Event type. We don't need to import the log package —
	// just declare what we want to pull out.
	type rawEvent struct {
		Timestamp string `json:"ts"`
		Level     string `json:"level"`
		Message   string `json:"message"`
	}

	var events []api.LogEvent
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var raw rawEvent
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // Skip malformed lines (could be due to crash)
		}
		ts, err := time.Parse(time.RFC3339Nano, raw.Timestamp)
		if err != nil {
			ts = time.Now()
		}
		events = append(events, api.LogEvent{
			Timestamp: ts,
			Level:     raw.Level,
			Message:   raw.Message,
		})
	}
	return events
}

// debugLoop polls for debug sessions and handles them concurrently with jobs.
func (a *Agent) debugLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		spec, ok, err := a.leaseDebug(ctx)
		if err != nil || !ok {
			a.sleep(ctx, 2*time.Second)
			continue
		}
		fmt.Printf("[agent %s] starting debug container for session %s\n",
			a.id[:8], spec.SessionID[:8])
		go a.handleDebugSession(ctx, spec)
	}
}

// handleDebugSession starts a debug container and relays commands until closed.
func (a *Agent) handleDebugSession(ctx context.Context, spec *api.DebugJobSpec) {
	// Always use an isolated workspace for each debug session to avoid Bug 2.
	workspaceDir := filepath.Join(a.workspaceDir, "forge-debug-"+spec.SessionID)
	os.MkdirAll(workspaceDir, 0755)

	// Bug 1: If the workspace is empty and we have repo info, perform a checkout.
	if spec.ProjectID != "" && spec.CommitSHA != "" {
		files, _ := os.ReadDir(workspaceDir)
		if len(files) == 0 {
			fmt.Printf("[agent %s] workspace empty, performing checkout for %s @ %s\n",
				a.id[:8], spec.ProjectID[:8], spec.CommitSHA[:8])
			a.performDebugCheckout(ctx, workspaceDir, spec.ProjectID, spec.CommitSHA)
		}
	}

	args := []string{
		"run", "--rm", "-d",
		"--label", "forge.managed=true",
		"--label", "forge.debug=true",
		"--workdir", spec.WorkDir,
		"--volume", workspaceDir + ":/workspace:rw",
	}
	if spec.DockerSocket {
		hostSocket := "/var/run/docker.sock"
		if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
			hostSocket = strings.TrimPrefix(h, "unix://")
		} else if runtime.GOOS == "windows" {
			hostSocket = `\\.\pipe\docker_engine`
		}
		args = append(args, "--volume", hostSocket+":/var/run/docker.sock")
		args = append(args, "-e", "DOCKER_HOST=unix:///var/run/docker.sock")
	}
	if spec.WorkDir == "" {
		args[3] = "/workspace"
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, spec.Image, "tail", "-f", "/dev/null")

	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		fmt.Printf("[agent %s] debug container failed to start: %v\n", a.id[:8], err)
		return
	}
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		return
	}

	defer func() {
		exec.Command("docker", "stop", containerID).Run()
		a.debugConts.Delete(spec.SessionID)
		fmt.Printf("[agent %s] debug container %s stopped\n", a.id[:8], containerID[:12])
	}()

	/*
			Install util-linux on Alpine images - it provides the `script` command which
		    handleTerminalWS uses to allocate a PTY inside the container.
			Runs before we register with the scheduler, so the browser only gets the
		   `ready` signal once `script` is available.
			On non-alpine images `apk` doesn't exist -> exits non-zero -> || true no-ops.
	*/
	fmt.Printf("[agent %s] preparing terminal tools in %s…\n", a.id[:8], containerID[:12])
	exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c",
		"apk add -q --no-cache util-linux >/dev/null 2>&1 || true",
	).Run()

	// Store container ID so the WS terminal handler can look it up be session.
	a.debugConts.Store(spec.SessionID, containerID)

	/*
			Register the INTERNAL address the scheduler uses to proxy WebSocket connections.
		    The browser connects to the scheduler at /api/v1/debug/{id}/ws;
		    the scheduler dials this internal URL and bridges the two connections.
		    We leverage the agent's Docker-network IP so it works with any number of scaled replicas.
	*/
	wsPort := a.wsPort()
	internalIP := agentInternalIP()
	terminalURL := fmt.Sprintf("ws://%s:%s/debug/%s/ws", internalIP, wsPort, spec.SessionID)
	if err := a.registerDebugContainer(spec.SessionID, containerID, terminalURL); err != nil {
		fmt.Printf("[agent %s] failed to register debug container: %v\n", a.id[:8], err)
		return
	}
	fmt.Printf("[agent %s] debug container ready: %s  ws: %s\n",
		a.id[:8], containerID[:12], terminalURL)

	// cancelCmd holds the cancel function for the currently running command.
	// The cancel-poll goroutine calls it when the browser requests a cancel.
	var (
		cancelMu  sync.Mutex
		cancelCmd context.CancelFunc
	)

	/*
	   This is an important goroutine: polls the cancel-check endpoint every 500ms.
	   uses a SEPARATE endpoint from pollDebugCommands so it never accidentally drains
	   the command queue.
	*/
	stopCancel := make(chan struct{})
	defer close(stopCancel)

	go func() {
		for {
			select {
			case <-stopCancel:
				return
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
				shouldCancel, sessionExists := a.checkDebugCancel(spec.SessionID)
				if !sessionExists {
					return
				}
				if shouldCancel {
					cancelMu.Lock()
					if cancelCmd != nil {
						cancelCmd()
					}
					cancelMu.Unlock()
				}
			}
		}
	}()

	// Execute commands sequentially.
	for {
		if ctx.Err() != nil {
			return
		}

		resp, err := a.pollDebugCommands(spec.SessionID)
		if err != nil {
			a.sleep(ctx, time.Second)
			continue
		}
		if resp.Closed {
			return
		}

		for _, cmd := range resp.Commands {

			cmdCtx, cancel := context.WithCancel(ctx)
			cancelMu.Lock()
			cancelCmd = cancel
			cancelMu.Unlock()

			a.execDebugCommand(cmdCtx, spec.SessionID, containerID, cmd)

			cancelMu.Lock()
			cancelCmd = nil
			cancelMu.Unlock()
			cancel()
		}

		if len(resp.Commands) == 0 {
			a.sleep(ctx, 500*time.Millisecond)
		}
	}
}

// execDebugCommand runs a single command in the debug container via docker exec.
// Output is streamed line-by-line so the user sees it in real-time.
// The ctx can be cancelled to kill the command (used by the cancel button).
func (a *Agent) execDebugCommand(ctx context.Context, sessionID, containerID string, cmd api.DebugCommand) {
	execCmd := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", cmd.Input)

	pr, pw, err := os.Pipe()
	if err != nil {
		a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
			CommandID: cmd.CommandID, Output: "error: " + err.Error() + "\n", ExitCode: 1,
		})
		return
	}
	execCmd.Stdout = pw
	execCmd.Stderr = pw

	if err := execCmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
			CommandID: cmd.CommandID, Output: "error starting: " + err.Error() + "\n", ExitCode: 1,
		})
		return
	}

	// Stream output line-by-line while the command runs.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
				CommandID: cmd.CommandID,
				Output:    scanner.Text() + "\n",
				ExitCode:  -1, // -1 = streaming, final exit code sent below
			})
		}
	}()

	exitCode := 0
	if err := execCmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	pw.Close()
	<-scanDone
	pr.Close()

	// Final event: empty output, real exit code - signals completion to the browser.
	a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
		CommandID: cmd.CommandID, Output: "", ExitCode: exitCode,
	})
}

func (a *Agent) leaseDebug(ctx context.Context) (*api.DebugJobSpec, bool, error) {
	body, _ := json.Marshal(api.LeaseRequest{AgentID: a.id})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		a.schedulerURL+"/api/v1/debug/lease", bytes.NewReader(body))
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("debug lease: status %d", resp.StatusCode)
	}
	var spec api.DebugJobSpec
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		return nil, false, err
	}
	return &spec, true, nil
}

func (a *Agent) registerDebugContainer(sessionID, containerID, terminalURL string) error {
	body, _ := json.Marshal(api.RegisterContainerRequest{
		SessionID:     sessionID,
		ContainerID:   containerID,
		AgentID:       a.id,
		TerminalWsURL: terminalURL,
	})
	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/debug/%s/container", a.schedulerURL, sessionID),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (a *Agent) checkDebugCancel(sessionID string) (shouldCancel bool, sessionExists bool) {
	resp, err := a.authGet(
		fmt.Sprintf("%s/api/v1/debug/%s/cancel-check", a.schedulerURL, sessionID))
	if err != nil {
		return false, true
	}
	defer resp.Body.Close()
	var result struct {
		Cancel bool `json:"cancel"`
		Closed bool `json:"closed"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Cancel, !result.Closed
}

func (a *Agent) pollDebugCommands(sessionID string) (*api.PollCommandsResponse, error) {
	resp, err := a.authGet(
		fmt.Sprintf("%s/api/v1/debug/%s/commands", a.schedulerURL, sessionID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result api.PollCommandsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *Agent) submitDebugOutput(sessionID string, req api.SubmitOutputRequest) {
	body, _ := json.Marshal(req)
	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/debug/%s/output", a.schedulerURL, sessionID),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

// streamJobLogs reads log events from ch and POSTs them to the scheduler
// in batches - either every 500ms or every 50 events, whichever comes first.
//
// The batching avoids hammering the scheduler with one HTTP request per line
// while still keeping latency low enough that the browser feels real-time.
func (a *Agent) streamJobLogs(jobID, leaseID string, ch <-chan api.LogEvent) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var buf []api.LogEvent

	flush := func() {
		if len(buf) == 0 {
			return
		}
		a.postLogBatch(jobID, leaseID, buf)
		buf = buf[:0]
	}

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				// Channel closed - step finished. Flush remaining events.
				flush()
				return
			}
			buf = append(buf, e)
			if len(buf) >= 50 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// authPost makes an authenticated POST to the scheduler.
func (a *Agent) authPost(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	return a.client.Do(req)
}

// authGet makes an authenticated GET to the scheduler.
func (a *Agent) authGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	return a.client.Do(req)
}

// rebaseURL replaces the schema+host of a URL with the agent's known scheduler address
// - but ONLY when the URL points at the scheduler itself (detected by comparing paths, not hosts, since FORGE_BASE_URL
// may use "localhost").
//
// This is necessary because FORGE_BASE_URL may be "http://localhost:8080" for browser access, but
// agents run inside Docker where `localhost` resolves to the agent container, not the scheduler.
//
// URLs pointing sat S3/MinIO (pre-signed, different path prefix) are returned unchanged
// so they still hit the object store directly.
func (a *Agent) rebaseURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}

	base, err := url.Parse(a.schedulerURL)
	if err != nil {
		return rawURL
	}

	// Only rebase URLs whose path starts with /api/v1/ - those are
	// scheduler endpoints. Pre-signed S3/Minio URLs never have an /api/v1/ prefix
	if strings.HasPrefix(u.Path, "/api/v1/") {
		u.Scheme = base.Scheme
		u.Host = base.Host
		return u.String()
	}
	return rawURL
}

// agent's Websocket server. In Docker, this is the container's network IP.
// Falls back to 127.0.0.1 for local (non-containerized) development.
func agentInternalIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}
func (a *Agent) postLogBatch(jobID, leaseID string, events []api.LogEvent) {
	body, _ := json.Marshal(api.AppendLogsRequest{
		LeaseID: leaseID,
		Events:  events,
	})
	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/jobs/%s/logs", a.schedulerURL, jobID),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

// runTerminalServer starts an HTTP server that upgrades debug session connections
// to Websockets and connects them directly to a docker exec PTY.
// Browsers connect here directly, no scheduler in the hot path
func (a *Agent) runTerminalServer(ctx context.Context) {
	listenAddr := ":" + a.wsPort()

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/{sessionID}/ws", a.handleTerminalWS)

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	fmt.Printf("[agent %s] terminal server listening on %s (internal only)\n",
		a.id[:8], listenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Printf("[agent %s] terminal server error: %v\n", a.id[:8], err)
	}
}

var wsUpgrader = websocket.Upgrader{

	// Allow all origins for now, will need to address this later.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleTerminalWS upgrades the connection to WebSocket and bridges it to
// a terminal session on the debug container.
//
// Why use `script` inside the container instead of `docker exec -it`
//
// `docker exec -it` allocates a PTY but requires the CALLING process to have a real console/TTY.
// On Windows with Docker Desktop, when Go runs docker as a subprocess with piped stdin/stdout (which has no console),
// Docker cannot set up the ConPTY and input never reaches the container.
//
// With run `script` inside the container, `script` allocates a PTY entirely within the container's Linux kernel,
// independent of the host OS. Our pipe connects to script's stdin, script forwards it through the PTY to
// sh, and echo+output flows back through the same path. This should work on Windows, Linux, and Mac with no host-side
// PTY libraries.
func (a *Agent) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	// Verify that auth token
	if a.apiToken != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+a.apiToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	sessionID := r.PathValue("sessionID")

	cid, ok := a.debugConts.Load(sessionID)
	if !ok {
		http.Error(w, "debug session not found", http.StatusNotFound)
		return
	}
	containerID := cid.(string)

	// Parse initial terminal size from query params, set by xterm.js FitAddon.
	cols, rows := 220, 50
	if c := r.URL.Query().Get("cols"); c != "" {
		fmt.Sscanf(c, "%d", &cols)
	}
	if r2 := r.URL.Query().Get("rows"); r2 != "" {
		fmt.Sscanf(r2, "%d", &rows)
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("[agent] failed to upgrade WS: %v\n", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	shellCmd := fmt.Sprintf(
		`export TERM=xterm-256color COLUMNS=%d LINES=%d; `+
			`if command -v script >/dev/null 2>&1; then exec script -q -c "/bin/sh" /dev/null; `+
			`elif command -v python3 >/dev/null 2>&1; then exec python3 -c 'import pty; pty.spawn("/bin/sh")'; `+
			`elif command -v python >/dev/null 2>&1; then exec python -c 'import pty; pty.spawn("/bin/sh")'; `+
			`else exec sh -i; fi`,
		cols, rows,
	)

	/*
		Use `script` to allocate a PTY inside the container.

		`-i` (no `-t`): keeps stdin open, no host PTY required.
		`script -q -c "sh" /dev/null`: allocates /dev/pts/N inside the container
		   giving sh a real PTY with echo, readline, colors, CTRL+C, etc.
		Falls back to `python` PTY or `sh- i` if script isn't available.
	*/
	cmd := exec.CommandContext(ctx, "docker", "exec",
		"-i",
		"-e", fmt.Sprintf("COLUMNS=%d", cols),
		"-e", fmt.Sprintf("LINES=%d", rows),
		containerID,
		"sh", "-c", shellCmd,
	)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n\x1b[31mFailed to create stdout pipe: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	cmd.Stderr = cmd.Stdout // Merge stderr so Docker errors appear in terminal

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n\x1b[31mFailed to create stdin pipe: "+err.Error()+"\x1b[0m\r\n"))
		return
	}

	if err := cmd.Start(); err != nil {
		fmt.Printf("[agent] failed to start docker exec: %v\n", err)
		conn.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf("\r\n\x1b[31mFailed to exec into container: %v\x1b[0m\r\n", err)))
		return
	}
	defer cmd.Wait()

	fmt.Printf("[agent %s] terminal WS connected — session %s (%dx%d)\n",
		a.id[:8], sessionID[:8], cols, rows)

	// Goroutine: container output -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				if wErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); wErr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// Main loop: WebSocket input -> container stdin.
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Detect resize control message
		if len(msg) > 0 && msg[0] == '{' {
			var ctrl api.TerminalResizeMsg
			if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "resize" {
				/*
						Send stty resize command through stdin so it applies to the active PTY inside the container.
					    Run in a goroutine so it doesn't block the input loop.
				*/
				go func(c, r int) {
					stdinPipe.Write([]byte(fmt.Sprintf("stty cols %d rows %d\n", c, r)))
				}(ctrl.Cols, ctrl.Rows)
				continue
			}
		}

		if _, err := stdinPipe.Write(msg); err != nil {
			break
		}
	}

	cancel()
}

// downloadArtifacts fetches declared artifacts from the scheduler before the step runs.
func (a *Agent) downloadArtifacts(spec *api.JobSpec, workspaceDir string) error {
	for _, dl := range spec.ArtifactDownloads {
		meta, err := a.getArtifact(spec.RunID, dl.Name)
		if err != nil {
			return fmt.Errorf("artifact %q: %w", dl.Name, err)
		}

		/*
				Scheduler may return a download URL that uses FORGE_BASE_URL (e.g. http://localhost:8080).
			    Inside a Docker container, localhost refers to the agent itself, not the scheduler. Replace the
			    URL's host with the known scheduler address so the download always works from inside Docker.
		*/
		downloadURL := a.rebaseURL(meta.DownloadURL)

		dest := dl.Dest
		if dest == "" {
			dest = dl.Name
		}
		if !strings.HasPrefix(dest, "/") {
			dest = filepath.Join(workspaceDir, dest)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating download dir for %q: %w", dl.Name, err)
		}
		if err := a.downloadFile(downloadURL, dest); err != nil {
			return fmt.Errorf("downloading artifact %q: %w", dl.Name, err)
		}
		fmt.Printf("[agent %s] downloaded artifact %q → %s\n", a.id[:8], dl.Name, dest)
	}
	return nil
}

// uploadArtifacts stores declared artifacts after a step succeeds.
func (a *Agent) uploadArtifacts(spec *api.JobSpec, workspaceDir string) {
	for _, ul := range spec.ArtifactUploads {
		pattern := ul.Path
		if !strings.HasPrefix(pattern, "/") {
			pattern = filepath.Join(workspaceDir, pattern)
		}

		matches, err := filepath.Glob(pattern)

		if err != nil || len(matches) == 0 {
			fmt.Printf("[agent %s] artifact pattern %q matched no files\n", a.id[:8], ul.Path)
			continue
		}

		for _, filePath := range matches {
			name := ul.Name
			if name == "" {
				name = filepath.Base(filePath)
			}
			if err := a.uploadArtifact(spec.RunID, spec.JobID, name, filePath); err != nil {
				fmt.Printf("[agent %s] artifact upload %q failed: %v\n", a.id[:8], name, err)
			} else {
				fmt.Printf("[agent %s] uploaded artifact %q → %s\n", a.id[:8], name, filePath)
			}
		}
	}
}

func (a *Agent) getArtifact(runId, name string) (*api.ArtifactMeta, error) {
	url := fmt.Sprintf("%s/api/v1/artifacts?run_id=%s&name=%s", a.schedulerURL, runId, name)
	resp, err := a.authGet(url)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	var meta api.ArtifactMeta
	json.NewDecoder(resp.Body).Decode(&meta)
	return &meta, nil
}

func (a *Agent) uploadArtifact(runId, jobId, name, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}

	defer f.Close()

	info, _ := f.Stat()
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	// Step 1: Get presigned URL
	body, _ := json.Marshal(api.PresignUploadRequest{
		RunID:    runId,
		JobID:    jobId,
		Name:     name,
		Filename: filepath.Base(filePath),
	})

	presignResp, err := a.authPost(
		a.schedulerURL+"/api/v1/artifacts/presign",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("presign: %w", err)
	}

	defer presignResp.Body.Close()
	var presign api.PresignUploadResponse
	json.NewDecoder(presignResp.Body).Decode(&presign)

	if presign.ArtifactID == "" {
		return fmt.Errorf("invalid presign response")
	}

	/*
			Step 2: PUT the file to the upload URL.
		    For local backend: URL may use FORGE_BASE_URL (e.g. localhost:8080) which
		    isn't reachable from inside Docker -rebase to the agent's scheduler address.

		   For S3: URL is a real pre-signed S3 URL - rebaseURL returns it unchanged
		   because its host differs from the scheduler's host.
	*/
	uploadURL := a.rebaseURL(presign.UploadURL)
	req, err := http.NewRequest("PUT", uploadURL, f)
	if err != nil {
		return err
	}

	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	/*
			Only add Bearer auth for scheduler endpoints (local backend).
		    S3/Minio pre-signed URLs embed AWS SigV4 credentials in the URL itself
		    and most NOT receive additional Authorization headers.
	*/
	if u, err2 := url.Parse(uploadURL); err2 == nil && strings.HasPrefix(u.Path, "/api/v1/") {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	putResp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT upload: %w", err)
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent && putResp.StatusCode != http.StatusOK {
		return fmt.Errorf("artifact upload returned HTTP %d", putResp.StatusCode)
	}

	// Step 3: confirm (only needed for local backend; S3 confirms automatically).
	confirmBody, _ := json.Marshal(api.ConfirmUploadRequest{SizeBytes: size})
	confirmResp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/artifacts/%s/confirm", a.schedulerURL, presign.ArtifactID),
		"application/json",
		bytes.NewReader(confirmBody),
	)

	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}

	io.Copy(io.Discard, confirmResp.Body)
	confirmResp.Body.Close()
	return nil
}

func (a *Agent) performDebugCheckout(ctx context.Context, dir, projectID, commitSHA string) {
	url := fmt.Sprintf("%s/api/v1/source/%s?commit=%s", a.schedulerURL, projectID, commitSHA)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		fmt.Printf("[agent %s] checkout failed: %v\n", a.id[:8], err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.apiToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("[agent %s] checkout failed: %v\n", a.id[:8], err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[agent %s] checkout failed: status %d\n", a.id[:8], resp.StatusCode)
		return
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		fmt.Printf("[agent %s] checkout failed (gzip): %v\n", a.id[:8], err)
		return
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Printf("[agent %s] checkout failed (tar): %v\n", a.id[:8], err)
			return
		}

		target := filepath.Join(dir, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				fmt.Printf("[agent %s] checkout failed (file): %v\n", a.id[:8], err)
				return
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				fmt.Printf("[agent %s] checkout failed (copy): %v\n", a.id[:8], err)
				return
			}
			f.Close()
		}
	}
}

func (a *Agent) downloadFile(downloadURL, dest string) error {
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return err
	}

	// Add bearer auth for scheduler endpoints (local artifact backend).
	if u, err2 := url.Parse(downloadURL); err2 == nil && strings.HasPrefix(u.Path, "/api/v1/") {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func (a *Agent) executePipelineStep(ctx context.Context, spec *api.JobSpec) error {
	ref := spec.PipelineRef
	if ref == nil || ref.Path == "" {
		return a.reportComplete(spec, 1, 0, pipelineLog("ERROR", "pipeline step has no path"), nil)
	}

	start := time.Now()
	logs := []api.LogEvent{{
		Timestamp: time.Now(), Level: "INFO",
		Message: fmt.Sprintf("pipeline step: %s (wait=%v)", ref.Path, ref.Wait),
	}}

	// Build the full path to the referenced pipeline file.
	pipelinePath := ref.Path
	if !filepath.IsAbs(pipelinePath) {
		pipelinePath = filepath.Join(a.workspaceDir, pipelinePath)
	}

	// Compile the child pipeline.
	childPipeline, err := compiler.Compile(pipelinePath)
	if err != nil {
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("compile %s: %v", ref.Path, err))...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, nil)
	}
	logs = append(logs, pipelineLog("INFO", fmt.Sprintf("compiled child pipeline %q (%d steps)", childPipeline.Name, len(childPipeline.Steps)))...)

	steps := make([]api.StepDef, 0, len(childPipeline.Steps))
	for _, s := range childPipeline.Steps {
		env := make(map[string]string, len(s.Env)+len(ref.Variables))
		for k, v := range s.Env {
			env[k] = v
		}

		// Variables override the step's own env vars.
		for k, v := range ref.Variables {
			env[k] = v
		}
		steps = append(steps, api.StepDef{
			ID:          s.ID,
			Image:       s.Image,
			Command:     s.Command,
			WorkDir:     s.WorkDir,
			Env:         env,
			DependsOn:   s.DependsOn,
			Inputs:      s.Inputs,
			Timeout:     s.Timeout,
			SecretNames: s.Secrets,
			Type:        s.Type,
		})
	}

	// Submit the child run.
	childRunName := fmt.Sprintf("%s → %s", spec.StepID, childPipeline.Name)
	body, _ := json.Marshal(api.SubmitRunRequest{
		PipelineName: childRunName,
		Steps:        steps,
		WorkspaceDir: a.workspaceDir,
		OrgID:        spec.OrgID,
		ProjectID:    spec.ProjectID,
	})
	submitResp, err := a.authPost(a.schedulerURL+"/api/v1/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("submit child run: %v", err))...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, nil)
	}
	defer submitResp.Body.Close()

	var runResp api.SubmitRunResponse
	json.NewDecoder(submitResp.Body).Decode(&runResp)
	if runResp.RunID == "" {
		logs = append(logs, pipelineLog("ERROR", "scheduler returned empty run ID")...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, nil)
	}
	logs = append(logs, pipelineLog("INFO", fmt.Sprintf("child run submitted: %s", runResp.RunID[:8]))...)

	if !ref.Wait {
		logs = append(logs, pipelineLog("INFO", "fire-and-forget — not waiting for child run")...)
		return a.reportComplete(spec, 0, time.Since(start).Milliseconds(), logs, nil)
	}

	// Poll until the child run finishes.
	finalStatus, pollLogs := a.waitForChildRun(ctx, runResp.RunID)
	logs = append(logs, pollLogs...)

	exitCode := 0
	if finalStatus != "passed" {
		exitCode = 1
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("child run %s: %s", runResp.RunID[:8], finalStatus))...)
	} else {
		logs = append(logs, pipelineLog("INFO", fmt.Sprintf("child run %s: passed", runResp.RunID[:8]))...)
	}

	// Copy artifacts delcared in artifacts_receive from child run into parent run
	for _, name := range ref.ArtifactsReceive {
		if err := a.bridgeArtifact(ctx, runResp.RunID, spec.RunID, spec.JobID, name); err != nil {
			logs = append(logs, pipelineLog("WARN", fmt.Sprintf("artifact %q bridge failed: %v", name, err))...)
		} else {
			logs = append(logs, pipelineLog("INFO", fmt.Sprintf("artifact %q copied from child run", name))...)
		}
	}

	return a.reportComplete(spec, exitCode, time.Since(start).Milliseconds(), logs, nil)
}

// waitForChildRun polls the scheduler every 5 seconds until the run reaches a terminal state. Reruns the
// final status and accumulated log events.
func (a *Agent) waitForChildRun(ctx context.Context, runID string) (string, []api.LogEvent) {
	var logs []api.LogEvent
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "canceled", logs
		case <-ticker.C:
		}

		resp, err := a.authGet(fmt.Sprintf("%s/api/v1/runs/%s", a.schedulerURL, runID))
		if err != nil {
			continue
		}
		var status api.RunStatus
		json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()

		switch string(status.Status) {
		case "passed", "failed", "canceled":
			return string(status.Status), logs
		default:
			logs = append(logs, pipelineLog("INFO",
				fmt.Sprintf("child run %s: %s", runID[:8], status.Status))...)
		}
	}
}

// bridgeArtifact copies an artifact from a source run into the target run's artifact store.
// Used to propagate artifacts from a child pipeline back to the parent run.
func (a *Agent) bridgeArtifact(ctx context.Context, srcRunID, dstRunID, dstJobID, name string) error {
	// Download the artifact from the child run into a temp file.
	meta, err := a.getArtifact(srcRunID, name)
	if err != nil {
		return fmt.Errorf("get artifact %q from run %s: %w", name, srcRunID[:8], err)
	}

	tmp, err := os.CreateTemp("", "forge-artifact-bridge-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := a.downloadFile(meta.DownloadURL, tmp.Name()); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// Re-upload into the parent run under the same logical name.
	return a.uploadArtifact(dstRunID, dstJobID, name, tmp.Name())
}

func pipelineLog(level, msg string) []api.LogEvent {
	return []api.LogEvent{{Timestamp: time.Now(), Level: level, Message: "[pipeline] " + msg}}
}
