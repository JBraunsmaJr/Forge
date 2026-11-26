package scheduler

import (
	"context"
	"log"
	"runtime/debug"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// authorizationMetadataKey is the gRPC metadata key that carries the
	// agent's API token, in the same "Bearer <token>" form as the HTTP API.
	authorizationMetadataKey = "authorization"

	// maxAgentIDLength bounds the agent IDs the scheduler will register.
	maxAgentIDLength = 128

	// shortIDLength is how much of an ID is shown in log lines.
	shortIDLength = 8
)

// agentAuthStreamInterceptor refuses any stream whose caller does not present
// a valid agent token. It runs before the handler, so an unauthenticated peer
// never reaches the agent registry or the job queue.
func (s *Server) agentAuthStreamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if _, err := s.authenticateAgent(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// agentAuthUnaryInterceptor is the unary counterpart of
// agentAuthStreamInterceptor, so that no RPC on the agent service is left
// unauthenticated.
func (s *Server) agentAuthUnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if _, err := s.authenticateAgent(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// authenticateAgent resolves the bearer token in the call's metadata and
// checks that it belongs to an agent. It returns codes.Unauthenticated when
// the token is missing or unknown, and codes.PermissionDenied when the token
// is valid but is not an agent token.
func (s *Server) authenticateAgent(ctx context.Context) (*tokenRecord, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent token required")
	}
	raw := bearerToken(md.Get(authorizationMetadataKey))
	if raw == "" {
		return nil, status.Error(codes.Unauthenticated, "agent token required")
	}
	rec, ok := s.verifier().Verify(raw)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "invalid or revoked agent token")
	}
	if rec.Role != roleAgent {
		return nil, status.Error(codes.PermissionDenied, "an agent token is required; this token has a different role")
	}
	return rec, nil
}

// bearerToken extracts the token from the first "Bearer <token>" value, or
// returns "" when there is none. The scheme is matched case-insensitively.
func bearerToken(values []string) string {
	const prefix = "bearer "
	for _, v := range values {
		if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
			return strings.TrimSpace(v[len(prefix):])
		}
	}
	return ""
}

// recoverStreamInterceptor turns a panic in a stream handler into an Internal
// error for that one stream. Without it, gRPC lets the panic end the process,
// which would let a single malformed session take down the scheduler.
func recoverStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[grpc] panic in %s: %v\n%s", info.FullMethod, r, debug.Stack())
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(srv, ss)
}

// recoverUnaryInterceptor is the unary counterpart of recoverStreamInterceptor.
func recoverUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[grpc] panic in %s: %v\n%s", info.FullMethod, r, debug.Stack())
			resp, err = nil, status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

// validAgentID reports whether id is acceptable as an agent identifier: 1 to
// maxAgentIDLength characters, starting with a letter or digit and otherwise
// limited to letters, digits, '.', '_', ':' and '-'. Agent IDs end up in log
// lines, Docker labels, and socket file names, so the set is kept narrow.
func validAgentID(id string) bool {
	if id == "" || len(id) > maxAgentIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == ':' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// shortID abbreviates an ID for log lines. Unlike slicing, it is safe for IDs
// shorter than shortIDLength.
func shortID(id string) string {
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength]
}
