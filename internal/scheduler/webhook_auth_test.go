package scheduler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyGenericWebhook(t *testing.T) {
	const secret = "forge_wh_example"
	body := []byte(`{"hello":"world"}`)

	tests := []struct {
		name    string
		secret  string
		headers map[string]string
		want    bool
	}{
		{"valid signature", secret, map[string]string{headerForgeSignature: signBody(body, secret)}, true},
		{"valid token", secret, map[string]string{headerForgeToken: secret}, true},
		{"signature made with another secret", secret, map[string]string{headerForgeSignature: signBody(body, "other")}, false},
		{"malformed signature", secret, map[string]string{headerForgeSignature: "nope"}, false},
		{"wrong token", secret, map[string]string{headerForgeToken: "wrong"}, false},
		{"no credential", secret, nil, false},
		{"bad signature is not rescued by a good token", secret, map[string]string{headerForgeSignature: "sha256=00", headerForgeToken: secret}, false},
		{"project without a secret, no credential", "", nil, false},
		{"project without a secret, empty-key signature", "", map[string]string{headerForgeSignature: signBody(body, "")}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/generic/p", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if got := verifyGenericWebhook(req, body, tt.secret); got != tt.want {
				t.Errorf("verifyGenericWebhook() = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestGenericWebhookCredentialPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if genericWebhookCredentialPresent(req) {
		t.Error("a request with neither header reported a credential")
	}
	req.Header.Set(headerForgeToken, "x")
	if !genericWebhookCredentialPresent(req) {
		t.Error("a request with a token header reported no credential")
	}
}

func TestSecretsEqual(t *testing.T) {
	tests := []struct {
		presented, expected string
		want                bool
	}{
		{"s3cret", "s3cret", true},
		{"s3cret", "other", false},
		{"", "s3cret", false},
		{"", "", false},
		{"anything", "", false},
	}
	for _, tt := range tests {
		if got := secretsEqual(tt.presented, tt.expected); got != tt.want {
			t.Errorf("secretsEqual(%q, %q) = %v; want %v", tt.presented, tt.expected, got, tt.want)
		}
	}
}

func TestVerifyGitHubSig_EmptySecret(t *testing.T) {
	body := []byte("payload")
	if verifyGitHubSig(body, "", signBody(body, "")) {
		t.Error("a signature made with an empty key was accepted for a project with no secret")
	}
}

func TestValidCommitRef(t *testing.T) {
	valid := []string{"HEAD", "abc1234", "ABCDEF0", strings.Repeat("a", 40), strings.Repeat("0", 64)}
	for _, ref := range valid {
		if !validCommitRef(ref) {
			t.Errorf("validCommitRef(%q) = false; want true", ref)
		}
	}
	invalid := []string{"", "head", "abc123", "main", "abc1234;id", "$(id)", "abc1234 ", "--upload-pack=x", strings.Repeat("a", 65)}
	for _, ref := range invalid {
		if validCommitRef(ref) {
			t.Errorf("validCommitRef(%q) = true; want false", ref)
		}
	}
}

func TestValidBranchName(t *testing.T) {
	valid := []string{"main", "release/1.2", "feature/JIRA-123_fix", "a", strings.Repeat("b", maxBranchNameLength)}
	for _, name := range valid {
		if !validBranchName(name) {
			t.Errorf("validBranchName(%q) = false; want true", name)
		}
	}
	invalid := []string{
		"", "-flag", ".hidden", "/abs", "a..b", "trailing/", "ref.lock",
		"has space", "semi;colon", "$(id)", "back`tick", "quo\"te", "new\nline",
		strings.Repeat("b", maxBranchNameLength+1),
	}
	for _, name := range invalid {
		if validBranchName(name) {
			t.Errorf("validBranchName(%q) = true; want false", name)
		}
	}
}
