package agent

import (
	"context"
	"testing"
)

func TestBearerCredentials(t *testing.T) {
	creds := bearerCredentials{token: "fgt_example", requireTLS: true}

	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if got, want := md["authorization"], "Bearer fgt_example"; got != want {
		t.Errorf("authorization metadata = %q; want %q", got, want)
	}
	if !creds.RequireTransportSecurity() {
		t.Error("RequireTransportSecurity() = false; want true when requireTLS is set")
	}
	if (bearerCredentials{token: "x"}).RequireTransportSecurity() {
		t.Error("RequireTransportSecurity() = true; want false for a plaintext connection")
	}
}
