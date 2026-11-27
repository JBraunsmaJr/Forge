package scm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

// statusServer records the status requests it receives and answers each
// with the next of the given HTTP statuses, repeating the last one.
type statusServer struct {
	*httptest.Server
	calls    atomic.Int32
	lastPath atomic.Value
	lastBody atomic.Value
	headers  atomic.Value
}

func newStatusServer(t *testing.T, responses ...struct {
	code int
	body string
}) *statusServer {
	t.Helper()
	s := &statusServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.calls.Add(1)) - 1
		if n >= len(responses) {
			n = len(responses) - 1
		}
		var payload map[string]string
		json.NewDecoder(r.Body).Decode(&payload)
		s.lastBody.Store(payload)
		s.lastPath.Store(r.URL.EscapedPath())
		s.headers.Store(r.Header.Clone())
		w.WriteHeader(responses[n].code)
		w.Write([]byte(responses[n].body))
	}))
	t.Cleanup(s.Close)
	return s
}

type response = struct {
	code int
	body string
}

func (s *statusServer) payload() map[string]string {
	payload, _ := s.lastBody.Load().(map[string]string)
	return payload
}

// fastRetry retries three times without making the test wait.
var fastRetry = RetryPolicy{Delays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}}

func TestReportStatus_GitLab(t *testing.T) {
	server := newStatusServer(t, response{http.StatusCreated, `{}`})

	err := ReportStatus(context.Background(), CommitStatus{
		Provider:    ProviderGitLab,
		RepoURL:     server.URL + "/platform/tools/api.git",
		CommitSHA:   testCommit,
		Ref:         "refs/heads/feature/login",
		State:       StateFailure,
		TargetURL:   "https://forge.example.com/runs/1",
		Description: "Forge CI: some steps failed",
	}, "glpat-secret", NoRetry)
	if err != nil {
		t.Fatalf("ReportStatus(): %v", err)
	}

	if got, want := server.lastPath.Load(), "/api/v4/projects/platform%2Ftools%2Fapi/statuses/"+testCommit; got != want {
		t.Errorf("path = %v; want %v", got, want)
	}
	want := map[string]string{
		"state":       "failed", // GitLab's word for failure
		"name":        "forge/ci",
		"ref":         "feature/login", // without it the merge request never sees the status
		"target_url":  "https://forge.example.com/runs/1",
		"description": "Forge CI: some steps failed",
	}
	got := server.payload()
	for key, value := range want {
		if got[key] != value {
			t.Errorf("payload[%q] = %q; want %q", key, got[key], value)
		}
	}
	headers := server.headers.Load().(http.Header)
	if headers.Get("PRIVATE-TOKEN") != "glpat-secret" {
		t.Error("the token was not sent in the PRIVATE-TOKEN header")
	}
}

func TestReportStatus_GitLabStates(t *testing.T) {
	for state, want := range map[string]string{
		StatePending: "pending", StateSuccess: "success", StateFailure: "failed", StateError: "canceled",
	} {
		server := newStatusServer(t, response{http.StatusCreated, `{}`})
		err := ReportStatus(context.Background(), CommitStatus{
			Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: state,
		}, "token", NoRetry)
		if err != nil {
			t.Fatalf("state %s: %v", state, err)
		}
		if got := server.payload()["state"]; got != want {
			t.Errorf("state %s sent as %q; want %q", state, got, want)
		}
		if _, hasRef := server.payload()["ref"]; hasRef {
			t.Errorf("state %s: an empty ref was sent", state)
		}
	}
}

func TestReportStatus_GitHub(t *testing.T) {
	server := newStatusServer(t, response{http.StatusCreated, `{}`})

	err := ReportStatus(context.Background(), CommitStatus{
		Provider:    ProviderGitHub,
		RepoURL:     server.URL + "/my-org/api.git",
		CommitSHA:   testCommit,
		Ref:         "refs/heads/main",
		State:       StateSuccess,
		Description: strings.Repeat("é", 200),
	}, "ghp-secret", NoRetry)
	if err != nil {
		t.Fatalf("ReportStatus(): %v", err)
	}
	if got, want := server.lastPath.Load(), "/api/v3/repos/my-org/api/statuses/"+testCommit; got != want {
		t.Errorf("path = %v; want %v", got, want)
	}
	payload := server.payload()
	if payload["state"] != "success" || payload["context"] != "forge/ci" {
		t.Errorf("payload = %v", payload)
	}
	if n := len(payload["description"]); n > gitHubDescriptionLimit {
		t.Errorf("description is %d bytes; GitHub accepts at most %d", n, gitHubDescriptionLimit)
	}
	if !strings.HasSuffix(payload["description"], "é…") {
		t.Errorf("description %q was cut in the middle of a character", payload["description"])
	}
}

func TestReportStatus_RepeatedStateOnGitLabIsSuccess(t *testing.T) {
	server := newStatusServer(t, response{http.StatusBadRequest,
		`{"message":"Cannot transition status via :enqueue from :pending (Reason(s): Status cannot transition via \"enqueue\")"}`})

	err := ReportStatus(context.Background(), CommitStatus{
		Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: StatePending,
	}, "token", fastRetry)
	if err != nil {
		t.Errorf("ReportStatus() = %v; the commit already has this state, so it is not an error", err)
	}
	if calls := server.calls.Load(); calls != 1 {
		t.Errorf("made %d requests; want 1, there is nothing to retry", calls)
	}
}

