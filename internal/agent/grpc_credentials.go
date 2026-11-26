package agent

import (
	"context"

	"google.golang.org/grpc/credentials"
)

// bearerCredentials attaches the agent's API token to every gRPC call as an
// "authorization: Bearer <token>" metadata entry, the same form the HTTP API
// uses. The scheduler refuses agent sessions that do not carry it.
type bearerCredentials struct {
	token string
	// requireTLS makes gRPC refuse to send the token over a plaintext
	// connection. It mirrors whether the agent dialed with TLS, so that a
	// plaintext deployment keeps working while a TLS one cannot silently
	// downgrade.
	requireTLS bool
}

var _ credentials.PerRPCCredentials = bearerCredentials{}

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c bearerCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials.
func (c bearerCredentials) RequireTransportSecurity() bool {
	return c.requireTLS
}
