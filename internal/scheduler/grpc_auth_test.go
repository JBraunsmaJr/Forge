package scheduler

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// stubVerifier is a tokenVerifier backed by a map of raw token to record.
type stubVerifier map[string]*tokenRecord

func (v stubVerifier) Verify(rawToken string) (*tokenRecord, bool) {
	rec, ok := v[rawToken]
	return rec, ok
}

func TestAuthenticateAgent(t *testing.T) {
	s := &Server{authn: stubVerifier{
		"agent-token":  {ID: "t1", Name: "agent", Role: roleAgent},
		"viewer-token": {ID: "t2", Name: "viewer", Role: "viewer"},
		"admin-token":  {ID: "t3", Name: "root", Role: "admin"},
	}}

	withAuth := func(value string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs(authorizationMetadataKey, value))
	}

	tests := []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{"no metadata", context.Background(), codes.Unauthenticated},
		{"empty metadata", metadata.NewIncomingContext(context.Background(), metadata.MD{}), codes.Unauthenticated},
		{"wrong scheme", withAuth("Basic agent-token"), codes.Unauthenticated},
		{"empty bearer", withAuth("Bearer "), codes.Unauthenticated},
		{"unknown token", withAuth("Bearer nope"), codes.Unauthenticated},
		{"viewer token", withAuth("Bearer viewer-token"), codes.PermissionDenied},
		{"admin token", withAuth("Bearer admin-token"), codes.PermissionDenied},
		{"agent token", withAuth("Bearer agent-token"), codes.OK},
		{"agent token, lowercase scheme", withAuth("bearer agent-token"), codes.OK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := s.authenticateAgent(tt.ctx)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("authenticateAgent() code = %s (%v); want %s", got, err, tt.want)
			}
			if tt.want == codes.OK && (rec == nil || rec.Role != roleAgent) {
				t.Errorf("authenticateAgent() record = %+v; want an agent record", rec)
			}
			if tt.want != codes.OK && rec != nil {
				t.Errorf("authenticateAgent() returned a record alongside an error: %+v", rec)
			}
		})
	}
}

func TestValidAgentID(t *testing.T) {
	valid := []string{
		"a",
		"agent-1",
		"0f3c9a1b2d4e5f60",
		"vmss_000003",
		"pool.eu:agent-7",
		strings.Repeat("a", maxAgentIDLength),
	}
	for _, id := range valid {
		if !validAgentID(id) {
			t.Errorf("validAgentID(%q) = false; want true", id)
		}
	}

	invalid := []string{
		"",
		"-leading-dash",
		".leading-dot",
		"has space",
		"has/slash",
		"has\nnewline",
		"semi;colon",
		strings.Repeat("a", maxAgentIDLength+1),
	}
	for _, id := range invalid {
		if validAgentID(id) {
			t.Errorf("validAgentID(%q) = true; want false", id)
		}
	}
}

func TestShortID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"12345678", "12345678"},
		{"123456789abcdef", "12345678"},
	}
	for _, tt := range tests {
		if got := shortID(tt.in); got != tt.want {
			t.Errorf("shortID(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestGRPCTLSOption(t *testing.T) {
	if opt, err := grpcTLSOption("", ""); err != nil || opt != nil {
		t.Errorf("grpcTLSOption with nothing set = (%v, %v); want (nil, nil)", opt, err)
	}
	if _, err := grpcTLSOption("cert.pem", ""); err == nil {
		t.Error("grpcTLSOption with only a certificate: want an error")
	}
	if _, err := grpcTLSOption("", "key.pem"); err == nil {
		t.Error("grpcTLSOption with only a key: want an error")
	}
	if _, err := grpcTLSOption("/nonexistent/cert.pem", "/nonexistent/key.pem"); err == nil {
		t.Error("grpcTLSOption with missing files: want an error")
	}
}
