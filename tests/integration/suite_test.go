package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	schedulerURL = "http://127.0.0.1:8080"
	adminToken   = "it-admin-token-forge"
	vaultURL     = "http://127.0.0.1:8200"
	vaultToken   = "forge-dev-token"
	startTimeout = 5 * time.Minute
	runTimeout   = 3 * time.Minute

	adminClient *client
)

var composeFiles []string

// TestMain starts the Forge stack before all tests and tears it down after.
func TestMain(m *testing.M) {
	if os.Getenv("INTEGRATION_SKIP") == "1" || os.Getenv("INTEGRATION_SKIP") == "true" {
		fmt.Println("[integration] skipped (INTEGRATION_SKIP set)")
		os.Exit(0)
	}

	if os.Getenv("FORGE_AGENT_ID") == "" {
		os.Setenv("FORGE_AGENT_ID", "local")
	}
	fmt.Printf("[integration] FORGE_AGENT_ID: %s\n", os.Getenv("FORGE_AGENT_ID"))

	initProjectName()
	fmt.Printf("[integration] COMPOSE_PROJECT_NAME: %s\n", composeProjectName)

	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Println("[integration] skipped (docker not found)")
		os.Exit(0)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	composeFiles = []string{
		filepath.Join(repoRoot, "compose.yml"),
		filepath.Join(repoRoot, "tests", "integration", "compose.test.yml"),
	}

	fmt.Println("[integration] starting Forge stack...")
	if err := startStack(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "[integration] failed to start stack: %v\n", err)
		dumpStatus(repoRoot)
		os.Exit(1)
	}
	fmt.Println("[integration] stack ready")

	// Best-effort safety net: if this process is interrupted/terminated
	// before m.Run() returns normally — e.g. because Forge's own
	// step-timeout killed the container this suite is running inside
	// (see the cmd.Cancel comment in internal/executor/executor.go) —
	// tear the compose stack down here instead of leaving it orphaned.
	// The normal teardown path below still runs when m.Run() returns on
	// its own; this only fires on an external kill.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Printf("[integration] received %v, tearing down stack before exit...\n", sig)
		stopStack(repoRoot)
		os.Exit(1)
	}()

	// Initialize the admin client after the stack is ready and ports are discovered.
	adminClient = newClient(adminToken)

	code := m.Run()
	if code != 0 {
		dumpStatus(repoRoot)
	}

	fmt.Println("[integration] tearing down stack...")
	stopStack(repoRoot)
	os.Exit(code)
}

func startStack(repoRoot string) error {
	fmt.Printf("[integration] FORGE_IMAGE: %s\n", os.Getenv("FORGE_IMAGE"))
	fmt.Printf("[integration] FORGE_AGENT_ID: %s\n", os.Getenv("FORGE_AGENT_ID"))
	fmt.Printf("[integration] FORGE_PROXY_AGENT_ID: %s\n", os.Getenv("FORGE_PROXY_AGENT_ID"))

	// Use dynamic ports for all services to avoid collisions on shared hosts.
	os.Setenv("FORGE_SCHEDULER_PORT", "0")
	os.Setenv("FORGE_SCHEDULER_GRPC_PORT", "0")
	os.Setenv("FORGE_VAULT_PORT", "0")
	os.Setenv("FORGE_MINIO_PORT", "0")
	os.Setenv("FORGE_MINIO_CONSOLE_PORT", "0")
	os.Setenv("FORGE_UI_PORT", "0")

	// compose.yml gates the autoscaler service behind this profile so it
	// doesn't start on a bare `docker compose up` (see the comment on
	// that service) — integration tests still need it active.
	os.Setenv("COMPOSE_PROFILES", "autoscaler")

	// Build the image once to avoid race conditions in Docker Compose when multiple
	// services share the same image and build context.
	fmt.Println("[integration] pre-building forge image...")
	if err := buildImage(repoRoot, os.Getenv("FORGE_IMAGE"), "Dockerfile"); err != nil {
		return fmt.Errorf("pre-building forge image: %w", err)
	}

	fmt.Println("[integration] pre-building autoscaler image...")
	if err := buildImage(repoRoot, os.Getenv("FORGE_AUTOSCALER_IMAGE"), "deployments/autoscaler/Dockerfile"); err != nil {
		return fmt.Errorf("pre-building autoscaler image: %w", err)
	}

	args := composeArgs("up", "-d")
	cmd := exec.Command("docker", args...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("[integration] docker compose up failed. Dumping stack status:")
		dumpStatus(repoRoot)
		return fmt.Errorf("docker compose up: %w", err)
	}

	host := dockerHostAddr()

	// Discover mapped ports
	sPort, err := getMappedPort(repoRoot, "scheduler", 8080)
	if err != nil {
		return fmt.Errorf("getting scheduler port: %w", err)
	}
	schedulerURL = "http://" + host + ":" + sPort
	fmt.Printf("[integration] scheduler discovered at %s\n", schedulerURL)

	vPort, err := getMappedPort(repoRoot, "vault", 8200)
	if err != nil {
		return fmt.Errorf("getting vault port: %w", err)
	}
	vaultURL = "http://" + host + ":" + vPort
	fmt.Printf("[integration] vault discovered at %s\n", vaultURL)

	fmt.Printf("[integration] waiting for scheduler at %s\n", schedulerURL)
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	dot := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	defer dot.Stop()

	httpClient := &http.Client{Timeout: 5 * time.Second}

	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			fmt.Println("[integration] TIMEOUT: Scheduler did not become ready. Dumping stack status:")
			dumpStatus(repoRoot)
			return fmt.Errorf("scheduler did not become ready within %s", startTimeout)
		case <-dot.C:
			fmt.Println("[integration] still waiting for scheduler...")
		case <-tick.C:
			resp, err := httpClient.Get(schedulerURL + "/")
			if err != nil {
				// Don't log every connection error to avoid spam, but we could log it occasionally.
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				fmt.Println("[integration] scheduler is ready")
				return waitForInit(repoRoot)
			}
			fmt.Printf("[integration] scheduler returned HTTP %d, still waiting...\n", resp.StatusCode)
		}
	}
}

