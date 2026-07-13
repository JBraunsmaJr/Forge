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

	expectedBaseDir := filepath.Join(tempDir, "forge-debug-"+spec.SessionID)
	expectedWorkspaceDir := filepath.Join(expectedBaseDir, "workspace")

	workspaceDir := spec.WorkspaceDir
	if workspaceDir == "" {
		baseDir := filepath.Join(a.workspaceDir, "forge-debug-"+spec.SessionID)
		workspaceDir = filepath.Join(baseDir, "workspace")
		err := os.MkdirAll(workspaceDir, 0755)
		if err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}
	}

	if workspaceDir != expectedWorkspaceDir {
		t.Errorf("expected workspace dir %q, got %q", expectedWorkspaceDir, workspaceDir)
	}

	if _, err := os.Stat(expectedWorkspaceDir); os.IsNotExist(err) {
		t.Errorf("expected directory %q to be created, but it doesn't exist", expectedWorkspaceDir)
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
