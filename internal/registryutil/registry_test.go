package registryutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRegistryHost(t *testing.T) {
	cases := map[string]string{
		"docker.io":             "https://registry-1.docker.io",
		"index.docker.io":       "https://registry-1.docker.io",
		"":                      "https://registry-1.docker.io",
		"ghcr.io":               "https://ghcr.io",
		"https://ghcr.io":       "https://ghcr.io",
		"registry.example.com/": "https://registry.example.com",
	}
	for in, want := range cases {
		if got := NormalizeRegistryHost(in); got != want {
			t.Errorf("NormalizeRegistryHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBearerChallenge(t *testing.T) {
	challenge := `Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:myorg/myapp:pull,push"`
	got := parseBearerChallenge(challenge)
	want := map[string]string{
		"realm":   "https://auth.example.com/token",
		"service": "registry.example.com",
		"scope":   "repository:myorg/myapp:pull,push",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestPromotionFlow spins up a fake registry that requires a bearer
// token, and verifies GetManifest -> PutManifest -> DeleteTag all
// transparently negotiate that token and hit the right endpoints.
func TestPromotionFlow(t *testing.T) {
	const manifestBody = `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`
	const digest = "sha256:abc123"

	var tokenServer *httptest.Server
	var registryServer *httptest.Server

	tokenServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") == "" {
			t.Errorf("token request missing scope query param")
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake-token"})
	}))
	defer tokenServer.Close()

	putCalled := false
	deleteCalled := false

	registryServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer fake-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenServer.URL+`",service="test-registry",scope="repository:myorg/myapp:pull,push"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(manifestBody))
		case http.MethodPut:
			putCalled = true
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			deleteCalled = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer registryServer.Close()

	client := NewClient(registryServer.URL, "user", "pass")
	// httptest.NewTLSServer certs are self-signed and per-server; trust
	// both of this test's fake servers specifically, rather than
	// disabling verification, so this still exercises real TLS
	// validation end to end.
	certPool := x509.NewCertPool()
	certPool.AddCert(registryServer.Certificate())
	certPool.AddCert(tokenServer.Certificate())
	client.httpClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certPool}}

	m, err := client.GetManifest(context.Background(), "myorg/myapp", "test-42")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if m.Digest != digest {
		t.Errorf("got digest %q, want %q", m.Digest, digest)
	}
	if string(m.Body) != manifestBody {
		t.Errorf("got body %q, want %q", m.Body, manifestBody)
	}

	if err := client.PutManifest(context.Background(), "myorg/myapp", "v1.2.3", m); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if !putCalled {
		t.Error("expected PUT to reach the registry")
	}

	if err := client.DeleteTag(context.Background(), "myorg/myapp", "test-42"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	if !deleteCalled {
		t.Error("expected DELETE to reach the registry")
	}

	// Second call should reuse the cached token — no extra token fetch
	// needed. We only verify it still succeeds; the cache itself is an
	// internal implementation detail.
	if _, err := client.GetManifest(context.Background(), "myorg/myapp", "test-42"); err != nil {
		t.Fatalf("second GetManifest (cached token): %v", err)
	}
}

func TestValidTag(t *testing.T) {
	cases := map[string]bool{
		"latest":                 true,
		"v1.2.3":                 true,
		"2026-08.14":             true,
		"test-2026-08.14":        true,
		"release/42":             false, // slash — the exact case a bad build-number format would produce
		"feature/builds":         false, // a raw branch name, same problem
		"":                       false,
		".starts-with-dot":       false,
		"-starts-with-hyphen":    false,
		strings.Repeat("a", 128): true,  // exactly at the limit
		strings.Repeat("a", 129): false, // one over the limit
	}
	for tag, want := range cases {
		if got := ValidTag(tag); got != want {
			t.Errorf("ValidTag(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestDeleteTag_UnsupportedReturnsTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a registry (like Docker Hub) that just rejects
		// deletion outright, no auth challenge involved.
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	err := client.DeleteTag(context.Background(), "myorg/myapp", "test-42")
	if err != ErrDeletionUnsupported {
		t.Fatalf("got err %v, want ErrDeletionUnsupported", err)
	}
}

// TestFetchToken_RejectsNonHTTPSRealm verifies a registry's own
// WWW-Authenticate challenge can't trick the client into sending Basic
// credentials to a plain-HTTP token endpoint.
func TestFetchToken_RejectsNonHTTPSRealm(t *testing.T) {
	registryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="http://insecure.example.com/token",service="test-registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registryServer.Close()

	client := NewClient(registryServer.URL, "user", "pass")
	_, err := client.GetManifest(context.Background(), "myorg/myapp", "test-42")
	if err == nil {
		t.Fatal("expected an error for a non-HTTPS token realm, got nil")
	}
	if !strings.Contains(err.Error(), "non-HTTPS realm") {
		t.Fatalf("got error %v, want it to mention the non-HTTPS realm", err)
	}
}

// TestClient_RefusesHTTPRedirect verifies the shared http.Client won't
// follow a redirect that downgrades to plain HTTP, which would replay
// Authorization headers (credentials, bearer tokens) in cleartext.
func TestClient_RefusesHTTPRedirect(t *testing.T) {
	insecureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("insecure HTTP redirect target should never actually be reached")
		w.WriteHeader(http.StatusOK)
	}))
	defer insecureServer.Close()

	registryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureServer.URL, http.StatusFound)
	}))
	defer registryServer.Close()

	client := NewClient(registryServer.URL, "", "")
	_, err := client.GetManifest(context.Background(), "myorg/myapp", "test-42")
	if err == nil {
		t.Fatal("expected an error refusing the HTTP redirect, got nil")
	}
}
