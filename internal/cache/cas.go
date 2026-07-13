/*
		Package cache implements Forge's content-addressed store (CAS).

	 	Core idea:

			Every pipeline step gets a "task hash" - a SHA 256 digest computed from:
				- The container image name
				- The command and its arguments (sorted for determinism)
				- The environment variables (sorted)
				- The SHA-256 of every file matched by the step's `inputs` globs.

			If we've seen that exact hash before and it passed, we skip execution
			entirely and relay the cached result. No re-running work that hasn't changed.

			Forge's CAS caches the step output keyed by a hash of its true inputs.
			Your declare `inputs: ["src/**", "go.mod"]` and the system does the rest.
*/
package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/glob"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// Entry is what gets stored in the CAS for a successful step execution.
// It records enough information to replay the result without re-running
type Entry struct {
	TaskHash  string        `json:"task_hash"`
	StepID    string        `json:"step_id"`
	RunID     string        `json:"run_id"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration_ns"`
	CreatedAt time.Time     `json:"created_at"`

	// Image and Command are stored for human-readable debugging —
	// so you can open the JSON file and understand what was cached.
	Image   string   `json:"image"`
	Command []string `json:"command"`

	// ArtifactNames is the list of logical artifact names uploaded by this step.
	ArtifactNames []string `json:"artifact_names,omitempty"`
}

// Storer is the interface for content-addressed cache stores.
type Storer interface {
	Lookup(taskHash string) (*Entry, bool)
	Store(entry *Entry) error
}

// LocalStore is an on-disk implementation of Storer.
type LocalStore struct {
	dir string // root directory, typically .forge/cache
}

// NewLocal creates (or opens) a LocalStore rooted at dir.
func NewLocal(dir string) (*LocalStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating cache dir %s: %w", dir, err)
	}
	return &LocalStore{dir: dir}, nil
}

// Lookup checks whether a cached result exists for taskHash.
func (s *LocalStore) Lookup(taskHash string) (*Entry, bool) {
	path := s.entryPath(taskHash)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false
		}
		return nil, false
	}

	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	return &entry, true
}

func (s *LocalStore) Store(entry *Entry) error {
	path := s.entryPath(entry.TaskHash)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating shard dir: %w", err)
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling cache entry: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing cache entry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("committing cache entry: %w", err)
	}
	return nil
}

func (s *LocalStore) entryPath(taskHash string) string {
	if len(taskHash) < 2 {
		return filepath.Join(s.dir, "misc", taskHash+".json")
	}
	return filepath.Join(s.dir, taskHash[:2], taskHash+".json")
}

// RemoteStore is a distributed implementation of Storer that communicates
// with the Forge scheduler.
type RemoteStore struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewRemote creates a RemoteStore.
func NewRemote(baseURL, token string) *RemoteStore {
	return &RemoteStore{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *RemoteStore) Lookup(taskHash string) (*Entry, bool) {
	req, _ := http.NewRequest("GET", s.baseURL+"/api/v1/cache/"+taskHash, nil)
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false
	}

	var entry Entry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return nil, false
	}
	return &entry, true
}

func (s *RemoteStore) Store(entry *Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	req, _ := http.NewRequest("POST", s.baseURL+"/api/v1/cache", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}

// ComputeTaskHash calculates the content hash for a step.
//
// The hash is a SHA-256 over a deterministic serialization of:
//   - The container image
//   - The command (joined)
//   - The environment variables (sorted by key, then value)
//   - The SHA-256 of every file matched by the step's inputs globs.
//
// Two steps with identical inputs will always produce the same hash.
// One changed file in the inputs will produce a completely different hash.
//
// workspaceDir is the project root - needed to resolve the inputs globs.
func ComputeTaskHash(step *pipeline.Step, workspaceDir string) (string, error) {
	/*
		C# dev note here (since I'm primarily a dotnet dev)
		this is similar to a StringWriter

		We are essentially appending strings into the buffer, to then perform
		a hash at the end via `Sum(nil)` instead of `ToString()`
	*/
	h := sha256.New()

	fmt.Fprintf(h, "image:%s\n", step.Image)

	fmt.Fprintf(h, "command:%s\n", strings.Join(step.Command, "\x00"))

	envKeys := make([]string, 0, len(step.Env))
	for k := range step.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(h, "env:%s=%s\n", k, step.Env[k])
	}

	// Hash each input file
	if err := hashInputFiles(h, step.Inputs, workspaceDir); err != nil {
		return "", fmt.Errorf("hashing inputs: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashInputFiles resolves glob patterns relative to workspaceDir,
// sorts the matched paths for determinism, then hashes each file's contents.
//
// h is the running SHA-256 hasher - we write into it rather than returning a separate
// hash, because we want all inputs to contribute to one digest.
func hashInputFiles(h io.Writer, globs []string, workspaceDir string) error {
	if len(globs) == 0 {
		// No inputs declared -> step is never cached (always re-runs).
		// Signal this by writing a sentinel so the hash is always unique.
		fmt.Fprintf(h, "inputs:none\n")
		return nil
	}

	// Collect all matched paths.
	var paths []string
	for _, pattern := range globs {
		matched, err := glob.Glob(workspaceDir, pattern)
		if err != nil {
			return fmt.Errorf("glob %q: %w", pattern, err)
		}
		paths = append(paths, matched...)
	}

	paths = dedup(paths)

	sort.Strings(paths)

	for _, p := range paths {
		// Make the path relative so the hash is the same regardless of where on disk the workspace lives.
		rel, err := filepath.Rel(workspaceDir, p)
		if err != nil {
			return err
		}

		f, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("opening input file %s: %w", rel, err)
		}

		fileHash := sha256.New()
		if _, err := io.Copy(fileHash, f); err != nil {
			f.Close()
			return fmt.Errorf("hashing file %s: %w", rel, err)
		}
		f.Close()

		fmt.Fprintf(h, "file:%s:%s\n", rel, hex.EncodeToString(fileHash.Sum(nil)))
	}

	return nil
}

// dedup removes duplicate strings from a slice.
// Uses a map as a set
func dedup(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, exists := seen[s]; !exists {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
