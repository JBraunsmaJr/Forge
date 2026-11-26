package scheduler

import (
	"fmt"
	"os"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Environment variables that enable TLS on the agent gRPC listener. Set both
// or neither.
const (
	envGRPCTLSCert = "FORGE_GRPC_TLS_CERT"
	envGRPCTLSKey  = "FORGE_GRPC_TLS_KEY"
)

// newGRPCServer builds the gRPC server that agents connect to, with the agent
// service registered.
//
// Every server option, including the interceptors that authenticate agents,
// belongs here so that tests exercise the same server that Start serves.
func (s *Server) newGRPCServer() (*grpc.Server, error) {
	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    10 * time.Second, // Ping the client if it is idle for 10s
			Timeout: 5 * time.Second,  // Wait 5s for the ping ack
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second, // Minimum time between client pings
			PermitWithoutStream: true,            // Allow pings even when there are no active streams
		}),
		grpc.MaxRecvMsgSize(64 * 1024 * 1024),
		grpc.MaxSendMsgSize(64 * 1024 * 1024),
		// Recovery is outermost so that it also covers the auth interceptor.
		grpc.ChainStreamInterceptor(recoverStreamInterceptor, s.agentAuthStreamInterceptor),
		grpc.ChainUnaryInterceptor(recoverUnaryInterceptor, s.agentAuthUnaryInterceptor),
	}

	tlsOpt, err := grpcTLSOption(os.Getenv(envGRPCTLSCert), os.Getenv(envGRPCTLSKey))
	if err != nil {
		return nil, err
	}
	if tlsOpt != nil {
		opts = append(opts, tlsOpt)
	}

	gsrv := grpc.NewServer(opts...)
	pb.RegisterAgentServiceServer(gsrv, &grpcServer{scheduler: s})
	return gsrv, nil
}

// grpcTLSOption returns the server option that enables TLS with the given
// certificate and key files, or nil when neither is set. Setting only one is
// an error: silently serving plaintext when TLS was intended would expose
// agent tokens.
func grpcTLSOption(certFile, keyFile string) (grpc.ServerOption, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("%s and %s must be set together", envGRPCTLSCert, envGRPCTLSKey)
	}
	creds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading gRPC TLS certificate: %w", err)
	}
	return grpc.Creds(creds), nil
}
