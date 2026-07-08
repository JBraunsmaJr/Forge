package localenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile_Basic(t *testing.T) {
	f := writeTmp(t, `
# Comment line
API_KEY=hello
TOKEN=world
EMPTY=
`)
	vals, err := parseEnvFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expect(t, vals, "API_KEY", "hello")
	expect(t, vals, "TOKEN", "world")
	expect(t, vals, "EMPTY", "")
}

func TestParseEnvFile_Quotes(t *testing.T) {
	f := writeTmp(t, `
DOUBLE="value with spaces"
SINGLE='another value'
NESTED="it's fine"
`)
	vals, err := parseEnvFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expect(t, vals, "DOUBLE", "value with spaces")
	expect(t, vals, "SINGLE", "another value")
	expect(t, vals, "NESTED", "it's fine")
}

func TestParseEnvFile_ExportPrefix(t *testing.T) {
	f := writeTmp(t, `export MY_VAR=exported_value`)
	vals, err := parseEnvFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expect(t, vals, "MY_VAR", "exported_value")
}

func TestParseEnvFile_InlineComment(t *testing.T) {
	f := writeTmp(t, `API_KEY=my-value # this is a comment`)
	vals, err := parseEnvFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expect(t, vals, "API_KEY", "my-value")
}

func TestParseEnvFile_QuotedInlineComment(t *testing.T) {

	f := writeTmp(t, `HASH_VALUE="abc#def"`)
	vals, err := parseEnvFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expect(t, vals, "HASH_VALUE", "abc#def")
}

func TestParseEnvFile_MissingFile(t *testing.T) {
	_, err := parseEnvFile("/nonexistent/.env")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestResolver_InlineFlag(t *testing.T) {
	r, err := New(WithSecretFlags([]string{"MY_KEY=flag-value"}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	val, err := r.Resolve("MY_KEY")
	if err != nil || val != "flag-value" {
		t.Errorf("expected 'flag-value', got %q (%v)", val, err)
	}
}

func TestResolver_EnvFile(t *testing.T) {
	f := writeTmp(t, "FILE_KEY=file-value\n")
	r, err := New(WithEnvFile(f))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	val, err := r.Resolve("FILE_KEY")
	if err != nil || val != "file-value" {
		t.Errorf("expected 'file-value', got %q (%v)", val, err)
	}
}

func TestResolver_AutoDotEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("AUTO_KEY=auto-value\n"), 0644)

	r, err := New(WithAutoEnvFile(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	val, err := r.Resolve("AUTO_KEY")
	if err != nil || val != "auto-value" {
		t.Errorf("expected 'auto-value', got %q (%v)", val, err)
	}
}

func TestResolver_AutoDotEnv_Missing(t *testing.T) {

	dir := t.TempDir()
	r, err := New(WithAutoEnvFile(dir))
	if err != nil {
		t.Fatalf("unexpected error when .env is missing: %v", err)
	}
	_ = r
}

func TestResolver_Priority_FlagOverridesFile(t *testing.T) {
	f := writeTmp(t, "KEY=file-value\n")
	r, err := New(
		WithSecretFlags([]string{"KEY=flag-value"}),
		WithEnvFile(f),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	val, err := r.Resolve("KEY")
	if err != nil || val != "flag-value" {
		t.Errorf("expected flag value 'flag-value', got %q (%v)", val, err)
	}
}

func TestResolver_Priority_ExplicitFileOverridesAutoEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("KEY=auto-value\n"), 0644)

	explicit := writeTmp(t, "KEY=explicit-value\n")

	r, err := New(
		WithEnvFile(explicit),
		WithAutoEnvFile(dir),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	val, err := r.Resolve("KEY")
	if err != nil || val != "explicit-value" {
		t.Errorf("expected 'explicit-value', got %q (%v)", val, err)
	}
}

func TestResolver_VaultFallback(t *testing.T) {
	called := false
	r, err := New(WithVault(func(name string) (string, error) {
		called = true
		if name == "VAULT_SECRET" {
			return "vault-value", nil
		}
		return "", nil
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	val, err := r.Resolve("VAULT_SECRET")
	if err != nil || val != "vault-value" {
		t.Errorf("expected 'vault-value', got %q (%v)", val, err)
	}
	if !called {
		t.Error("vault fallback was not called")
	}
}

func TestResolver_NotFound(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = r.Resolve("NONEXISTENT")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}

	msg := err.Error()
	if !contains(msg, "--secret") {
		t.Errorf("error message should mention --secret flag, got:\n%s", msg)
	}
	if !contains(msg, "--env-file") {
		t.Errorf("error message should mention --env-file flag, got:\n%s", msg)
	}
}

func TestResolver_InvalidFlagFormat(t *testing.T) {
	_, err := New(WithSecretFlags([]string{"NO_EQUALS_SIGN"}))
	if err == nil {
		t.Fatal("expected error for missing = in secret flag")
	}
}

func TestResolver_ResolveAll(t *testing.T) {
	r, err := New(WithSecretFlags([]string{"A=1", "B=2"}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	vals, err := r.ResolveAll([]string{"A", "B"})
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}
	if vals["A"] != "1" || vals["B"] != "2" {
		t.Errorf("unexpected values: %v", vals)
	}
}

func TestResolver_ResolveAll_MissingOne(t *testing.T) {
	r, err := New(WithSecretFlags([]string{"A=1"}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = r.ResolveAll([]string{"A", "MISSING"})
	if err == nil {
		t.Fatal("expected error when one secret is missing")
	}
}

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.env")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func expect(t *testing.T, m map[string]string, key, want string) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("key %q missing from map", key)
		return
	}
	if got != want {
		t.Errorf("key %q: expected %q, got %q", key, want, got)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
