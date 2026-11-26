package scheduler

import (
	"time"

	"github.com/JBraunsmaJr/forge/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// newGRPCServer builds the gRPC server that agents connect to, with the agent
// service registered.
//
// Every server option, including any interceptor that authenticates agents,
// belongs here so that tests exercise the same server that Start serves.
func (s *Server) newGRPCServer() *grpc.Server {
	var kasp = keepalive.ServerParameters{
		Time:    10 * time.Second, // Ping the client if it is idle for 10s
		Timeout: 5 * time.Second,  // Wait 5s for the ping ack
	}
	var kapv = keepalive.EnforcementPolicy{
		MinTime:             5 * time.Second, // Minimum time between client pings
		PermitWithoutStream: true,            // Allow pings even when there are no active streams
	}
	gsrv := grpc.NewServer(
		grpc.KeepaliveParams(kasp),
		grpc.KeepaliveEnforcementPolicy(kapv),
		grpc.MaxRecvMsgSize(64*1024*1024),
		grpc.MaxSendMsgSize(64*1024*1024),
	)
	pb.RegisterAgentServiceServer(gsrv, &grpcServer{scheduler: s})
	return gsrv
}