// buildkitTransientExportErrors are substrings of a known, well-documented
// class of BuildKit bug (e.g. moby/buildkit#2041, #4793) where a build's
// final "exporting to image" step fails with "No such image" / "content
// digest ... not found" specifically under concurrent build load sharing
// the same BuildKit cache — exactly what happens here when multiple
// sharded test runs each run their own `docker build` around the same
// time. It's transient: retrying after a short pause routinely succeeds.
var buildkitTransientExportErrors = []string{
	"failed to export image",
	"No such image",
	"content digest",
}

// buildImage runs `docker build` for the given image and dockerfile, retrying
// a few times on the known transient BuildKit export race.
func buildImage(repoRoot, imageName, dockerfile string) error {
	const maxAttempts = 3
	var lastErr error
	var lastOutput string

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		buildCtx, buildCancel := context.WithTimeout(context.Background(), 15*time.Minute)
		buildCmd := exec.CommandContext(buildCtx, "docker", "build", "-t", imageName, "-f", dockerfile, ".")
		buildCmd.Dir = repoRoot

		var captured bytes.Buffer
		buildCmd.Stdout = io.MultiWriter(os.Stderr, &captured)
		buildCmd.Stderr = io.MultiWriter(os.Stderr, &captured)

		err := buildCmd.Run()
		buildCancel()
		if err == nil {
			return nil
		}

		lastErr = err
		lastOutput = captured.String()

		transient := false
		for _, sig := range buildkitTransientExportErrors {
			if strings.Contains(lastOutput, sig) {
				transient = true
				break
			}
		}
		if !transient || attempt == maxAttempts {
			break
		}

		wait := time.Duration(attempt) * 5 * time.Second
		fmt.Printf("[integration] build hit a known transient BuildKit export race (attempt %d/%d), retrying in %s...\n", attempt, maxAttempts, wait)
		time.Sleep(wait)
	}

	return lastErr
}

func getMappedPort(repoRoot, service string, port int) (string, error) {
	args := composeArgs("port", service, fmt.Sprintf("%d", port))
	cmd := exec.Command("docker", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	// Output format: 0.0.0.0:PORT or [::]:PORT
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected output from docker compose port: %q", s)
	}
	return parts[len(parts)-1], nil
}

