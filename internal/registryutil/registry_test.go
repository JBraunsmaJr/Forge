package registryutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	tokenServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") == "" {
			t.Errorf("token request missing scope query param")
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake-token"})
	}))
	defer tokenServer.Close()

	putCalled := false
	deleteCalled := false

	registryServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	m, err := client.GetManifest("myorg/myapp", "test-42")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if m.Digest != digest {
		t.Errorf("got digest %q, want %q", m.Digest, digest)
	}
	if string(m.Body) != manifestBody {
		t.Errorf("got body %q, want %q", m.Body, manifestBody)
	}

	if err := client.PutManifest("myorg/myapp", "v1.2.3", m); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if !putCalled {
		t.Error("expected PUT to reach the registry")
	}

	if err := client.DeleteTag("myorg/myapp", digest); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	if !deleteCalled {
		t.Error("expected DELETE to reach the registry")
	}

	// Second call should reuse the cached token — no extra token fetch
	// needed. We only verify it still succeeds; the cache itself is an
	// internal implementation detail.
	if _, err := client.GetManifest("myorg/myapp", "test-42"); err != nil {
		t.Fatalf("second GetManifest (cached token): %v", err)
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
	err := client.DeleteTag("myorg/myapp", "sha256:doesnotmatter")
	if err != ErrDeletionUnsupported {
		t.Fatalf("got err %v, want ErrDeletionUnsupported", err)
	}
}
