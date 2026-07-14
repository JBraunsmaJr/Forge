package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/pb"
	"google.golang.org/grpc/peer"
)

type grpcServer struct {
	pb.UnimplementedAgentServiceServer
	scheduler *Server
}

func (s *grpcServer) Session(stream pb.AgentService_SessionServer) error {
	p, _ := peer.FromContext(stream.Context())
	addr := "unknown"
	if p != nil {
		addr = p.Addr.String()
	}

	// Set a timeout for the first message (registration)
	ctx, cancel := context.WithTimeout(stream.Context(), 10*time.Second)
	defer cancel()

	type result struct {
		msg *pb.AgentMessage
		err error
	}
	resChan := make(chan result, 1)

	go func() {
		msg, err := stream.Recv()
		resChan <- result{msg, err}
	}()

	var msg *pb.AgentMessage
	var err error
	select {
	case res := <-resChan:
		msg = res.msg
		err = res.err
	case <-ctx.Done():
		return fmt.Errorf("registration timeout")
	}

	if err != nil {
		return err
	}
	var agentID string
	var concurrency int
	if reg := msg.GetRegister(); reg != nil {
		agentID = msg.AgentId
		concurrency = int(reg.Concurrency)
		if concurrency <= 0 {
			concurrency = 1
		}
		s.scheduler.agents.Register(agentID, concurrency, msg.GetRegister().Labels)
		log.Printf("[grpc] agent %s registered from %s (concurrency: %d)", agentID[:8], addr, concurrency)
	} else {
		return fmt.Errorf("first message must be register")
	}

	// Use a new context for the rest of the session
	defer s.scheduler.agents.Disconnect(agentID)
	sessionCtx, sessionCancel := context.WithCancel(stream.Context())
	defer sessionCancel()

	// Outgoing message channel to ensure thread-safe stream.Send
	out := make(chan *pb.SchedulerMessage, 64)
	go func() {
		for {
			select {
			case <-sessionCtx.Done():
				return
			case msg := <-out:
				if err := stream.Send(msg); err != nil {
					sessionCancel()
					return
				}
			}
		}
	}()

	// Goroutine to receive messages from the agent (heartbeats, completion, logs)
	go func() {
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				sessionCancel()
				return
			}
			if err != nil {
				sessionCancel()
				return
			}

			switch m := msg.Payload.(type) {
			case *pb.AgentMessage_Heartbeat:
				s.scheduler.agents.Heartbeat(msg.AgentId, m.Heartbeat.Status)
				if m.Heartbeat.JobId == "" {
					continue // Agent-wide status heartbeat
				}
				err := s.scheduler.store.Heartbeat(m.Heartbeat.JobId, m.Heartbeat.LeaseId, msg.AgentId)
				stop := err != nil
				out <- &pb.SchedulerMessage{
					Payload: &pb.SchedulerMessage_HeartbeatAck{
						HeartbeatAck: &pb.HeartbeatAck{
							JobId: m.Heartbeat.JobId,
							Stop:  stop,
						},
					},
				}
			case *pb.AgentMessage_Complete:
				logs := make([]api.LogEvent, len(m.Complete.Logs))
				for i, l := range m.Complete.Logs {
					logs[i] = api.LogEvent{
						Timestamp: l.Ts.AsTime(),
						Level:     l.Level,
						Message:   l.Message,
					}
				}
				var emitted []api.StepDef
				if m.Complete.EmittedStepsJson != "" {
					if err := json.Unmarshal([]byte(m.Complete.EmittedStepsJson), &emitted); err != nil {
						fmt.Printf("[grpc] failed to unmarshal emitted steps: %v\n", err)
					}
				}
				runID, err := s.scheduler.store.Complete(m.Complete.JobId, m.Complete.LeaseId, int(m.Complete.ExitCode), m.Complete.DurationMs, logs, emitted, m.Complete.Skipped, m.Complete.TimedOut)
				if err != nil {
					fmt.Printf("[grpc] store.Complete error for job %s: %v\n", m.Complete.JobId, err)
				}

				result := "passed"
				if m.Complete.TimedOut {
					result = "timed_out"
				} else if m.Complete.ExitCode != 0 {
					result = "failed"
				}

				if detail, ok := s.scheduler.store.RunDetail(runID); ok {
					stepID := s.scheduler.store.GetJobStepID(m.Complete.JobId)
					if stepID == "" {
						stepID = m.Complete.JobId
					}
					go s.scheduler.store.RecordStepResult(runID, detail.Name, stepID, result, m.Complete.DurationMs)

					jobsCompletedTotal.WithLabelValues(detail.OrgID, detail.ProjectID, result).Inc()
					jobDurationSeconds.WithLabelValues(detail.OrgID, detail.ProjectID).Observe(float64(m.Complete.DurationMs) / 1000.0)
				}

				s.scheduler.publishRunDetail(runID)
				s.scheduler.publishJobLogs(m.Complete.JobId, logs)
			case *pb.AgentMessage_LogBatch:
				logs := make([]api.LogEvent, len(m.LogBatch.Events))
				for i, l := range m.LogBatch.Events {
					logs[i] = api.LogEvent{
						Timestamp: l.Ts.AsTime(),
						Level:     l.Level,
						Message:   l.Message,
					}
				}
				if err := s.scheduler.store.AppendJobLogs(m.LogBatch.JobId, m.LogBatch.LeaseId, logs); err != nil {
					fmt.Printf("[grpc] failed to persist logs for job %s: %v\n", m.LogBatch.JobId[:8], err)
				}
				s.scheduler.publishJobLogs(m.LogBatch.JobId, logs)
			}
		}
	}()

	// Loop to push jobs to the agent
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sessionCtx.Done():
			return nil
		case <-ticker.C:
			// Check if agent has capacity
			active, err := s.scheduler.store.ActiveJobsCount(agentID)
			if err != nil {
				log.Printf("[grpc] error checking active jobs for agent %s: %v", agentID[:8], err)
				continue
			}

			if active >= concurrency {
				// Agent is at capacity, skip leasing for now
				continue
			}

			// Try to lease a job for this agent
			spec, ok := s.scheduler.store.LeaseNext(agentID)
			if ok {
				s.scheduler.publishRunDetail(spec.RunID)

				pbSpec := &pb.JobSpec{
					JobId:          spec.JobID,
					RunId:          spec.RunID,
					LeaseId:        spec.LeaseID,
					StepId:         spec.StepID,
					Image:          spec.Image,
					Entrypoint:     spec.Entrypoint,
					Command:        spec.Command,
					WorkDir:        spec.WorkDir,
					Env:            spec.Env,
					Inputs:         spec.Inputs,
					SecretNames:    spec.SecretNames,
					DockerSocket:   spec.DockerSocket,
					TimeoutNs:      int64(spec.Timeout),
					Type:           spec.Type,
					OrgId:          spec.OrgID,
					ProjectId:      spec.ProjectID,
					CommitSha:      spec.CommitSHA,
					Condition:      spec.Condition,
					AlwaysRun:      spec.AlwaysRun,
					AppliedStepIds: spec.AppliedStepIDs,
					WorkspaceDir:   spec.WorkspaceDir,
					Ref:            spec.Ref,
				}

				if spec.PipelineRef != nil {
					pbSpec.PipelineRef = &pb.PipelineRef{
						Path:             spec.PipelineRef.Path,
						Wait:             spec.PipelineRef.Wait,
						Variables:        spec.PipelineRef.Variables,
						ArtifactsSend:    spec.PipelineRef.ArtifactsSend,
						ArtifactsReceive: spec.PipelineRef.ArtifactsReceive,
					}
				}

				for _, u := range spec.ArtifactUploads {
					pbSpec.ArtifactUploads = append(pbSpec.ArtifactUploads, &pb.ArtifactUploadSpec{
						Path: u.Path,
						Name: u.Name,
					})
				}
				for _, d := range spec.ArtifactDownloads {
					pbSpec.ArtifactDownloads = append(pbSpec.ArtifactDownloads, &pb.ArtifactDownloadSpec{
						Name: d.Name,
						Dest: d.Dest,
					})
				}

				out <- &pb.SchedulerMessage{
					Payload: &pb.SchedulerMessage_Job{
						Job: pbSpec,
					},
				}
			}
		}
	}
}

func (s *grpcServer) LeaseJob(ctx context.Context, req *pb.RegisterRequest) (*pb.JobSpec, error) {
	// This is for unary fallback if needed, but we use Session stream.
	return nil, fmt.Errorf("use Session stream")
}
