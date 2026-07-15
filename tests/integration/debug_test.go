package integration

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestDebugSession(t *testing.T) {
	// 1. Submit a failing pipeline
	runID := submitPipeline(t, adminClient, "debug-test", []stepDef{
		failStep("fail-me"),
	})
	status := waitForRun(t, adminClient, runID)
	assertFailed(t, status)

	jobID := ""
	for _, j := range status.Jobs {
		if j.StepID == "fail-me" {
			jobID = j.JobID
			break
		}
	}
	if jobID == "" {
		t.Fatal("failed to find job ID for fail-me step")
	}

	// 2. Create debug session
	resp, err := adminClient.post("/api/v1/debug", api.CreateDebugRequest{JobID: jobID})
	if err != nil {
		t.Fatalf("create debug session: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)
	var session api.DebugSessionInfo
	decode(t, resp, &session)

	// 3. Wait for session to be ready
	ready := false
	for i := 0; i < 30; i++ {
		resp, err = adminClient.get("/api/v1/debug/" + session.SessionID)
		if err == nil && resp.StatusCode == http.StatusOK {
			var info api.DebugSessionInfo
			decode(t, resp, &info)
			if info.Status == "ready" {
				ready = true
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	if !ready {
		t.Fatal("debug session did not become ready")
	}

	// 4. Subscribe to output stream (SSE)
	streamResp, err := adminClient.get("/api/v1/debug/" + session.SessionID + "/stream")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer streamResp.Body.Close()

	// 5. Exec a command
	execResp, err := adminClient.post("/api/v1/debug/"+session.SessionID+"/exec", map[string]string{"input": "echo hello-debug-reliability"})
	if err != nil {
		t.Fatalf("exec command: %v", err)
	}
	requireStatus(t, execResp, http.StatusOK)

	// 6. Verify output in SSE stream
	found := false
	scanner := bufio.NewScanner(streamResp.Body)
	timeout := time.After(10 * time.Second)

	for !found {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for output in SSE stream")
		default:
			if !scanner.Scan() {
				t.Fatal("SSE stream closed prematurely")
			}
			line := scanner.Text()
			if strings.Contains(line, "hello-debug-reliability") {
				found = true
			}
		}
	}

	t.Log("✓ debug session command output verified")
}

func TestDebugDockerAccess(t *testing.T) {
	// 1. Submit a failing pipeline that requires docker
	runID := submitPipeline(t, adminClient, "debug-docker-access", []stepDef{
		{
			ID:           "fail-docker",
			Image:        "docker:27-cli",
			DockerSocket: true,
			Run:          "docker ps && exit 1",
		},
	})
	status := waitForRun(t, adminClient, runID)
	assertFailed(t, status)

	jobID := ""
	for _, j := range status.Jobs {
		if j.StepID == "fail-docker" {
			jobID = j.JobID
			break
		}
	}
	if jobID == "" {
		t.Fatal("failed to find job ID for fail-docker step")
	}

	// 2. Create debug session
	resp, err := adminClient.post("/api/v1/debug", api.CreateDebugRequest{JobID: jobID})
	if err != nil {
		t.Fatalf("create debug session: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)
	var session api.DebugSessionInfo
	decode(t, resp, &session)

	// 3. Wait for session to be ready
	ready := false
	for i := 0; i < 30; i++ {
		resp, err = adminClient.get("/api/v1/debug/" + session.SessionID)
		if err == nil && resp.StatusCode == http.StatusOK {
			var info api.DebugSessionInfo
			decode(t, resp, &info)
			if info.Status == "ready" {
				ready = true
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	if !ready {
		t.Fatal("debug session did not become ready")
	}

	// 4. Subscribe to output stream (SSE)
	streamResp, err := adminClient.get("/api/v1/debug/" + session.SessionID + "/stream")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer streamResp.Body.Close()

	// 5. Exec 'docker ps' inside the debug session
	execResp, err := adminClient.post("/api/v1/debug/"+session.SessionID+"/exec", map[string]string{"input": "docker ps"})
	if err != nil {
		t.Fatalf("exec command: %v", err)
	}
	requireStatus(t, execResp, http.StatusOK)

	// 6. Verify output in SSE stream
	found := false
	scanner := bufio.NewScanner(streamResp.Body)
	timeout := time.After(20 * time.Second)

	for !found {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for 'docker ps' output in SSE stream")
		default:
			if !scanner.Scan() {
				t.Fatal("SSE stream closed prematurely")
			}
			line := scanner.Text()
			// 'docker ps' output usually contains 'CONTAINER ID'
			if strings.Contains(line, "CONTAINER ID") {
				found = true
			}
		}
	}

	t.Log("✓ debug session docker access verified")
}
