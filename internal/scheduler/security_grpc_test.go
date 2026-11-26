package scheduler

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// TestSecurity_S1_AgentSessionRequiresAuthentication covers threat model gap
// S1: a peer that presents no credential must not be able to register as an
// agent. The session must end with codes.Unauthenticated and the peer must
// never appear in the agent registry.
func TestSecurity_S1_AgentSessionRequiresAuthentication(t *testing.T) {
	const agentID = "unauthenticated-agent"

	s := &Server{agents: newAgentRegistry()}
	lis := bufconn.Listen(1 << 20)
	gsrv, err := s.newGRPCServer()
	if err != nil {
		t.Fatalf("building the gRPC server: %v", err)
	}
	go func() { _ = gsrv.Serve(lis) }()
	t.Cleanup(gsrv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dialing the in-memory gRPC server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := pb.NewAgentServiceClient(conn).Session(ctx)
	if err != nil {
		requireUnauthenticated(t, err)
		return
	}

	// A send can succeed even when the server has already refused the
	// stream, so the result is judged by the registry and by Recv below.
	_ = stream.Send(&pb.AgentMessage{
		AgentId: agentID,
		Payload: &pb.AgentMessage_Register{
			Register: &pb.RegisterRequest{Concurrency: 1},
		},
	})

	// Wait for the session to end. A server that refuses the peer ends the
	// stream at once, so this normally returns within milliseconds.
	recvErr := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvErr <- err
	}()

	// If the server accepts the peer instead, the stream stays open. Watch the
	// registry meanwhile and end the session at once if the agent shows up:
	// with no store behind this Server, a registered session must not live
	// long enough to lease a job.
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()

	for {
		select {
		case err := <-recvErr:
			requireUnauthenticated(t, err)
			if agentRegistered(s, agentID) {
				t.Fatalf("the session was refused, but agent %q is in the registry", agentID)
			}
			return
		case <-poll.C:
			if agentRegistered(s, agentID) {
				cancel()
				t.Fatalf("a peer with no credential was registered as agent %q", agentID)
			}
		case <-timeout.C:
			cancel()
			t.Fatal("the session was neither refused nor registered within one second")
		}
	}
}

func agentRegistered(s *Server, id string) bool {
	for _, a := range s.agents.List() {
		if a.ID == id {
			return true
		}
	}
	return false
}

func requireUnauthenticated(t *testing.T, err error) {
	t.Helper()
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("session ended with code %s (%v); want %s", got, err, codes.Unauthenticated)
	}
}
