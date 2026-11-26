package scheduler

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Headers that authenticate a request to the generic webhook endpoint. A
// caller sends one of them; both are checked against the project's webhook
// secret.
const (
	// headerForgeSignature carries "sha256=<hex>", the HMAC-SHA256 of the
	// request body keyed with the webhook secret. This is the same scheme
	// GitHub uses, and it keeps the secret itself off the wire.
	headerForgeSignature = "X-Forge-Signature-256"

	// headerForgeToken carries the webhook secret itself. It is simpler to
	// produce from a script and should only be used over TLS.
	headerForgeToken = "X-Forge-Token"
)

// maxBranchNameLength bounds the branch names accepted from a webhook.
const maxBranchNameLength = 255

// genericWebhookCredentialPresent reports whether the request carries either
// generic webhook credential. It lets the handler refuse a request that has
// nothing to verify before it looks anything up.
func genericWebhookCredentialPresent(r *http.Request) bool {
	return r.Header.Get(headerForgeSignature) != "" || r.Header.Get(headerForgeToken) != ""
}

// verifyGenericWebhook reports whether the request proves knowledge of the
// project's webhook secret, by signature or by token. A project with no
// secret cannot be triggered through the generic endpoint.
func verifyGenericWebhook(r *http.Request, body []byte, secret string) bool {
	if secret == "" {
		return false
	}
	if sig := r.Header.Get(headerForgeSignature); sig != "" {
		return verifyGitHubSig(body, secret, sig)
	}
	return secretsEqual(r.Header.Get(headerForgeToken), secret)
}

// secretsEqual compares a presented secret with the expected one in constant
// time. An empty expected secret never matches, so an unset secret cannot be
// satisfied by an absent header.
func secretsEqual(presented, expected string) bool {
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// validCommitRef reports whether ref is "HEAD" or a hexadecimal commit ID of
// 7 to 64 characters. Values from a webhook's query string are untrusted and
// are later passed to git and to a job's shell command.
func validCommitRef(ref string) bool {
	if ref == "HEAD" {
		return true
	}
	if len(ref) < 7 || len(ref) > 64 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// validBranchName reports whether name is a plain branch name: letters,
// digits, '.', '_', '-' and '/', not starting with '-', '.' or '/', and with
// no ".." sequence. This is stricter than git itself, which also allows
// characters that are meaningful to a shell.
func validBranchName(name string) bool {
	if name == "" || len(name) > maxBranchNameLength {
		return false
	}
	if strings.Contains(name, "..") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-' || c == '/'):
		default:
			return false
		}
	}
	return true
}
