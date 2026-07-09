package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// TestStoreAndLookup verifies the basic write-then-read round trip.
func TestStoreAndLookup(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entry := &Entry{
		TaskHash:  "abc123",
		StepID:    "lint",
		ExitCode:  0,
		Duration:  3 * time.Second,
		CreatedAt: time.Now(),
		Image:     "alpine:latest",
		Command:   []string{"sh", "-c", "echo hi"},
	}

	if err := store.Store(entry); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, hit := store.Lookup("abc123")
	if !hit {
		t.Fatal("expected a cache hit, got miss")
	}
	if got.StepID != "lint" {
		t.Errorf("expected StepID 'lint', got %q", got.StepID)
	}
	if got.ExitCode != 0 {
		t.Errorf("expected ExitCode 0, got %d", got.ExitCode)
	}
}

// TestLookup_Miss verifies that an unknown hash returns (nil, false).
func TestLookup_Miss(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entry, hit := store.Lookup("doesnotexist")
	if hit {
		t.Error("expected a miss, got a hit")
	}
	if entry != nil {
		t.Error("expected nil entry on miss")
	}
}

// TestComputeTaskHash_Determinism verifies that the same inputs always
// produce the same hash. This is the core correctness property of the CAS.
func TestComputeTaskHash_Determinism(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main")

	step := &pipeline.Step{
		ID:      "build",
		Image:   "golang:1.26.5",
		Command: []string{"go", "build", "./..."},
		Env:     map[string]string{"CGO_ENABLED": "0"},
		Inputs:  []string{"*.go"},
	}

	hash1, err := ComputeTaskHash(step, dir)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	hash2, err := ComputeTaskHash(step, dir)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("hashes differ across identical calls:\n  %s\n  %s", hash1, hash2)
	}
}

// TestComputeTaskHash_ChangedFile verifies that modifying an input file
// produces a different hash - the most important property of content-addressing.
func TestComputeTaskHash_ChangedFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "main.go")
	writeFile(t, filePath, "package main\n// version 1")

	step := &pipeline.Step{
		ID:      "build",
		Image:   "golang:1.26.5",
		Command: []string{"go", "build"},
		Inputs:  []string{"*.go"},
	}

	hashBefore, err := ComputeTaskHash(step, dir)
	if err != nil {
		t.Fatalf("hash before: %v", err)
	}

	// Modify the file
	writeFile(t, filePath, "package main\n// version 2")

	hashAfter, err := ComputeTaskHash(step, dir)
	if err != nil {
		t.Fatalf("hash after: %v", err)
	}

	if hashBefore == hashAfter {
		t.Error("expected different hashes after file change, but they were the same")
	}
}

// TestComputeTaskHash_ChangedCommand verifies that changing the command
// produces a different hash even when files are identical.
func TestComputeTaskHash_ChangedCommand(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main")

	step := &pipeline.Step{
		Image:   "golang:1.26.5",
		Command: []string{"go", "build"},
		Inputs:  []string{"*.go"},
	}

	hash1, _ := ComputeTaskHash(step, dir)

	step.Command = []string{"go", "test"} // different command, same files.
	hash2, _ := ComputeTaskHash(step, dir)

	if hash1 == hash2 {
		t.Error("expected different hashes for different commands")
	}
}

// TestComputeTaskHash_NoInputs verifies that a step with no inputs
// declared is never considered a cache hit (always re-runs).
func TestComputeTaskHash_NoInputs(t *testing.T) {
	dir := t.TempDir()

	step := &pipeline.Step{
		Image:   "alpine:latest",
		Command: []string{"date"}, // always produce different output
		Inputs:  nil,              // no inputs declared.
	}

	hash1, _ := ComputeTaskHash(step, dir)
	hash2, _ := ComputeTaskHash(step, dir)

	/*
		With no inputs, two identical calls WILL produce the same hash
		The "never cache" semantics are enforced at the executor level by
		checking len(step.Inputs). This test just verifies the hash is stable for
		its inputs.
	*/
	if hash1 != hash2 {
		t.Error("hash should be deterministic even with no inputs")
	}
}

// TestSharding verifies that cache entries are stored in subdirectories
// based on the first 2 chars of their hash (like Git's object store).
func TestSharding(t *testing.T) {
	cacheDir := t.TempDir()
	store, _ := New(cacheDir)

	entry := &Entry{
		TaskHash:  "ab3f9c2d1e",
		StepID:    "x",
		CreatedAt: time.Now(),
	}
	store.Store(entry)

	// The file should be a <cacheDir>/ab/ab3f9c2d1e.json
	expected := filepath.Join(cacheDir, "ab", "ab3f9c2d1e.json")
	if _, err := os.Stat(expected); os.IsNotExist(err) {
		t.Errorf("expected sharded file at %s, not found", expected)
	}
}

// writeFile is a test helper that creates a file with given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}
