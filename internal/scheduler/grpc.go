package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/pb"
)

type grpcServer struct {
	pb.UnimplementedAgentServiceServer
	scheduler *Server
}

func (s *grpcServer) Session(stream pb.AgentService_SessionServer) error {
	var agentID string
	var concurrency int32

	// First message must be a register request
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	if reg := msg.GetRegister(); reg != nil {
		agentID = msg.AgentId
		concurrency = reg.Concurrency
		fmt.Printf("[grpc] agent %s registered (concurrency: %d)\n", agentID[:8], concurrency)
	} else {
		return fmt.Errorf("first message must be register")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	// Goroutine to receive messages from the agent (heartbeats, completion, logs)
	go func() {
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				cancel()
				return
			}
			if err != nil {
				cancel()
				return
			}

			switch m := msg.Payload.(type) {
			case *pb.AgentMessage_Heartbeat:
				err := s.scheduler.store.Heartbeat(m.Heartbeat.JobId, m.Heartbeat.LeaseId, msg.AgentId)
				stop := err != nil
				stream.Send(&pb.SchedulerMessage{
					Payload: &pb.SchedulerMessage_HeartbeatAck{
						HeartbeatAck: &pb.HeartbeatAck{
							JobId: m.Heartbeat.JobId,
							Stop:  stop,
						},
					},
				})
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
				runID, err := s.scheduler.store.Complete(m.Complete.JobId, m.Complete.LeaseId, int(m.Complete.ExitCode), m.Complete.DurationMs, logs, emitted, false)
				if err != nil {
					fmt.Printf("[grpc] store.Complete error for job %s: %v\n", m.Complete.JobId, err)
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
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Try to lease a job for this agent
			spec, ok := s.scheduler.store.LeaseNext(agentID)
			if ok {
				fmt.Printf("[grpc] pushing job %s to agent %s\n", spec.JobID[:8], agentID[:8])
				s.scheduler.publishRunDetail(spec.RunID)

				pbSpec := &pb.JobSpec{
					JobId:        spec.JobID,
					RunId:        spec.RunID,
					LeaseId:      spec.LeaseID,
					StepId:       spec.StepID,
					Image:        spec.Image,
					Entrypoint:   spec.Entrypoint,
					Command:      spec.Command,
					WorkDir:      spec.WorkDir,
					Env:          spec.Env,
					Inputs:       spec.Inputs,
					SecretNames:  spec.SecretNames,
					DockerSocket: spec.DockerSocket,
					TimeoutNs:    int64(spec.Timeout),
					Type:         spec.Type,
					OrgId:        spec.OrgID,
					ProjectId:    spec.ProjectID,
					CommitSha:    spec.CommitSHA,
					Condition:    spec.Condition,
					AlwaysRun:    spec.AlwaysRun,
				}

				if spec.PipelineRef != nil {
					pbSpec.PipelineRef = &pb.PipelineRef{
						Path: spec.PipelineRef.Path,
						Wait: spec.PipelineRef.Wait,
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

				if err := stream.Send(&pb.SchedulerMessage{
					Payload: &pb.SchedulerMessage_Job{
						Job: pbSpec,
					},
				}); err != nil {
					return err
				}
			}
		}
	}
}

func (s *grpcServer) LeaseJob(ctx context.Context, req *pb.RegisterRequest) (*pb.JobSpec, error) {
	// This is for unary fallback if needed, but we use Session stream.
	return nil, fmt.Errorf("use Session stream")
}
