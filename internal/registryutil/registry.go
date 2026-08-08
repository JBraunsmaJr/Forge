// Package registryutil implements just enough of the Docker Registry
// HTTP API V2 (distribution spec) to support the docker_publish step
// type (issue #57): reading an already-pushed image's manifest and
// writing it back under new tags. Since a manifest's referenced blobs
// already exist in the registry (they were pushed under the source
// tag), writing it under a new tag is a promotion, not a rebuild or
// re-upload — the same mechanism `docker buildx imagetools create`
// and `crane tag` use.
package registryutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// tagPattern matches Docker's own tag-name grammar (see
// docker/distribution/reference): a tag must start with a word
// character (letter, digit, or underscore) and contain only word
// characters, periods, and hyphens after that, up to 128 characters
// total. Notably: no slashes — a slash here almost always means
// something upstream (a literal segment of a configured build-number
// format, a raw branch name, etc.) leaked into what's meant to be just
// the tag component of an image reference, not a full "repo/path:tag".
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)

// ValidTag reports whether tag is valid as a Docker image tag on its
// own — just the tag component, not a full "repository:tag" reference.
// docker_publish uses this to validate its resolved source and target
// tags (after ${{ env.* }} interpolation) before ever calling the
// registry: buildnumber.Parse only validates a format string's token
// structure, not whether what it (or any other part of the step's
// configuration) actually renders to is a valid tag — a format like
// "release/%counter%" parses just fine and renders a slash straight
// into the tag.
func ValidTag(tag string) bool {
	return tagPattern.MatchString(tag)
}

// ManifestMediaTypes is sent as the Accept header on every manifest
// GET, matching what `docker pull`/`docker push` accept: single-arch
// manifests (Docker v2 / OCI) and multi-arch manifest lists/indexes.
var ManifestMediaTypes = []string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
}

// ErrDeletionUnsupported is returned by DeleteTag when the registry
// doesn't support (or has disabled) manifest deletion — Docker Hub's
// public API is the most common example. A docker_publish step treats
// this as a warning, not a failure (issue #57).
var ErrDeletionUnsupported = errors.New("registry does not support tag deletion")

// Client is a minimal Docker Registry HTTP API V2 client.
type Client struct {
	// BaseURL is the registry's HTTPS distribution API endpoint, e.g.
	// "https://ghcr.io" — see NormalizeRegistryHost for turning a plain
	// registry hostname into this.
	BaseURL  string
	Username string
	Password string

	httpClient *http.Client

	// token caches a bearer token already obtained for a given
	// "repository:scope" key, so repeated calls against the same
	// repository during one docker_publish step don't re-authenticate
	// on every request.
	token map[string]string
}

// NewClient creates a registry client. username/password may both be
// empty for an anonymous-read registry; many registries still require
// a scoped bearer token even for anonymous pulls, which doAuthed
// negotiates automatically regardless of whether credentials are set.
func NewClient(baseURL, username, password string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Username: username,
		Password: password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// Refuse to follow a redirect that downgrades to plain
			// HTTP: Authorization headers (Basic credentials, bearer
			// tokens) would otherwise be replayed in cleartext.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing to follow redirect to non-HTTPS URL: %s", req.URL)
				}
				return nil
			},
		},
		token: make(map[string]string),
	}
}

// NormalizeRegistryHost turns a plain registry hostname (as a user
// would type it in a docker_publish step's registry: field, e.g.
// "ghcr.io" or "docker.io") into the base URL its distribution API
// actually lives at, adding an https:// scheme. Docker Hub is the one
// common registry whose API host differs from its familiar name.
func NormalizeRegistryHost(registry string) string {
	registry = strings.TrimSuffix(registry, "/")
	registry = strings.TrimPrefix(registry, "https://")
	registry = strings.TrimPrefix(registry, "http://")
	switch registry {
	case "", "docker.io", "index.docker.io":
		registry = "registry-1.docker.io"
	}
	return "https://" + registry
}

// Manifest is a fetched image manifest: its raw bytes (opaque — never
// re-parsed, just written back verbatim under a new tag), the
// Content-Type required on the matching PUT, and its digest.
type Manifest struct {
	Body        []byte
	ContentType string
	Digest      string
}

// GetManifest fetches the manifest for repository:reference, where
// reference is a tag (for reading the source tag) or a digest (for
// re-checking after a promotion). ctx allows an in-flight call to be
// canceled (e.g. server shutdown) rather than running to completion.
func (c *Client) GetManifest(ctx context.Context, repository, reference string) (*Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.manifestURL(repository, reference), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join(ManifestMediaTypes, ", "))

	resp, err := c.doAuthed(ctx, req, repository, "pull")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get manifest %s:%s: registry returned %s: %s", repository, reference, resp.Status, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read manifest body: %w", err)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		digest = sha256Digest(body)
	}

	return &Manifest{
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		Digest:      digest,
	}, nil
}

