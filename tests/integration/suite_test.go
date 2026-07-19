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
	"path/filepath"
	"runtime"
	"strings"
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

	// Initialize the admin client after the stack is ready and ports are discovered.
	adminClient = newClient(adminToken)

	code := m.Run()

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

	// Build the image once to avoid race conditions in Docker Compose when multiple
	// services share the same image and build context.
	fmt.Println("[integration] pre-building forge image...")
	buildCmd := exec.Command("docker", "build", "-t", os.Getenv("FORGE_IMAGE"), ".")
	buildCmd.Dir = repoRoot
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("pre-building forge image: %w", err)
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
				return nil
			}
			fmt.Printf("[integration] scheduler returned HTTP %d, still waiting...\n", resp.StatusCode)
		}
	}
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

	dumpLogs := func(service string, tail int) {
		fmt.Printf("--- %s logs ---\n", service)
		args := composeArgs("logs", "--tail", fmt.Sprintf("%d", tail), service)
		cmd := exec.Command("docker", args...)
		cmd.Dir = repoRoot
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			if strings.Contains(out.String(), "does not support reading") {
				fmt.Printf("[dumpStatus] skipping %s logs: logging driver does not support reading\n", service)
			} else {
				fmt.Printf("[dumpStatus] failed to get %s logs: %v\n%s\n", service, err, out.String())
			}
			return
		}
		fmt.Println(out.String())
	}

	dumpLogs("scheduler", 50)
	dumpLogs("proxy", 50)
	dumpLogs("agent", 100)
}

func stopStack(repoRoot string) {
	// Clean up any sibling job containers that weren't part of the compose project.
	// We do this BEFORE compose down so that job containers are removed before
	// compose tries to remove the network they might be attached to.
	fmt.Println("[integration] cleaning up dangling job containers...")
	// We use --format to get labels for filtering.
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "label=forge.managed=true", "--format", "{{.ID}}|{{.Labels}}").Output()
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
			// Only clean up job containers or policies, not the stack itself.
			if !strings.Contains(labels, "forge.run_id") && !strings.Contains(labels, "forge.policy") {
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
	// Use a more robust unique ID (timestamp in seconds + microsecond part)
	now := time.Now()
	composeProjectName = fmt.Sprintf("forge-it-%s-%d-%06d", agentID, now.Unix()%10000, now.UnixNano()%1000000)
	os.Setenv("COMPOSE_PROJECT_NAME", composeProjectName)
	// Use a unique image name for this test run to avoid build collisions on shared hosts.
	os.Setenv("FORGE_IMAGE", "forge-test:"+composeProjectName)
	// Ensure FORGE_PROXY_AGENT_ID is set for the internal stack to use for labels.
	if os.Getenv("FORGE_PROXY_AGENT_ID") == "" {
		os.Setenv("FORGE_PROXY_AGENT_ID", agentID)
	}
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
			if j.Status == "failed" {
				resp, err := adminClient.get("/api/v1/jobs/" + j.JobID + "/logs")
				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					t.Errorf("Job %s (%s) failed logs:\n%s", j.JobID, j.StepID, body)
				}
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
