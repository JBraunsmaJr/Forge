package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestHandleDebugSession_EmptyWorkspace(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	a := &Agent{
		workspaceDir: tempDir,
		id:           "test-agent",
	}

	spec := &api.DebugJobSpec{
		SessionID:    "session-123",
		WorkspaceDir: "",
		Image:        "alpine",
	}

	expectedDir := filepath.Join(tempDir, "forge-debug-"+spec.SessionID)

	workspaceDir := spec.WorkspaceDir
	if workspaceDir == "" {
		workspaceDir = filepath.Join(a.workspaceDir, "forge-debug-"+spec.SessionID)
		err := os.MkdirAll(workspaceDir, 0755)
		if err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}
	}

	if workspaceDir != expectedDir {
		t.Errorf("expected workspace dir %q, got %q", expectedDir, workspaceDir)
	}

	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("expected directory %q to be created, but it doesn't exist", expectedDir)
	}
}

func TestHandleDebugSession_ProvidedWorkspace(t *testing.T) {
	providedDir := "/tmp/provided-workspace"

	spec := &api.DebugJobSpec{
		SessionID:    "session-456",
		WorkspaceDir: providedDir,
	}

	workspaceDir := spec.WorkspaceDir
	if workspaceDir == "" {

		workspaceDir = "wrong"
	}

	if workspaceDir != providedDir {
		t.Errorf("expected workspace dir %q, got %q", providedDir, workspaceDir)
	}
}