func TestReportStatus_Retries(t *testing.T) {
	t.Run("temporary failures are retried until one succeeds", func(t *testing.T) {
		server := newStatusServer(t,
			response{http.StatusBadGateway, "restarting"},
			response{http.StatusConflict, "update in progress"},
			response{http.StatusCreated, `{}`},
		)
		err := ReportStatus(context.Background(), CommitStatus{
			Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: StateSuccess,
		}, "token", fastRetry)
		if err != nil {
			t.Errorf("ReportStatus() = %v; want success on the third attempt", err)
		}
		if calls := server.calls.Load(); calls != 3 {
			t.Errorf("made %d requests; want 3", calls)
		}
	})

	t.Run("it gives up when the policy is exhausted", func(t *testing.T) {
		server := newStatusServer(t, response{http.StatusServiceUnavailable, "down"})
		err := ReportStatus(context.Background(), CommitStatus{
			Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: StateSuccess,
		}, "token", fastRetry)
		if err == nil || !strings.Contains(err.Error(), "503") {
			t.Errorf("ReportStatus() = %v; want the last HTTP status reported", err)
		}
		if calls := server.calls.Load(); calls != int32(len(fastRetry.Delays))+1 {
			t.Errorf("made %d requests; want %d", calls, len(fastRetry.Delays)+1)
		}
	})

	t.Run("a rejected request is not retried", func(t *testing.T) {
		for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest} {
			server := newStatusServer(t, response{code, "no"})
			err := ReportStatus(context.Background(), CommitStatus{
				Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: StateSuccess,
			}, "token", fastRetry)
			if err == nil {
				t.Errorf("HTTP %d: ReportStatus() succeeded", code)
			}
			if calls := server.calls.Load(); calls != 1 {
				t.Errorf("HTTP %d: made %d requests; want 1", code, calls)
			}
		}
	})

	t.Run("NoRetry makes one attempt", func(t *testing.T) {
		server := newStatusServer(t, response{http.StatusBadGateway, "restarting"})
		ReportStatus(context.Background(), CommitStatus{
			Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: StatePending,
		}, "token", NoRetry)
		if calls := server.calls.Load(); calls != 1 {
			t.Errorf("made %d requests; want 1", calls)
		}
	})

	t.Run("a canceled context stops the retries", func(t *testing.T) {
		server := newStatusServer(t, response{http.StatusBadGateway, "restarting"})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := ReportStatus(ctx, CommitStatus{
			Provider: ProviderGitLab, RepoURL: server.URL + "/g/r", CommitSHA: testCommit, State: StateSuccess,
		}, "token", RetryPolicy{Delays: []time.Duration{time.Hour}})
		if err == nil || time.Since(start) > 5*time.Second {
			t.Errorf("ReportStatus() = %v after %v; want a prompt error", err, time.Since(start))
		}
	})
}

func TestReportStatus_NetworkErrorDoesNotLeakTheToken(t *testing.T) {
	server := newStatusServer(t, response{http.StatusCreated, `{}`})
	repoURL := server.URL + "/g/r"
	server.Close() // nothing is listening any more

	err := ReportStatus(context.Background(), CommitStatus{
		Provider: ProviderGitLab, RepoURL: repoURL, CommitSHA: testCommit, State: StateSuccess,
	}, "glpat-secret", NoRetry)
	if err == nil {
		t.Fatal("ReportStatus() succeeded against a closed server")
	}
	if strings.Contains(err.Error(), "glpat-secret") {
		t.Errorf("the error contains the token: %v", err)
	}
}

func TestReportStatus_NothingToDo(t *testing.T) {
	status := CommitStatus{RepoURL: "https://gitlab.example.com/g/r", CommitSHA: testCommit, State: StateSuccess}

	for _, provider := range []string{"", "generic", "manual"} {
		status.Provider = provider
		if err := ReportStatus(context.Background(), status, "token", NoRetry); err != nil {
			t.Errorf("provider %q: ReportStatus() = %v; want nil", provider, err)
		}
	}

	status.Provider = ProviderGitLab
	if err := ReportStatus(context.Background(), status, "", NoRetry); !errors.Is(err, ErrNoToken) {
		t.Errorf("without a token: ReportStatus() = %v; want ErrNoToken", err)
	}

	status.CommitSHA = "refs/heads/main"
	if err := ReportStatus(context.Background(), status, "token", NoRetry); err == nil {
		t.Error("ReportStatus accepted something that is not a commit ID")
	}
}

func TestGitLabBaseForRepo(t *testing.T) {
	t.Setenv(EnvGitLabURL, "https://gitlab.enclave.example:8443")

	cases := map[string]string{
		"https://gitlab.enclave.example:8443/g/r.git":   "https://gitlab.enclave.example:8443",
		"http://gitlab.internal/g/r.git":                "http://gitlab.internal",
		"git@gitlab.enclave.example:g/r.git":            "https://gitlab.enclave.example:8443", // SSH URL: use the configured instance
		"ssh://git@gitlab.enclave.example:2222/g/r.git": "https://gitlab.enclave.example:8443", // the SSH port is not the API port
		"git@gitlab.other.example:g/r.git":              "https://gitlab.other.example",
	}
	for repoURL, want := range cases {
		if got := gitLabBaseForRepo(repoURL); got != want {
			t.Errorf("gitLabBaseForRepo(%q) = %q; want %q", repoURL, got, want)
		}
	}
}
