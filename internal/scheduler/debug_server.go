package scheduler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func (s *Server) handleCreateDebugSession(w http.ResponseWriter, r *http.Request) {
	var req api.CreateDebugRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	detail, ok := s.store.RunDetailByJobID(req.JobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	var jobDetail *api.JobDetail
	for i := range detail.Jobs {
		if detail.Jobs[i].JobID == req.JobID {
			jobDetail = &detail.Jobs[i]
			break
		}
	}
	if jobDetail == nil || jobDetail.Status != api.JobStatusFailed {
		writeError(w, http.StatusBadRequest, "can only debug failed jobs")
		return
	}

	runID, image, workDir, workspaceDir, projectID, commitSHA, dockerSocket := s.store.GetJobDetails(req.JobID)
	env := s.store.GetJobEnv(req.JobID)
	info := s.debug.CreateSession(req.JobID, runID, image, workDir, env, workspaceDir, projectID, commitSHA, dockerSocket)
	fmt.Printf("[scheduler] debug session %s created for job %s\n", info.SessionID[:8], req.JobID[:8])
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) handleGetDebugSession(w http.ResponseWriter, r *http.Request) {
	info, ok := s.debug.GetInfo(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleDebugStream(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "websocket" {
		s.handleDebugStreamWS(w, r)
		return
	}

	sessionID := r.PathValue("id")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	if info, ok := s.debug.GetInfo(sessionID); ok {
		data, _ := json.Marshal(map[string]any{
			"type":         "status",
			"status":       info.Status,
			"container_id": info.ContainerID,
			"expires_in_s": info.ExpiresInS,
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ch, ok := s.debug.Subscribe(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	defer s.debug.Unsubscribe(sessionID, ch)

	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleDebugExec(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	var req struct {
		Input string `json:"input"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}

	cmdID, err := s.debug.ExecCommand(sessionID, req.Input)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"command_id": cmdID})
}

func (s *Server) handleCloseDebugSession(w http.ResponseWriter, r *http.Request) {
	s.debug.CloseSession(r.PathValue("id"))
	fmt.Printf("[scheduler] debug session %s closed\n", r.PathValue("id")[:8])
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancelDebugCommand(w http.ResponseWriter, r *http.Request) {
	if err := s.debug.RequestCancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDebugCancelCheck is polled by the agent to check if a cancel was requested.
// Separate from handleDebugPollCommands so the cancel goroutine doesn't drain commands.
func (s *Server) handleDebugCancelCheck(w http.ResponseWriter, r *http.Request) {
	cancel, exists := s.debug.CheckAndClearCancel(r.PathValue("id"))
	if !exists {
		writeJSON(w, http.StatusOK, map[string]any{"cancel": false, "closed": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancel": cancel, "closed": false})
}

func (s *Server) handleDebugLease(w http.ResponseWriter, r *http.Request) {
	var req api.LeaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	spec, ok := s.debug.LeaseNext(req.AgentID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	fmt.Printf("[scheduler] debug session %s leased to agent %s\n",
		spec.SessionID[:8], req.AgentID[:8])
	writeJSON(w, http.StatusOK, spec)
}

func (s *Server) handleDebugRegisterContainer(w http.ResponseWriter, r *http.Request) {
	var req api.RegisterContainerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.debug.RegisterContainer(r.PathValue("id"), req.ContainerID, req.AgentID, req.TerminalWsURL); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	fmt.Printf("[scheduler] debug session %s ready — terminal: %s\n",
		r.PathValue("id")[:8], req.TerminalWsURL)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDebugPollCommands(w http.ResponseWriter, r *http.Request) {
	resp, err := s.debug.PollCommands(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDebugSubmitOutput(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitOutputRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err := s.debug.SubmitOutput(r.PathValue("id"), api.DebugOutput{
		CommandID: req.CommandID,
		Output:    req.Output,
		ExitCode:  req.ExitCode,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// debugExpiryMonitor runs in a goroutine to close sessions whose TTL has elapsed.
func (s *Server) debugExpiryMonitor(stopCh <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.debug.ExpireSessions()
		case <-stopCh:
			return
		}
	}
}

// handleDebugTerminalProxy proxies a browser WebSocket connection to the
// agent that owns the debug session. In this new 'reverse' model, the
// scheduler triggers a request to the agent, which then dials back to
// the scheduler. This eliminates the need for agents to expose any ports.
func (s *Server) handleDebugTerminalProxy(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	/*
		Validate the session-scope one-time token so the long-lived API token
		never appears in the server access logs (WebSocket upgrade URLs are logged
		by the reverse proxies). The browser obtains this token from the session
		info response (DebugSessionInfo.WsToken) and appends it as ?ws_token=.
		Fall through if no token is present - the outer auth middleware already
		verified the API token, providing a safe default for old clients.
	*/
	if wsToken := r.URL.Query().Get("ws_token"); wsToken != "" {
		if !s.debug.ValidateWsToken(sessionID, wsToken) {
			writeError(w, http.StatusForbidden, "invalid or expired ws_token")
			return
		}
	}

	// Parse initial terminal size from query params, set by xterm.js FitAddon.
	cols, rows := 220, 50
	if c := r.URL.Query().Get("cols"); c != "" {
		fmt.Sscanf(c, "%d", &cols)
	}
	if r2 := r.URL.Query().Get("rows"); r2 != "" {
		fmt.Sscanf(r2, "%d", &rows)
	}

	// 1. Tell the agent we want a terminal.
	termID, ch := s.debug.RequestTerminal(sessionID, cols, rows)
	if ch == nil {
		writeError(w, http.StatusNotFound, "debug session not found")
		return
	}
	defer s.debug.CleanupTerminal(termID)

	// 2. Upgrade the browser's connection to WebSocket.
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browserConn.Close()

	// 3. Wait for the agent to connect back.
	var agentConn *websocket.Conn
	select {
	case conn := <-ch:
		agentConn = conn
	case <-time.After(10 * time.Second):
		browserConn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n\x1b[31mTimeout waiting for agent to connect back. Is the agent running?\x1b[0m\r\n"))
		return
	case <-r.Context().Done():
		return
	}
	defer agentConn.Close()

	fmt.Printf("[scheduler] proxying terminal WS for session %s (reverse connect %s)\n",
		sessionID[:8], termID)

	// Bidirectional proxy: browser <--> agent.
	errc := make(chan error, 2)
	go func() {
		for {
			mt, msg, err := browserConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := agentConn.WriteMessage(mt, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			mt, msg, err := agentConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := browserConn.WriteMessage(mt, msg); err != nil {
				errc <- err
				return
			}
		}
	}()

	<-errc
}

// handleAgentDebugTerminal accepts a reverse WebSocket connection from an agent.
func (s *Server) handleAgentDebugTerminal(w http.ResponseWriter, r *http.Request) {
	termID := r.URL.Query().Get("terminalID")
	if termID == "" {
		http.Error(w, "terminalID is required", http.StatusBadRequest)
		return
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("[scheduler] failed to upgrade agent reverse WS: %v\n", err)
		return
	}

	if !s.debug.RegisterAgentConn(termID, conn) {
		conn.WriteMessage(websocket.TextMessage, []byte("invalid terminalID"))
		conn.Close()
		return
	}

	// The connection is now owned by handleDebugTerminalProxy.
	// We must NOT close it here. We just return and let the connection live.
}
