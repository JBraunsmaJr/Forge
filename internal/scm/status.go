package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Commit status states, in GitHub's vocabulary. They are translated for
// GitLab when the status is sent.
const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
)

// DefaultStatusContext is the name a Forge status is reported under. It is
// the name to require in a branch protection rule.
const DefaultStatusContext = "forge/ci"

// CommitStatus is the result of a run, as shown on the commit and on any
// pull or merge request that contains it.
type CommitStatus struct {
	Provider  string // ProviderGitHub or ProviderGitLab
	RepoURL   string
	CommitSHA string
	// Ref is the branch or tag the run is for, in short or full form. GitLab
	// uses it to attach the status to the right pipeline of a merge
	// request; GitHub ignores it.
	Ref         string
	State       string
	TargetURL   string
	Description string
	Context     string // defaults to DefaultStatusContext
}

// RetryPolicy says how often and how patiently a status report is retried
// after a failure that may be temporary.
type RetryPolicy struct {
	// Delays are the waits before each retry. Empty means one attempt.
	Delays []time.Duration
}

var (
	// NoRetry makes a single attempt. Use it for a "pending" status, where
	// a late retry could overwrite the final result.
	NoRetry = RetryPolicy{}

	// FinalStatusRetry keeps trying for about fifteen minutes. A final
	// status that is never delivered leaves a merge request blocked, so it
	// is worth being patient through a restart of the Git host.
	FinalStatusRetry = RetryPolicy{Delays: []time.Duration{
		5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute, 8 * time.Minute,
	}}
)

// statusError is a failed status report. Temporary failures are retried.
type statusError struct {
	HTTPStatus int // 0 for a network error
	Temporary  bool
	Detail     string
}

func (e *statusError) Error() string {
	if e.HTTPStatus == 0 {
		return "reporting commit status: " + e.Detail
	}
	return fmt.Sprintf("reporting commit status: HTTP %d: %s", e.HTTPStatus, e.Detail)
}

// ErrNoToken is returned when a project has no token to report with.
var ErrNoToken = errors.New("no SCM token configured")

// ReportStatus sends a commit status to the Git host, retrying temporary
// failures as policy says. It returns nil for a provider that has no commit
// statuses (a generic webhook or a manual run).
func ReportStatus(ctx context.Context, status CommitStatus, token string, policy RetryPolicy) error {
	if status.Provider != ProviderGitHub && status.Provider != ProviderGitLab {
		return nil
	}
	if token == "" {
		return ErrNoToken
	}

	request, err := newStatusRequest(status)
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		err := request.send(ctx, token)
		if err == nil {
			return nil
		}
		var failure *statusError
		if !errors.As(err, &failure) || !failure.Temporary || attempt >= len(policy.Delays) {
			return err
		}

		timer := time.NewTimer(policy.Delays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (gave up: %v)", err, ctx.Err())
		case <-timer.C:
		}
	}
}

// statusRequest is a status report ready to send to one provider.
type statusRequest struct {
	provider string
	url      string
	payload  map[string]string
}

// Length limits the providers put on a status.
const (
	gitHubDescriptionLimit = 140
	gitLabFieldLimit       = 255
)

