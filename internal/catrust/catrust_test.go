package catrust

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestCA returns a freshly generated self-signed CA certificate in PEM.
func newTestCA(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// isolate gives a test its own temporary directory and a clean slate of the
// variables and package state that Install touches.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv(EnvCABundle, "")
	t.Setenv(envGoCertFile, "")
	for _, name := range toolCertVariables {
		t.Setenv(name, "")
	}
	mu.Lock()
	bundlePath = ""
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		bundlePath = ""
		mu.Unlock()
	})
	return dir
}

func writeFile(t *testing.T, path string, content []byte) string {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestInstall_UnsetDoesNothing(t *testing.T) {
	isolate(t)

	added, err := Install()
	if err != nil || added != 0 {
		t.Fatalf("Install() = %d, %v; want 0, nil", added, err)
	}
	if BundlePath() != "" {
		t.Errorf("BundlePath() = %q; want empty", BundlePath())
	}
	if got := os.Getenv(envGoCertFile); got != "" {
		t.Errorf("%s was set to %q", envGoCertFile, got)
	}
}

func TestInstall_CombinesSystemAndExtra(t *testing.T) {
	dir := isolate(t)
	systemCA := newTestCA(t, "System Root")
	extraCA := append(newTestCA(t, "Enclave Root"), newTestCA(t, "Enclave Issuing")...)

	t.Setenv(envGoCertFile, writeFile(t, filepath.Join(dir, "system.pem"), systemCA))
	t.Setenv(EnvCABundle, writeFile(t, filepath.Join(dir, "extra.pem"), extraCA))
	t.Setenv("GIT_SSL_CAINFO", "/already/chosen.pem")

	added, err := Install()
	if err != nil {
		t.Fatalf("Install(): %v", err)
	}
	if added != 2 {
		t.Errorf("Install() added %d certificates; want 2", added)
	}

	path := BundlePath()
	combined, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the combined bundle: %v", err)
	}
	if !bytes.Contains(combined, systemCA) || !bytes.Contains(combined, extraCA) {
		t.Error("the combined bundle does not hold both the system and the extra certificates")
	}
	if n, err := countCertificates(combined); err != nil || n != 3 {
		t.Errorf("combined bundle has %d certificates (%v); want 3", n, err)
	}

	if got := os.Getenv(envGoCertFile); got != path {
		t.Errorf("%s = %q; want the combined bundle %q", envGoCertFile, got, path)
	}
	if got := os.Getenv("CURL_CA_BUNDLE"); got != path {
		t.Errorf("CURL_CA_BUNDLE = %q; want the combined bundle", got)
	}
	if got := os.Getenv("GIT_SSL_CAINFO"); got != "/already/chosen.pem" {
		t.Errorf("GIT_SSL_CAINFO = %q; an explicit setting must be kept", got)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(combined) {
		t.Error("Go cannot load the combined bundle")
	}
}

func TestInstall_IsStableAcrossCalls(t *testing.T) {
	dir := isolate(t)
	t.Setenv(EnvCABundle, writeFile(t, filepath.Join(dir, "extra.pem"), newTestCA(t, "Enclave Root")))

	if _, err := Install(); err != nil {
		t.Fatalf("first Install(): %v", err)
	}
	first := BundlePath()

	// A second process start sees SSL_CERT_FILE already pointing at the
	// combined bundle. The result must still be one usable bundle.
	if _, err := Install(); err != nil {
		t.Fatalf("second Install(): %v", err)
	}
	combined, err := os.ReadFile(BundlePath())
	if err != nil {
		t.Fatalf("reading the combined bundle: %v", err)
	}
	if _, err := countCertificates(combined); err != nil {
		t.Errorf("the bundle after a second Install is not valid: %v", err)
	}
	if first == "" {
		t.Error("the first Install did not record a bundle path")
	}
	if second := BundlePath(); second != first {
		t.Errorf("a second Install produced a different bundle (%q, then %q); the extra certificates were appended twice", first, second)
	}
}

func TestInstall_Errors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		dir := isolate(t)
		t.Setenv(EnvCABundle, filepath.Join(dir, "does-not-exist.pem"))
		if _, err := Install(); err == nil {
			t.Error("want an error for a bundle that does not exist")
		}
	})
	t.Run("no certificates", func(t *testing.T) {
		dir := isolate(t)
		t.Setenv(EnvCABundle, writeFile(t, filepath.Join(dir, "empty.pem"), []byte("not a certificate\n")))
		_, err := Install()
		if err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
			t.Errorf("Install() error = %v; want one about missing certificates", err)
		}
	})
	t.Run("corrupt certificate", func(t *testing.T) {
		dir := isolate(t)
		corrupt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})
		t.Setenv(EnvCABundle, writeFile(t, filepath.Join(dir, "bad.pem"), corrupt))
		if _, err := Install(); err == nil {
			t.Error("want an error for a certificate that does not parse")
		}
		if BundlePath() != "" {
			t.Error("a failed Install must not leave a bundle installed")
		}
	})
}

func TestCountCertificates_IgnoresOtherBlocks(t *testing.T) {
	data := append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")}), newTestCA(t, "Root")...)
	if n, err := countCertificates(data); err != nil || n != 1 {
		t.Errorf("countCertificates() = %d, %v; want 1, nil", n, err)
	}
}

func TestStageInWorkspaceAndContainerEnv(t *testing.T) {
	dir := isolate(t)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing configured: nothing staged, no variables.
	if err := StageInWorkspace(workspace); err != nil {
		t.Fatalf("StageInWorkspace with nothing configured: %v", err)
	}
	if env := ContainerEnv(workspace); env != nil {
		t.Errorf("ContainerEnv() = %v; want none before a bundle is staged", env)
	}

	t.Setenv(EnvCABundle, writeFile(t, filepath.Join(dir, "extra.pem"), newTestCA(t, "Enclave Root")))
	if _, err := Install(); err != nil {
		t.Fatalf("Install(): %v", err)
	}
	if err := StageInWorkspace(workspace); err != nil {
		t.Fatalf("StageInWorkspace: %v", err)
	}

	staged, err := os.ReadFile(filepath.Join(workspace, ".forge", "certs", "ca-bundle.pem"))
	if err != nil {
		t.Fatalf("the bundle was not staged: %v", err)
	}
	installed, _ := os.ReadFile(BundlePath())
	if !bytes.Equal(staged, installed) {
		t.Error("the staged bundle differs from the installed one")
	}

	env := ContainerEnv(workspace)
	if len(env) != len(containerCertVariables) {
		t.Fatalf("ContainerEnv() returned %d variables; want %d", len(env), len(containerCertVariables))
	}
	const want = "/workspace/.forge/certs/ca-bundle.pem"
	for i, v := range env {
		if v.Name != containerCertVariables[i] || v.Value != want {
			t.Errorf("ContainerEnv()[%d] = %+v; want %s=%s", i, v, containerCertVariables[i], want)
		}
	}

	if env := ContainerEnv(""); env != nil {
		t.Errorf("ContainerEnv(\"\") = %v; want none", env)
	}
}
