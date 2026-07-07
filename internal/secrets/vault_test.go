package secrets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestVault starts a minimal fake KV v2 Vault server.
// It handles any path under /v1/secret/data/ and /v1/secret/metadata/
// so all scope prefixes (global, orgs/*, projects/*) work transparently.
func newTestVault(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	store := map[string]string{} // full path key → value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		const dataPrefix = "/v1/secret/data/"
		const metaPrefix = "/v1/secret/metadata/"

		switch {
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, dataPrefix):
			key := r.URL.Path[len(dataPrefix):]
			var body struct {
				Data map[string]string `json:"data"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			store[key] = body.Data["value"]
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1}})

		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, dataPrefix):
			key := r.URL.Path[len(dataPrefix):]
			val, ok := store[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{"value": val},
				},
			})

		case r.Method == "LIST" && strings.HasPrefix(r.URL.Path, metaPrefix):
			prefix := r.URL.Path[len(metaPrefix):]
			prefix = strings.TrimSuffix(prefix, "/")
			var keys []string
			for k := range store {
				if strings.HasPrefix(k, prefix+"/") {
					keys = append(keys, strings.TrimPrefix(k, prefix+"/"))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"keys": keys},
			})

		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-token"), srv
}

func TestSetAndGet_Global(t *testing.T) {
	client, _ := newTestVault(t)
	prefix := GlobalScopePath()

	if err := client.Set(prefix, "MY_SECRET", "super-secret-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, err := client.Get(prefix, "MY_SECRET")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "super-secret-value" {
		t.Errorf("expected 'super-secret-value', got %q", val)
	}
}

func TestGet_NotFound(t *testing.T) {
	client, _ := newTestVault(t)
	_, err := client.Get(GlobalScopePath(), "DOES_NOT_EXIST")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

func TestList_Global(t *testing.T) {
	client, _ := newTestVault(t)
	prefix := GlobalScopePath()
	client.Set(prefix, "SECRET_A", "a")
	client.Set(prefix, "SECRET_B", "b")

	names, err := client.List(prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("expected 2 secrets, got %d: %v", len(names), names)
	}
}

func TestGetScoped_ResolutionChain(t *testing.T) {
	client, _ := newTestVault(t)

	// Store the same key at different scopes with distinct values.
	client.Set(GlobalScopePath(), "API_KEY", "global-value")
	client.Set(OrgScopePath("org1"), "API_KEY", "org-value")
	client.Set(ProjectScopePath("proj1"), "API_KEY", "project-value")

	// No scope → global
	val, err := client.GetScoped("API_KEY", "", "")
	if err != nil || val != "global-value" {
		t.Errorf("no scope: expected global-value, got %q (%v)", val, err)
	}

	// Org scope → org wins over global
	val, err = client.GetScoped("API_KEY", "org1", "")
	if err != nil || val != "org-value" {
		t.Errorf("org scope: expected org-value, got %q (%v)", val, err)
	}

	// Project scope → project wins over org and global
	val, err = client.GetScoped("API_KEY", "org1", "proj1")
	if err != nil || val != "project-value" {
		t.Errorf("project scope: expected project-value, got %q (%v)", val, err)
	}
}

func TestGetScoped_FallsThrough(t *testing.T) {
	client, _ := newTestVault(t)

	// Only stored at global — should still resolve with org+project context.
	client.Set(GlobalScopePath(), "ONLY_GLOBAL", "found-it")

	val, err := client.GetScoped("ONLY_GLOBAL", "org1", "proj1")
	if err != nil {
		t.Fatalf("expected fallthrough to global, got: %v", err)
	}
	if val != "found-it" {
		t.Errorf("expected 'found-it', got %q", val)
	}
}

func TestGetScoped_NotFound(t *testing.T) {
	client, _ := newTestVault(t)
	_, err := client.GetScoped("NONEXISTENT", "org1", "proj1")
	if err == nil {
		t.Fatal("expected error when secret doesn't exist at any scope")
	}
}

func TestInvalidToken(t *testing.T) {
	_, srv := newTestVault(t)
	badClient := NewClient(srv.URL, "wrong-token")
	if err := badClient.Ping(); err == nil {
		t.Fatal("expected error for bad token, got nil")
	}
}