func dumpStatus(repoRoot string) {
	fmt.Println("--- docker ps ---")
	cmd := exec.Command("docker", "ps", "-a")
	cmd.Stdout = os.Stderr
	cmd.Run()

	fmt.Println("--- docker compose ps ---")
	args := composeArgs("ps", "-a")
	cmd = exec.Command("docker", args...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Run()

	dumpLogs(repoRoot, "scheduler", 50)
	dumpLogs(repoRoot, "proxy", 50)
	dumpLogs(repoRoot, "agent", 100)
}

func dumpLogs(repoRoot, service string, tail int) {
	fmt.Printf("--- %s logs ---\n", service)
	args := composeArgs("logs", "--tail", fmt.Sprintf("%d", tail), service)
	cmd := exec.Command("docker", args...)
	cmd.Dir = repoRoot
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if strings.Contains(out.String(), "does not support reading") {
			fmt.Printf("[dumpLogs] skipping %s logs: logging driver does not support reading\n", service)
		} else {
			fmt.Printf("[dumpLogs] failed to get %s logs: %v\n%s\n", service, err, out.String())
		}
		return
	}
	fmt.Println(out.String())
}

func stopStack(repoRoot string) {
	// Clean up any sibling job containers that weren't part of the compose project.
	// We do this BEFORE compose down so that job containers are removed before
	// compose tries to remove the network they might be attached to.
	//
	// IMPORTANT: scope by this project's network, not just labels. The
	// forge.agent_id label is shared by every job running on the same agent,
	// so a label-only listing also matches *sibling test runs* (e.g. parallel
	// split shards) — and removing their forge.run_id containers killed them
	// mid-test. Only containers attached to OUR stack's network belong to us.
	fmt.Println("[integration] cleaning up dangling job containers...")
	// We use --format to get labels for filtering.
	out, _ := exec.Command("docker", "ps", "-a",
		"--filter", "label=forge.managed=true",
		"--filter", "network="+composeProjectName+"_forge",
		"--format", "{{.ID}}|{{.Labels}}").Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 && lines[0] != "" {
		self, _ := os.Hostname()
		var toRemove []string
		for _, line := range lines {
			parts := strings.Split(line, "|")
			if len(parts) < 2 {
				continue
			}
			id := parts[0]
			labels := parts[1]

			if self != "" && (strings.HasPrefix(self, id) || strings.HasPrefix(id, self)) {
				continue
			}
			// Only clean up job containers, policies, or autoscaler-spawned
			// agents — not the stack itself. Agents don't carry
			// forge.run_id/forge.policy (an agent isn't tied to one
			// specific job over its lifetime); they carry forge-pool
			// instead (see DockerFakeProvisioner.ScaleUp). Without this,
			// dynamically-spawned agents are invisible to `docker compose
			// down` below — they were never part of the compose project,
			// just raw `docker run` containers on this same network — and
			// are left orphaned after every test run that triggers any
			// autoscaling.
			if !strings.Contains(labels, "forge.run_id") &&
				!strings.Contains(labels, "forge.policy") &&
				!strings.Contains(labels, "forge-pool") {
				continue
			}
			toRemove = append(toRemove, id)
		}
		if len(toRemove) > 0 {
			rmArgs := append([]string{"rm", "-f"}, toRemove...)
			exec.Command("docker", rmArgs...).Run()
		}
	}

	downArgs := composeArgs("down", "-v", "--remove-orphans")
	cmd := exec.Command("docker", downArgs...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Run()
}

// composeProjectName is used as the Docker Compose project name for
// integration tests. This makes the Docker network name deterministic:
// the agent's FORGE_DOCKER_NETWORK is set to composeProjectName+"_forge"
// in compose.test.yml so job containers can always reach the stack.
var composeProjectName string

func initProjectName() {
	composeProjectName = "forge-it"
	// Always use a unique project name to avoid collisions and "poisoned" volumes from previous runs.
	// We include the agent ID and a timestamp to ensure uniqueness.
	agentID := os.Getenv("FORGE_AGENT_ID")
	if agentID == "" {
		agentID = "local"
	}
	jobID := os.Getenv("FORGE_JOB_ID")
	uniquePart := jobID
	if uniquePart == "" {
		uniquePart = "it"
	}

	// Use a more robust unique ID (timestamp in seconds + microsecond part)
	now := time.Now()
	composeProjectName = fmt.Sprintf("%s-%s-%d-%06d", agentID, uniquePart, now.Unix()%10000, now.UnixNano()%1000000)
	os.Setenv("COMPOSE_PROJECT_NAME", composeProjectName)
	// Use a unique image name for this test run to avoid build collisions on shared hosts.
	os.Setenv("FORGE_IMAGE", "forge-test:"+composeProjectName)
	os.Setenv("FORGE_AUTOSCALER_IMAGE", "forge-autoscaler-test:"+composeProjectName)
	// Ensure FORGE_PROXY_AGENT_ID is unique for this stack to avoid cross-talk
	// between parallel shards sharing the same host Docker daemon.
	os.Setenv("FORGE_PROXY_AGENT_ID", composeProjectName)
	// Proxy also needs a unique ID to avoid collisions in the host sockets volume.
	os.Setenv("FORGE_AGENT_ID", composeProjectName)
}

func composeArgs(sub ...string) []string {
	args := []string{"compose", "-p", composeProjectName}
	for _, f := range composeFiles {
		args = append(args, "-f", f)
	}
	return append(args, sub...)
}

type client struct {
	token string
	base  string
	http  *http.Client
}

func newClient(token string) *client {
	return &client{
		token: token,
		base:  schedulerURL,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *client) do(method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *client) get(path string) (*http.Response, error) { return c.do("GET", path, nil) }
func (c *client) post(path string, body any) (*http.Response, error) {
	return c.do("POST", path, body)
}
func (c *client) delete(path string) (*http.Response, error) { return c.do("DELETE", path, nil) }

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func requireStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected HTTP %d, got %d: %s", expected, resp.StatusCode, body)
	}
}