func newStatusRequest(status CommitStatus) (*statusRequest, error) {
	repoPath := getRepoPath(status.RepoURL)
	if repoPath == "" {
		return nil, fmt.Errorf("could not parse repo path from %s", status.RepoURL)
	}
	if !isCommitID(status.CommitSHA) {
		return nil, fmt.Errorf("%q is not a commit ID", status.CommitSHA)
	}
	statusContext := status.Context
	if statusContext == "" {
		statusContext = DefaultStatusContext
	}

	switch status.Provider {
	case ProviderGitHub:
		return &statusRequest{
			provider: ProviderGitHub,
			url:      fmt.Sprintf("%s/repos/%s/statuses/%s", gitHubAPIBaseForRepo(status.RepoURL), repoPath, status.CommitSHA),
			payload: map[string]string{
				"state":       status.State,
				"target_url":  status.TargetURL,
				"description": truncate(status.Description, gitHubDescriptionLimit),
				"context":     statusContext,
			},
		}, nil

	case ProviderGitLab:
		payload := map[string]string{
			"state":       gitLabState(status.State),
			"description": truncate(status.Description, gitLabFieldLimit),
			"name":        statusContext,
		}
		// GitLab rejects a target URL longer than its limit; a status
		// without a link is better than no status.
		if status.TargetURL != "" && len(status.TargetURL) <= gitLabFieldLimit {
			payload["target_url"] = status.TargetURL
		}
		// Without a ref GitLab attaches the status to the default branch,
		// and a merge request for another branch never sees it.
		if ref := shortRefName(status.Ref); ref != "" {
			payload["ref"] = ref
		}
		return &statusRequest{
			provider: ProviderGitLab,
			url: fmt.Sprintf("%s/api/v4/projects/%s/statuses/%s",
				gitLabBaseForRepo(status.RepoURL), url.PathEscape(repoPath), status.CommitSHA),
			payload: payload,
		}, nil
	}
	return nil, fmt.Errorf("unsupported provider %q", status.Provider)
}

// send makes one attempt and classifies the outcome.
func (r *statusRequest) send(ctx context.Context, token string) error {
	body, err := json.Marshal(r.payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Forge-CI")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("PRIVATE-TOKEN", token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Never include the request in the message; it carries the token.
		return &statusError{Temporary: true, Detail: "request failed: " + networkErrorSummary(err)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode < 300:
		return nil
	case r.provider == ProviderGitLab && resp.StatusCode == http.StatusBadRequest &&
		strings.Contains(string(respBody), "Cannot transition status"):
		// GitLab refuses to set a status to the state it already has.
		// The commit shows what was intended, so this is success.
		return nil
	case resp.StatusCode == http.StatusConflict, // GitLab: another update is in progress
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500:
		return &statusError{HTTPStatus: resp.StatusCode, Temporary: true, Detail: strings.TrimSpace(string(respBody))}
	default:
		return &statusError{HTTPStatus: resp.StatusCode, Detail: strings.TrimSpace(string(respBody))}
	}
}

// networkErrorSummary describes a transport error without the URL, which
// url.Error would otherwise include.
func networkErrorSummary(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

// gitLabState translates a state from GitHub's vocabulary to GitLab's.
func gitLabState(state string) string {
	switch state {
	case StateFailure:
		return "failed"
	case StateError:
		return "canceled"
	}
	return state
}

// gitLabBaseForRepo returns the base URL of the GitLab instance that hosts
// repoURL. An HTTP(S) clone URL carries it. An SSH clone URL only carries
// the host, so the configured instance is used when the host matches it,
// and HTTPS on that host otherwise.
func gitLabBaseForRepo(repoURL string) string {
	if u, err := url.Parse(strings.TrimSpace(repoURL)); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	configured := GitLabURL()
	host := RepoHost(repoURL)
	// An SSH URL may name a port (ssh://git@host:2222/...), which is the
	// SSH port and says nothing about where the API is.
	hostname, _, _ := strings.Cut(host, ":")
	configuredHostname, _, _ := strings.Cut(hostOf(configured), ":")
	if hostname == "" || hostname == configuredHostname {
		return configured
	}
	return "https://" + hostname
}

// shortRefName turns "refs/heads/main" or "refs/tags/v1" into "main" or
// "v1", and leaves a short name as it is.
func shortRefName(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return strings.TrimPrefix(ref, "refs/tags/")
}

// truncate shortens text to at most limit bytes without cutting a
// character in half.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit - len("…")
	for cut > 0 && !isRuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// isCommitID reports whether s looks like a full or abbreviated commit ID.
func isCommitID(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
