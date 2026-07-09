package scheduler

import (
	"fmt"
	"sync"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

const debugSessionTTL = 15 * time.Minute

// debugSession holds state for one interactive debug container
type debugSession struct {
	id           string
	jobID        string
	image        string
	workDir      string
	env          map[string]string
	workspaceDir string
	projectID    string
	commitSHA    string
	dockerSocket bool

	status        string
	agentID       string
	containerID   string
	terminalWsURL string // direct WS URL: ws://agent-host:port/debug/{id}/ws

	pendingCmds     []api.DebugCommand
	outputs         []api.DebugOutput
	subscribers     map[chan string]struct{}
	cancelRequested bool

	createdAt time.Time
	expiresAt time.Time
}

// DebugStore manages all active debug sessions.
type DebugStore struct {
	mu       sync.Mutex
	sessions map[string]*debugSession
}

func newDebugStore() *DebugStore {
	return &DebugStore{sessions: make(map[string]*debugSession)}
}

// CreateSession opens a new debug session for a failed job. The caller provides
// the job's spec (image, env, workspace) so the agent can recreate the exact environment.
func (d *DebugStore) CreateSession(jobID, image, workDir string,
	env map[string]string, workspaceDir, projectID, commitSHA string, dockerSocket bool) *api.DebugSessionInfo {

	d.mu.Lock()
	defer d.mu.Unlock()

	id := newID()
	now := time.Now()
	s := &debugSession{
		id:           id,
		jobID:        jobID,
		image:        image,
		workDir:      workDir,
		env:          env,
		workspaceDir: workspaceDir,
		projectID:    projectID,
		commitSHA:    commitSHA,
		dockerSocket: dockerSocket,
		status:       "starting",
		subscribers:  make(map[chan string]struct{}),
		createdAt:    now,
		expiresAt:    now.Add(debugSessionTTL),
	}
	d.sessions[id] = s

	return d.info(s)
}

// LeaseNext returns the next pending debug session for an agent to handle.
// Returns (nil, false) if no sessions are waiting.
func (d *DebugStore) LeaseNext(agentID string) (*api.DebugJobSpec, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, s := range d.sessions {
		if s.status != "starting" || s.agentID != "" {
			continue
		}
		s.agentID = agentID
		return &api.DebugJobSpec{
			SessionID:    s.id,
			Image:        s.image,
			WorkDir:      s.workDir,
			Env:          s.env,
			WorkspaceDir: s.workspaceDir,
			ProjectID:    s.projectID,
			CommitSHA:    s.commitSHA,
			DockerSocket: s.dockerSocket,
		}, true
	}
	return nil, false
}

// RegisterContainer is called by the agent once the debug container is running.
// terminalURL is the direct WebSocket URL browsers use to connect to the terminal
func (d *DebugStore) RegisterContainer(sessionID, containerID, agentID, terminalURL string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.containerID = containerID
	s.terminalWsURL = terminalURL
	s.status = "ready"
	d.broadcast(s, fmt.Sprintf(
		`{"type":"ready","container_id":%q,"terminal_ws_url":%q}`,
		containerID, terminalURL,
	))
	return nil
}

// ExecCommand queues a command from the browser for the agent to execute.
// Also touches the session TTL - activity keeps the session active.
func (d *DebugStore) ExecCommand(sessionID, input string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return "", fmt.Errorf("session %s not found", sessionID)
	}
	if s.status != "ready" {
		return "", fmt.Errorf("session is %s, not ready", s.status)
	}

	// Reset the TTL on every command - the session lives as long as the user is actively using it.
	s.expiresAt = time.Now().Add(debugSessionTTL)
	remaining := int(debugSessionTTL.Seconds())
	d.broadcast(s, fmt.Sprintf(`{"type":"ttl","expires_in_s":%d}`, remaining))

	cmdID := newID()[:8]
	s.pendingCmds = append(s.pendingCmds, api.DebugCommand{
		CommandID: cmdID,
		Input:     input,
	})
	return cmdID, nil
}