// stepDef is a minimal step definition for test pipelines.
// Field names and JSON tags must match api.StepDef exactly.
type stepDef struct {
	ID                string            `json:"id"`
	Image             string            `json:"image"`
	Run               string            `json:"run,omitempty"`
	Command           []string          `json:"command,omitempty"`
	DependsOn         []string          `json:"depends_on,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Type              string            `json:"type,omitempty"`
	ArtifactUploads   []uploadSpec      `json:"artifact_uploads,omitempty"`
	ArtifactDownloads []downloadSpec    `json:"artifact_downloads,omitempty"`
	Timeout           time.Duration     `json:"timeout_ns,omitempty"`
	DockerSocket      bool              `json:"docker_socket,omitempty"`
	AlwaysRun         bool              `json:"always_run,omitempty"`
	Condition         string            `json:"condition,omitempty"`
}

type uploadSpec struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type downloadSpec struct {
	Name string `json:"name"`
	Dest string `json:"dest"`
}

func submitPipeline(t *testing.T, c *client, name string, steps []stepDef) string {
	t.Helper()
	resp, err := c.post("/api/v1/runs", map[string]any{
		"pipeline_name": name,
		"steps":         steps,
		"workspace_dir": "/tmp",
	})
	if err != nil {
		t.Fatalf("submit pipeline: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)
	var result struct {
		RunID string `json:"run_id"`
	}
	decode(t, resp, &result)
	if result.RunID == "" {
		t.Fatal("empty run_id in response")
	}
	t.Logf("submitted run %s", result.RunID)
	return result.RunID
}

type runStatus struct {
	Status string `json:"status"`
	Jobs   []struct {
		JobID  string `json:"job_id"`
		StepID string `json:"step_id"`
		Status string `json:"status"`
	} `json:"jobs"`
}

func waitForRun(t *testing.T, c *client, runID string) runStatus {
	t.Helper()
	deadline := time.Now().Add(runTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		resp, err := c.get("/api/v1/runs/" + runID + "/detail")
		if err != nil {
			continue
		}
		var s runStatus
		decode(t, resp, &s)
		if s.Status == "passed" || s.Status == "failed" || s.Status == "canceled" {
			return s
		}
		t.Logf("run %s: %s", runID[:8], s.Status)
	}
	t.Fatalf("run %s did not complete within %s", runID, runTimeout)
	return runStatus{}
}

func assertPassed(t *testing.T, s runStatus) {
	t.Helper()
	if s.Status != "passed" {
		for _, j := range s.Jobs {
			if j.Status == "failed" || j.Status == "timed_out" || j.Status == "canceled" {
				resp, err := adminClient.get("/api/v1/jobs/" + j.JobID + "/logs")
				if err != nil {
					t.Errorf("Job %s (%s) failed but logs could not be fetched: %v", j.JobID, j.StepID, err)
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Errorf("Job %s (%s) status %q logs:\n%s", j.JobID, j.StepID, j.Status, body)
			}
		}
		t.Fatalf("expected run to pass, got status %q", s.Status)
	}
}

func assertFailed(t *testing.T, s runStatus) {
	t.Helper()
	if s.Status != "failed" {
		t.Errorf("expected run to fail, got status %q", s.Status)
	}
}

func getRun(t *testing.T, c *client, runID string) runStatus {
	t.Helper()
	resp, err := c.get("/api/v1/runs/" + runID + "/detail")
	if err != nil {
		t.Fatalf("get run detail: %v", err)
	}
	var s runStatus
	decode(t, resp, &s)
	return s
}

// echoStep returns a fast alpine step that just prints a message.
func echoStep(id, msg string, deps ...string) stepDef {
	return stepDef{
		ID:        id,
		Image:     "alpine:latest",
		Run:       fmt.Sprintf("echo '%s'", msg),
		DependsOn: deps,
		Timeout:   time.Minute * 2,
	}
}

// failStep returns a step that always exits non-zero.
func failStep(id string, deps ...string) stepDef {
	return stepDef{
		ID:        id,
		Image:     "alpine:latest",
		Run:       "exit 1",
		DependsOn: deps,
		Timeout:   time.Minute * 1,
	}
}

func createOrg(t *testing.T, c *client, name string) string {
	t.Helper()
	resp, err := c.post("/api/v1/orgs", map[string]string{"name": name})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}

	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()

		r2, _ := c.get("/api/v1/orgs")
		var orgs []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		decode(t, r2, &orgs)
		for _, o := range orgs {
			if o.Name == name {
				return o.ID
			}
		}
		t.Fatalf("org %q already exists but not found in list", name)
	}
	requireStatus(t, resp, http.StatusCreated)
	var org struct {
		ID string `json:"id"`
	}
	decode(t, resp, &org)
	return org.ID
}

// uniqueName generates a unique test resource name so parallel tests don't collide.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// allStepsIn returns true if every step in the run has the given status.
func allStepsIn(s runStatus, statuses ...string) bool {
	set := make(map[string]bool, len(statuses))
	for _, st := range statuses {
		set[st] = true
	}
	for _, j := range s.Jobs {
		if !set[j.Status] {
			return false
		}
	}
	return true
}

func setVaultSecret(t *testing.T, name, value string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"data": map[string]string{"value": value}})
	req, _ := http.NewRequest("POST",
		vaultURL+"/v1/secret/data/forge/global/"+name,
		bytes.NewReader(body))
	req.Header.Set("X-Vault-Token", vaultToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("set vault secret: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("vault set secret returned %d", resp.StatusCode)
	}
}

// stripPrefix removes a leading common prefix for cleaner test output.
func stripPrefix(s, prefix string) string {
	return strings.TrimPrefix(s, prefix)
}

func waitForInit(repoRoot string) error {
	fmt.Println("[integration] waiting for init service to complete...")
	name := composeProjectName + "-init-1"
	deadline := time.Now().Add(3 * time.Minute)
	var err error
	for time.Now().Before(deadline) {
		err = nil // reset for current duration
		cmd := exec.Command("docker", "inspect",
			"--format", "{{.State.Status}}:{{.State.ExitCode}}", name)
		cmd.Dir = repoRoot
		out, err := cmd.Output()
		if err == nil {
			s := strings.TrimSpace(string(out))
			if status, code, ok := strings.Cut(s, ":"); ok && status == "exited" {
				if code == "0" {
					fmt.Println("[integration] init service completed")
					return nil
				}
				fmt.Printf("[integration] init service failed (exit %s), check logs if tests fail\n", code)
				dumpLogs(repoRoot, "init", 50)
				return nil
			}
		}
		// err != nil: container not created yet, or not visible through the
		// proxy — keep polling until the deadline either way.
		time.Sleep(2 * time.Second)
	}
	fmt.Println("[integration] init service wait timed out; dumping init logs:")
	dumpLogs(repoRoot, "init", 50)
	return err
}

func dockerHostAddr() string {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return "127.0.0.1"
	}

	/*
		Inside a container talking to a sibling Docker daemon (DooD):
		127.0.0.1 is our own loopback, not the real host's use the default
		gateway instead which routes back to the host
	*/
	out, err := exec.Command("sh", "-c", "ip route show default | awk '{print $3}'").Output()
	if err == nil {
		if gw := strings.TrimSpace(string(out)); gw != "" {
			return gw
		}
	}

	return "127.0.0.1" // last resort fallback
}
