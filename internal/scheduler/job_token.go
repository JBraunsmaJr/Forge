package scheduler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// Job tokens.
//
// A job token is a short-lived credential the scheduler hands to one job when
// it dispatches the job to an agent. It replaces the agent's own API token
// inside the job's container, so that code in a pipeline can act only as
// that job: see jobTokenMayAccess for what a job may do.
//
// A token is "fjt_<payload>.<signature>". The payload is the base64url JSON
// of jobTokenClaims and the signature is its HMAC-SHA256 under the
// scheduler's signing key. Tokens are verified by signature alone, so they
// need no storage and cannot be listed or revoked individually; they simply
// expire.

const (
	// jobTokenPrefix marks a raw token as a job token, so the scheduler can
	// tell it apart from a stored API token without a database lookup.
	jobTokenPrefix = "fjt_"

	// jobTokenEnvVar is the variable a job reads its token from. It is the
	// name scripts already use for the scheduler API.
	jobTokenEnvVar = "FORGE_API_TOKEN"

	// envJobTokenKey names the environment variable holding the signing key.
	// When it is unset the scheduler generates a key at startup, which means
	// tokens issued before a restart stop working. Set it to keep tokens
	// valid across restarts, and to share one key between scheduler replicas.
	envJobTokenKey = "FORGE_JOB_TOKEN_KEY"

	// minJobTokenKeyBytes is the shortest signing key the scheduler accepts.
	minJobTokenKeyBytes = 32

	// jobTokenTTL is how long a job token is valid after it is issued. It
	// must outlast the longest job; it is not the job's time limit.
	jobTokenTTL = 24 * time.Hour
)

// jobTokenClaims is the signed content of a job token.
type jobTokenClaims struct {
	JobID     string `json:"job"`
	RunID     string `json:"run"`
	ProjectID string `json:"proj,omitempty"`
	OrgID     string `json:"org,omitempty"`
	ExpiresAt int64  `json:"exp"` // Unix seconds
}

// jobTokenSigner mints and verifies job tokens with one HMAC key.
type jobTokenSigner struct {
	key []byte
	now func() time.Time
}

// newJobTokenSigner returns a signer for key, which must be at least
// minJobTokenKeyBytes long.
func newJobTokenSigner(key []byte) (*jobTokenSigner, error) {
	if len(key) < minJobTokenKeyBytes {
		return nil, fmt.Errorf("job token key must be at least %d bytes, got %d", minJobTokenKeyBytes, len(key))
	}
	return &jobTokenSigner{key: key, now: time.Now}, nil
}

// jobTokenSignerFromEnv builds the signer from FORGE_JOB_TOKEN_KEY, or from a
// freshly generated key when that is unset. A key that is set but too short
// is an error rather than a silent fallback, so a misconfiguration is seen.
func jobTokenSignerFromEnv() (*jobTokenSigner, error) {
	if configured := os.Getenv(envJobTokenKey); configured != "" {
		signer, err := newJobTokenSigner([]byte(configured))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envJobTokenKey, err)
		}
		return signer, nil
	}
	key := make([]byte, minJobTokenKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating job token key: %w", err)
	}
	return newJobTokenSigner(key)
}

// Mint returns a signed token for claims, valid for jobTokenTTL from now.
func (j *jobTokenSigner) Mint(claims jobTokenClaims) (string, error) {
	if claims.JobID == "" || claims.RunID == "" {
		return "", errors.New("a job token needs a job ID and a run ID")
	}
	claims.ExpiresAt = j.now().Add(jobTokenTTL).Unix()

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return jobTokenPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(j.sign(encoded)), nil
}

// Verify checks a raw token's signature and expiry and returns the token
// record it stands for. A nil signer verifies nothing.
func (j *jobTokenSigner) Verify(rawToken string) (*tokenRecord, bool) {
	if j == nil || !isJobToken(rawToken) {
		return nil, false
	}
	encoded, encodedSig, found := strings.Cut(strings.TrimPrefix(rawToken, jobTokenPrefix), ".")
	if !found {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(encodedSig)
	if err != nil || !hmac.Equal(sig, j.sign(encoded)) {
		return nil, false
	}

	// The signature is good, so the payload is one this scheduler wrote.
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	var claims jobTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	if claims.JobID == "" || j.now().Unix() >= claims.ExpiresAt {
		return nil, false
	}

	return &tokenRecord{
		ID:        "job:" + claims.JobID,
		Name:      "job " + shortID(claims.JobID),
		Role:      roleJob,
		OrgID:     claims.OrgID,
		ProjectID: claims.ProjectID,
		RunID:     claims.RunID,
		JobID:     claims.JobID,
	}, true
}

func (j *jobTokenSigner) sign(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, j.key)
	mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}

// isJobToken reports whether a raw token has the job token form.
func isJobToken(rawToken string) bool {
	return strings.HasPrefix(rawToken, jobTokenPrefix)
}

// attachJobToken mints a token for the job described by spec and places it in
// the job's environment, replacing any value a pipeline set under that name.
// It is called at dispatch, on the copy of the spec sent to the agent.
func (s *Server) attachJobToken(spec *api.JobSpec) {
	if s.jobTokens == nil {
		return
	}
	token, err := s.jobTokens.Mint(jobTokenClaims{
		JobID:     spec.JobID,
		RunID:     spec.RunID,
		ProjectID: spec.ProjectID,
		OrgID:     spec.OrgID,
	})
	if err != nil {
		fmt.Printf("[scheduler] could not mint a token for job %s: %v\n", shortID(spec.JobID), err)
		return
	}

	// Copy the map: the caller's spec may share it with other readers.
	env := make(map[string]string, len(spec.Env)+1)
	for k, v := range spec.Env {
		env[k] = v
	}
	env[jobTokenEnvVar] = token
	spec.Env = env
}