// PutManifest writes an already-fetched manifest under a new tag. No
// image content is re-uploaded — this is exactly what makes tag
// promotion fast and rebuild-free.
func (c *Client) PutManifest(ctx context.Context, repository, tag string, m *Manifest) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.manifestURL(repository, tag), bytes.NewReader(m.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", m.ContentType)

	resp, err := c.doAuthed(ctx, req, repository, "push,pull")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put manifest %s:%s: registry returned %s: %s", repository, tag, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// DeleteTag deletes repository's tag reference. Registries interpret a
// DELETE against a *tag* reference as unlinking just that tag; a DELETE
// against a *digest* reference removes the underlying manifest and
// cascades to every tag pointing at it — the wrong operation whenever
// other tags might share that digest (which is always true immediately
// after a docker_publish promotion: the source and every freshly
// promoted target tag point at the identical digest). Callers must pass
// the tag name, never a resolved digest.
//
// Even tag-reference deletion isn't universally guaranteed safe: some
// registry implementations resolve the tag to its digest internally
// before deleting, and would cascade-delete other tags anyway. Forge's
// own docker_publish step does not call this during a delete_source
// promotion for exactly that reason — see executeDockerPublish.
//
// Returns ErrDeletionUnsupported (never a generic error) when the
// registry rejects the delete for lack of support rather than a real
// problem, so callers can turn that into a non-fatal warning per the
// spec: "if the target registry doesn't support tag deletion ... record
// a warning and shall not fail the job by default."
func (c *Client) DeleteTag(ctx context.Context, repository, tag string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.manifestURL(repository, tag), nil)
	if err != nil {
		return err
	}

	resp, err := c.doAuthed(ctx, req, repository, "push,pull")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusMethodNotAllowed, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotImplemented:
		return ErrDeletionUnsupported
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete manifest %s:%s: registry returned %s: %s", repository, tag, resp.Status, strings.TrimSpace(string(body)))
	}
}

func (c *Client) manifestURL(repository, reference string) string {
	return fmt.Sprintf("%s/v2/%s/manifests/%s", c.BaseURL, repository, reference)
}

// doAuthed sends req, transparently handling the registry's bearer
// token challenge (the standard flow every major registry — Docker
// Hub, GHCR, ECR, GCR, ACR, a private Harbor/Distribution instance —
// implements): an unauthenticated request gets back a 401 with a
// WWW-Authenticate header describing where to fetch a token; doAuthed
// fetches it, retries once with it attached, and caches it for
// subsequent calls against the same repository+scope within this
// Client's lifetime (one docker_publish step run).
func (c *Client) doAuthed(ctx context.Context, req *http.Request, repository, scope string) (*http.Response, error) {
	cacheKey := repository + ":" + scope

	if tok, ok := c.token[cacheKey]; ok {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	if !strings.HasPrefix(challenge, "Bearer ") {
		// No bearer challenge to act on (e.g. basic-auth-only registry
		// that just rejected the credentials) — return the 401 as-is.
		return resp, nil
	}

	tok, err := c.fetchToken(ctx, challenge, repository, scope)
	if err != nil {
		return nil, fmt.Errorf("registry auth: %w", err)
	}
	c.token[cacheKey] = tok

	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("rewind request body for retry: %w", err)
		}
		retry.Body = body
	}
	retry.Header.Set("Authorization", "Bearer "+tok)

	return c.httpClient.Do(retry)
}

// fetchToken parses a `Bearer realm="...",service="...",scope="..."`
// WWW-Authenticate challenge and fetches a token from the named realm,
// per the Docker Registry v2 token authentication spec.
func (c *Client) fetchToken(ctx context.Context, challenge, repository, scope string) (string, error) {
	params := parseBearerChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("bearer challenge missing realm: %s", challenge)
	}

	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("invalid realm %q: %w", realm, err)
	}
	if u.Scheme != "https" {
		// The realm comes from the registry's own WWW-Authenticate
		// response — an external, not-fully-trusted source. Sending
		// Basic credentials to a non-HTTPS realm would leak them in
		// cleartext, whether that's a misconfigured registry or an
		// active downgrade attempt.
		return "", fmt.Errorf("refusing to fetch a token from a non-HTTPS realm: %s", realm)
	}
	q := u.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	reqScope := params["scope"]
	if reqScope == "" && repository != "" {
		reqScope = fmt.Sprintf("repository:%s:%s", repository, scope)
	}
	if reqScope != "" {
		q.Set("scope", reqScope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token request to %s returned %s: %s", realm, resp.Status, strings.TrimSpace(string(body)))
	}

	// Registries are inconsistent about which of these two keys they
	// use for the same thing; accept either.
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	if payload.AccessToken != "" {
		return payload.AccessToken, nil
	}
	return "", fmt.Errorf("token response from %s had no token", realm)
}

// parseBearerChallenge parses a WWW-Authenticate header's parameters
// (after the "Bearer " prefix) into a key/value map.
func parseBearerChallenge(header string) map[string]string {
	header = strings.TrimPrefix(header, "Bearer ")
	params := make(map[string]string)
	for _, part := range splitChallengeParams(header) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		params[key] = val
	}
	return params
}

// splitChallengeParams splits a comma-separated list of key="value"
// pairs, respecting commas inside quoted values.
func splitChallengeParams(s string) []string {
	var parts []string
	var buf strings.Builder
	inQuotes := false
	for _, r := range s {
		switch r {
		case '"':
			inQuotes = !inQuotes
			buf.WriteRune(r)
		case ',':
			if inQuotes {
				buf.WriteRune(r)
			} else {
				parts = append(parts, buf.String())
				buf.Reset()
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		parts = append(parts, buf.String())
	}
	return parts
}

func sha256Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
