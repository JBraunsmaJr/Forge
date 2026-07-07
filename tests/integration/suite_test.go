// Package integration contains end-to-end tests for the Forge CI/CD system.
//
// Tests spin up the complete stack (postgres, vault, minio, scheduler, agent)
// using docker compose and interact with Forge entirely through its HTTP API —
// the same way a real user or CI system would.
//
// # Running locally
//
//	go test ./tests/integration/... -v -timeout=10m
//
// # Skipping without Docker
//
//	INTEGRATION_SKIP=true go test ./...
//
// # What gets tested
//
//   - Pipeline submission, DAG execution, pass/fail detection
//   - Artifact upload/download across steps
//   - Authentication (token required, wrong token rejected)
//   - Run cancellation
//   - Generator steps producing dynamic child jobs
//   - Webhook triggers and branch filtering
//   - Run retention / pruning
//   - Flaky step detection
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

// ── Suite configuration ───────────────────────────────────────────────────────

const (
	schedulerURL = "http://localhost:8080"
	adminToken   = "it-admin-token-forge"
	vaultURL     = "http://localhost:8200"
	vaultToken   = "forge-dev-token"
	startTimeout = 5 * time.Minute
	runTimeout   = 3 * time.Minute
)

var composeFiles []string

// TestMain starts the Forge stack before all tests and tears it down after.
func TestMain(m *testing.M) {
	if os.Getenv("INTEGRATION_SKIP") == "1" || os.Getenv("INTEGRATION_SKIP") == "true" {
		fmt.Println("[integration] skipped (INTEGRATION_SKIP set)")
		os.Exit(0)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Println("[integration] skipped (docker not found)")
		os.Exit(0)
	}

	// Resolve compose file paths relative to the repo root.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	composeFiles = []string{
		filepath.Join(repoRoot, "compose.yml"),
		filepath.Join(repoRoot, "tests", "integration", "compose.test.yml"),
	}

	fmt.Println("[integration] starting Forge stack...")
	if err := startStack(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "[integration] failed to start stack: %v\n", err)
		stopStack(repoRoot)
		os.Exit(1)
	}
	fmt.Println("[integration] stack ready")

	code := m.Run()

	fmt.Println("[integration] tearing down stack...")
	stopStack(repoRoot)
	os.Exit(code)
}

func startStack(repoRoot string) error {
	args := composeArgs("up", "--build", "-d")
	cmd := exec.Command("docker", args...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// Poll until the scheduler HTTP server is responding.
	// Print a dot every 5 seconds so the user can see progress.
	fmt.Printf("[integration] waiting for scheduler at %s", schedulerURL)
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	dot := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	defer dot.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return fmt.Errorf("scheduler did not become ready within %s — check: docker compose logs scheduler", startTimeout)
		case <-dot.C:
			fmt.Print(".")
		case <-tick.C:
			resp, err := http.Get(schedulerURL + "/")
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				fmt.Println(" ready")
				return nil
			}
		}
	}
}

func stopStack(repoRoot string) {
	args := composeArgs("down", "-v", "--remove-orphans")
	cmd := exec.Command("docker", args...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Run()
}

// composeProjectName is used as the Docker Compose project name for
// integration tests. This makes the Docker network name deterministic:
// the agent's FORGE_DOCKER_NETWORK is set to composeProjectName+"_forge"
// in compose.test.yml so job containers can always reach the stack.
const composeProjectName = "forge-it"

func composeArgs(sub ...string) []string {
	args := []string{"compose", "-p", composeProjectName}
	for _, f := range composeFiles {
		args = append(args, "-f", f)
	}
	return append(args, sub...)
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

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

// ── Pipeline helpers ──────────────────────────────────────────────────────────

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
	Timeout           string            `json:"timeout,omitempty"`
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
		t.Errorf("expected run to pass, got status %q", s.Status)
	}
}

func assertFailed(t *testing.T, s runStatus) {
	t.Helper()
	if s.Status != "failed" {
		t.Errorf("expected run to fail, got status %q", s.Status)
	}
}

// echoStep returns a fast alpine step that just prints a message.
func echoStep(id, msg string, deps ...string) stepDef {
	return stepDef{
		ID:        id,
		Image:     "alpine:latest",
		Run:       fmt.Sprintf("echo '%s'", msg),
		DependsOn: deps,
		Timeout:   "2m",
	}
}

// failStep returns a step that always exits non-zero.
func failStep(id string, deps ...string) stepDef {
	return stepDef{
		ID:        id,
		Image:     "alpine:latest",
		Run:       "exit 1",
		DependsOn: deps,
		Timeout:   "1m",
	}
}

// ── Org helpers ───────────────────────────────────────────────────────────────

func createOrg(t *testing.T, c *client, name string) string {
	t.Helper()
	resp, err := c.post("/api/v1/orgs", map[string]string{"name": name})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	// Org may already exist — both 201 and 409 are acceptable.
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		// Fetch the existing org by listing and filtering.
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

// ── Vault helper ──────────────────────────────────────────────────────────────

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
