// Package catrust makes a Forge process, the tools it runs, and the job
// containers it starts trust additional certificate authorities.
//
// Private networks commonly terminate TLS with an internal CA. Setting
// FORGE_CA_BUNDLE to a PEM file of such CA certificates is enough: Install
// combines it with the system's own trust store into one bundle and points
// Go, git, curl, and the other common clients at it. Nothing has to be
// installed into the operating system's trust store.
package catrust

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// EnvCABundle names the environment variable holding the path of a PEM file
// with the extra CA certificates to trust.
const EnvCABundle = "FORGE_CA_BUNDLE"

// envGoCertFile is the variable Go's TLS stack reads its trust store from on
// Linux. OpenSSL-based tools honor it too.
const envGoCertFile = "SSL_CERT_FILE"

// toolCertVariables are read by common tools to find a CA bundle. Install
// sets each one that is not already set, so an explicit choice is respected.
var toolCertVariables = []string{
	"GIT_SSL_CAINFO",     // git
	"CURL_CA_BUNDLE",     // curl
	"REQUESTS_CA_BUNDLE", // Python requests
	"AWS_CA_BUNDLE",      // AWS CLI and SDKs
}

// systemBundleCandidates are where Linux distributions keep their CA bundle.
var systemBundleCandidates = []string{
	"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu, Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // RHEL, CentOS
	"/etc/ssl/ca-bundle.pem",                            // openSUSE
	"/etc/ssl/cert.pem",                                 // Alpine, macOS
}

const (
	// bundleFileName is the name the bundle is given inside a workspace.
	bundleFileName = "ca-bundle.pem"

	// workspaceCertDir is where the bundle is staged, relative to a job's
	// workspace. The workspace is mounted or copied to containerWorkspace.
	workspaceCertDir   = ".forge/certs"
	containerWorkspace = "/workspace"
)

var (
	mu         sync.RWMutex
	bundlePath string // path of the installed combined bundle; "" if none
)

// Install reads FORGE_CA_BUNDLE and, when it is set, makes this process and
// its child processes trust the certificates in it in addition to the system
// trust store. It returns how many extra certificates were added.
//
// Call it once at the start of main, before any TLS connection is made: Go
// reads its trust store on first use and does not look again.
//
// When FORGE_CA_BUNDLE is unset, Install does nothing and returns 0. A bundle
// that is set but unreadable, or holds no valid certificate, is an error:
// continuing would only move the failure to the first TLS handshake.
func Install() (added int, err error) {
	extraPath := os.Getenv(EnvCABundle)
	if extraPath == "" {
		return 0, nil
	}

	extra, err := os.ReadFile(extraPath)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", EnvCABundle, err)
	}
	added, err = countCertificates(extra)
	if err != nil {
		return 0, fmt.Errorf("%s (%s): %w", EnvCABundle, extraPath, err)
	}

	// A child process inherits SSL_CERT_FILE already pointing at a combined
	// bundle. Appending again would grow the bundle at every level.
	combined := readSystemBundle()
	if !bytes.Contains(combined, bytes.TrimSpace(extra)) {
		combined = append(combined, '\n')
		combined = append(combined, extra...)
	}

	path, err := writeBundle(combined)
	if err != nil {
		return 0, err
	}

	if err := os.Setenv(envGoCertFile, path); err != nil {
		return 0, err
	}
	for _, name := range toolCertVariables {
		if os.Getenv(name) == "" {
			if err := os.Setenv(name, path); err != nil {
				return 0, err
			}
		}
	}

	mu.Lock()
	bundlePath = path
	mu.Unlock()
	return added, nil
}

// BundlePath returns the path of the combined bundle Install wrote, or ""
// when no extra certificates are configured.
func BundlePath() string {
	mu.RLock()
	defer mu.RUnlock()
	return bundlePath
}

// countCertificates returns the number of certificates in PEM data. Blocks
// of other types are ignored; a certificate block that does not parse, or
// data with no certificate at all, is an error.
func countCertificates(pemData []byte) (int, error) {
	count := 0
	rest := pemData
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return 0, fmt.Errorf("certificate %d is not valid: %w", count+1, err)
		}
		count++
	}
	if count == 0 {
		return 0, errors.New("no PEM certificates found")
	}
	return count, nil
}

// readSystemBundle returns the system trust store as PEM: the file named by
// SSL_CERT_FILE when that is already set, otherwise the first standard
// location that exists. It returns nil when there is none, as in a minimal
// container image; the extra certificates are then the whole trust store.
func readSystemBundle() []byte {
	candidates := systemBundleCandidates
	if configured := os.Getenv(envGoCertFile); configured != "" {
		candidates = append([]string{configured}, candidates...)
	}
	for _, path := range candidates {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
	}
	return nil
}

// writeBundle stores the combined bundle in the temporary directory under a
// name derived from its content, so that repeated starts with the same
// certificates reuse one file instead of accumulating copies.
func writeBundle(content []byte) (string, error) {
	sum := sha256.Sum256(content)
	path := filepath.Join(os.TempDir(), "forge-ca-bundle-"+hex.EncodeToString(sum[:8])+".pem")
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(content) {
		return path, nil
	}
	if err := writeFileAtomic(path, content); err != nil {
		return "", fmt.Errorf("writing the combined CA bundle: %w", err)
	}
	return path, nil
}

// writeFileAtomic writes content to path through a temporary file in the
// same directory, so a reader never sees a partial file. The file is world
// readable: it holds only public certificates, and job containers that run
// as another user must be able to read their copy.
func writeFileAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