// PollCommands returns and clears pending commands for a session.
// Called by the agent on a polling loop.
func (d *DebugStore) PollCommands(sessionID string) (*api.PollCommandsResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return &api.PollCommandsResponse{Closed: true}, nil
	}
	if s.status == "closed" {
		return &api.PollCommandsResponse{Closed: true}, nil
	}

	cancel := s.cancelRequested
	s.cancelRequested = false // Agent clears after seeing it.

	cmds := s.pendingCmds
	s.pendingCmds = nil
	return &api.PollCommandsResponse{
		Commands:      cmds,
		CancelCurrent: cancel,
	}, nil
}

// RequestCancel asks the agent to kill the currently running command
func (d *DebugStore) RequestCancel(sessionID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.cancelRequested = true
	return nil
}

// CheckAndClearCancel returns true of a cancel was requested and clears the flag.
// Intentionally does NOT touch pendingCmds - the cancel goroutine uses this
// so it doesn't accidentally drain commands mean for the main execute loop
func (d *DebugStore) CheckAndClearCancel(sessionID string) (bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return false, false
	}
	cancel := s.cancelRequested
	s.cancelRequested = false
	return cancel, true
}

// SubmitOutput receives command output from the agent and broadcasts it to the browser.
func (d *DebugStore) SubmitOutput(sessionID string, out api.DebugOutput) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.outputs = append(s.outputs, out)

	// Build SSE event JSON without importing encoding/json here.
	evt := fmt.Sprintf(
		`{"type":"output","command_id":%q,"output":%q,"exit_code":%d}`,
		out.CommandID, out.Output, out.ExitCode,
	)
	d.broadcast(s, evt)
	return nil
}

// CloseSession shuts down a session. The agent will detect this on its next poll and stop the container.
func (d *DebugStore) CloseSession(sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return
	}
	s.status = "closed"
	d.broadcast(s, `{"type":"closed"}`)

	for ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = make(map[chan string]struct{})
}

// GetInfo returns the current state of a session.
func (d *DebugStore) GetInfo(sessionID string) (*api.DebugSessionInfo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return nil, false
	}
	return d.info(s), true
}

// Subscribe registers a new SSE channel for a session.
func (d *DebugStore) Subscribe(sessionID string) (chan string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return nil, false
	}
	ch := make(chan string, 32)
	s.subscribers[ch] = struct{}{}
	return ch, true
}

// Unsubscribe removes an SSE channel
func (d *DebugStore) Unsubscribe(sessionID string, ch chan string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, ok := d.sessions[sessionID]
	if !ok {
		return
	}
	delete(s.subscribers, ch)
}

// ExpireSessions closes sessions whose TTL has elapsed.
func (d *DebugStore) ExpireSessions() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	for id, s := range d.sessions {
		if s.status != "closed" && now.After(s.expiresAt) {
			s.status = "closed"
			d.broadcast(s, `{"type":"closed","reason":"ttl_expired"}`)
			for ch := range s.subscribers {
				close(ch)
			}
			delete(d.sessions, id)
		}
	}
}

// GetJobSpec returns the job spec needed to recreate a debug container.
// Called by the server when a job lookup is needed for session creation.
func (d *DebugStore) SessionForJob(jobID string) (*debugSession, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		if s.jobID == jobID {
			return s, true
		}
	}
	return nil, false
}

// broadcast sends an event to all SSE subscribers. Callers must hold mu.
func (d *DebugStore) broadcast(s *debugSession, event string) {
	for ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (d *DebugStore) info(s *debugSession) *api.DebugSessionInfo {
	remaining := int(time.Until(s.expiresAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return &api.DebugSessionInfo{
		SessionID:     s.id,
		Status:        s.status,
		ContainerID:   s.containerID,
		ExpiresInS:    remaining,
		TerminalWsURL: s.terminalWsURL,
	}
}

// GetTerminalWsURL returns the internal agent WebSocket URL for a session.
// Used by the scheduler's WebSocket proxy to dial the correct agent.
func (d *DebugStore) GetTerminalWsURL(sessionID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[sessionID]
	if !ok || s.terminalWsURL == "" {
		return "", false
	}
	return s.terminalWsURL, true
}
