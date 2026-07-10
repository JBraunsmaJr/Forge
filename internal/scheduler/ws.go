package scheduler

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for simplicity in this example
	},
}

func (s *Server) handleRunEventsWS(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Send initial state
	if detail, ok := s.store.RunDetail(runID); ok {
		if err := conn.WriteJSON(detail); err != nil {
			return
		}
	}

	ch := s.broker.Subscribe(runID)
	defer s.broker.Unsubscribe(runID, ch)

	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			// event is a JSON string from broker.Publish
			var msg any
			if err := json.Unmarshal([]byte(event), &msg); err == nil {
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleJobLogStreamWS(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Catch up with existing logs
	if logs, ok := s.store.GetJobLogs(jobID); ok {
		for _, log := range logs {
			if err := conn.WriteJSON(log); err != nil {
				return
			}
		}
	}

	ch := s.broker.Subscribe("log:" + jobID)
	defer s.broker.Unsubscribe("log:"+jobID, ch)

	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			var msg any
			if err := json.Unmarshal([]byte(event), &msg); err == nil {
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleDebugStreamWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Initial status
	if info, ok := s.debug.GetInfo(sessionID); ok {
		conn.WriteJSON(map[string]any{
			"type":         "status",
			"status":       info.Status,
			"container_id": info.ContainerID,
			"expires_in_s": info.ExpiresInS,
		})
	}

	ch, ok := s.debug.Subscribe(sessionID)
	if !ok {
		return
	}
	defer s.debug.Unsubscribe(sessionID, ch)

	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			var msg any
			if err := json.Unmarshal([]byte(event), &msg); err == nil {
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			}
		case <-r.Context().Done():
			return
		}
	}
}
