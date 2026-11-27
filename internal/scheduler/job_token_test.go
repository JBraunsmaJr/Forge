package scheduler

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func newTestJobTokenSigner(t *testing.T) *jobTokenSigner {
	t.Helper()
	signer, err := newJobTokenSigner(bytes.Repeat([]byte("k"), minJobTokenKeyBytes))
	if err != nil {
		t.Fatalf("newJobTokenSigner: %v", err)
	}
	return signer
}

func TestJobToken_RoundTrip(t *testing.T) {
	signer := newTestJobTokenSigner(t)

	raw, err := signer.Mint(jobTokenClaims{JobID: "job-1", RunID: "run-1", ProjectID: "proj-1", OrgID: "org-1"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(raw, jobTokenPrefix) {
		t.Errorf("token %q does not start with %q", raw, jobTokenPrefix)
	}

	rec, ok := signer.Verify(raw)
	if !ok {
		t.Fatal("Verify rejected a token the same signer minted")
	}
	want := tokenRecord{
		ID: "job:job-1", Name: "job job-1", Role: roleJob,
		OrgID: "org-1", ProjectID: "proj-1", RunID: "run-1", JobID: "job-1",
	}
	if *rec != want {
		t.Errorf("Verify() = %+v; want %+v", *rec, want)
	}
}

func TestJobToken_MintRequiresJobAndRun(t *testing.T) {
	signer := newTestJobTokenSigner(t)
	if _, err := signer.Mint(jobTokenClaims{RunID: "run-1"}); err == nil {
		t.Error("Mint without a job ID: want an error")
	}
	if _, err := signer.Mint(jobTokenClaims{JobID: "job-1"}); err == nil {
		t.Error("Mint without a run ID: want an error")
	}
}

func TestJobToken_VerifyRejects(t *testing.T) {
	signer := newTestJobTokenSigner(t)
	valid, err := signer.Mint(jobTokenClaims{JobID: "job-1", RunID: "run-1", ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	payload, sig, _ := strings.Cut(strings.TrimPrefix(valid, jobTokenPrefix), ".")

	otherSigner, err := newJobTokenSigner(bytes.Repeat([]byte("x"), minJobTokenKeyBytes))
	if err != nil {
		t.Fatalf("newJobTokenSigner: %v", err)
	}
	foreign, err := otherSigner.Mint(jobTokenClaims{JobID: "job-1", RunID: "run-1"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	// A payload that claims another project, paired with the original signature.
	forgedPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"job":"job-1","run":"run-1","proj":"other-project","exp":99999999999}`))

	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not a job token", "fgt_abcdef"},
		{"prefix only", jobTokenPrefix},
		{"no signature", jobTokenPrefix + payload},
		{"empty signature", jobTokenPrefix + payload + "."},
		{"signature is not base64", jobTokenPrefix + payload + ".!!!"},
		{"signed with another key", foreign},
		{"altered payload, original signature", jobTokenPrefix + forgedPayload + "." + sig},
		{"truncated signature", jobTokenPrefix + payload + "." + sig[:len(sig)-2]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec, ok := signer.Verify(tt.raw); ok {
				t.Errorf("Verify accepted the token as %+v", rec)
			}
		})
	}
}

func TestJobToken_Expiry(t *testing.T) {
	signer := newTestJobTokenSigner(t)
	issued := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return issued }

	raw, err := signer.Mint(jobTokenClaims{JobID: "job-1", RunID: "run-1"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	signer.now = func() time.Time { return issued.Add(jobTokenTTL - time.Second) }
	if _, ok := signer.Verify(raw); !ok {
		t.Error("token rejected one second before it expires")
	}

	signer.now = func() time.Time { return issued.Add(jobTokenTTL) }
	if _, ok := signer.Verify(raw); ok {
		t.Error("token accepted at its expiry time")
	}
}

func TestJobToken_NilSignerVerifiesNothing(t *testing.T) {
	var signer *jobTokenSigner
	if _, ok := signer.Verify(jobTokenPrefix + "abc.def"); ok {
		t.Error("a nil signer accepted a token")
	}
}

func TestNewJobTokenSigner_RejectsShortKey(t *testing.T) {
	if _, err := newJobTokenSigner(bytes.Repeat([]byte("k"), minJobTokenKeyBytes-1)); err == nil {
		t.Error("a key shorter than the minimum was accepted")
	}
}

func TestJobTokenSignerFromEnv(t *testing.T) {
	t.Setenv(envJobTokenKey, "")
	generated, err := jobTokenSignerFromEnv()
	if err != nil || generated == nil {
		t.Fatalf("with no key configured: signer = %v, err = %v; want a generated key", generated, err)
	}

	t.Setenv(envJobTokenKey, "too-short")
	if _, err := jobTokenSignerFromEnv(); err == nil {
		t.Error("a configured key that is too short was accepted")
	}

	t.Setenv(envJobTokenKey, strings.Repeat("a", minJobTokenKeyBytes))
	first, err := jobTokenSignerFromEnv()
	if err != nil {
		t.Fatalf("with a valid configured key: %v", err)
	}
	second, err := jobTokenSignerFromEnv()
	if err != nil {
		t.Fatalf("with a valid configured key: %v", err)
	}
	raw, err := first.Mint(jobTokenClaims{JobID: "job-1", RunID: "run-1"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, ok := second.Verify(raw); !ok {
		t.Error("two signers built from the same configured key do not accept each other's tokens")
	}
}

func TestAttachJobToken(t *testing.T) {
	s := &Server{jobTokens: newTestJobTokenSigner(t)}
	original := map[string]string{"CI": "true", jobTokenEnvVar: "value-set-by-the-pipeline"}
	spec := &api.JobSpec{JobID: "job-1", RunID: "run-1", ProjectID: "proj-1", OrgID: "org-1", Env: original}

	s.attachJobToken(spec)

	rec, ok := s.jobTokens.Verify(spec.Env[jobTokenEnvVar])
	if !ok {
		t.Fatalf("the attached value %q is not a valid job token", spec.Env[jobTokenEnvVar])
	}
	if rec.JobID != "job-1" || rec.RunID != "run-1" || rec.ProjectID != "proj-1" || rec.OrgID != "org-1" {
		t.Errorf("token record = %+v; want the spec's job, run, project, and org", rec)
	}
	if spec.Env["CI"] != "true" {
		t.Error("attachJobToken dropped an existing variable")
	}
	if original[jobTokenEnvVar] != "value-set-by-the-pipeline" {
		t.Error("attachJobToken modified the caller's map instead of a copy")
	}
}

func TestAttachJobToken_DisabledLeavesSpecAlone(t *testing.T) {
	s := &Server{}
	spec := &api.JobSpec{JobID: "job-1", RunID: "run-1"}
	s.attachJobToken(spec)
	if _, present := spec.Env[jobTokenEnvVar]; present {
		t.Error("a token was attached although job tokens are disabled")
	}
}

func TestJobTokenMayAccess(t *testing.T) {
	job := &tokenRecord{Role: roleJob, ProjectID: "proj-1", RunID: "run-1", JobID: "job-1"}

	tests := []struct {
		name   string
		rec    *tokenRecord
		method string
		target string
		want   bool
	}{
		{"own project's source", job, http.MethodGet, "/api/v1/source/proj-1?commit=abc1234", true},
		{"another project's source", job, http.MethodGet, "/api/v1/source/proj-2?commit=abc1234", false},
		{"own project's source, wrong method", job, http.MethodPost, "/api/v1/source/proj-1", false},
		{"path below its source", job, http.MethodGet, "/api/v1/source/proj-1/extra", false},
		{"list runs", job, http.MethodGet, "/api/v1/runs", false},
		{"submit a run", job, http.MethodPost, "/api/v1/runs", false},
		{"lease a job", job, http.MethodPost, "/api/v1/jobs/lease", false},
		{"own project's secrets", job, http.MethodGet, "/api/v1/projects/proj-1/secrets", false},
		{"job with no project", &tokenRecord{Role: roleJob, JobID: "job-1"}, http.MethodGet, "/api/v1/source/", false},
		{"no token record", nil, http.MethodGet, "/api/v1/source/proj-1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			if got := jobTokenMayAccess(tt.rec, req); got != tt.want {
				t.Errorf("jobTokenMayAccess() = %v; want %v", got, tt.want)
			}
		})
	}
}
