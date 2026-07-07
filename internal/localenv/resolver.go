// Package localenv resolves secrets for local pipeline runs (forge run).
//
// Resolution order (highest priority first):
//
//  1. --secret KEY=VALUE flags on the command line
//  2. --env-file <path> (explicit file path)
//  3. .env in the working directory (auto-detected, optional)
//  4. Vault (if FORGE_VAULT_ADDR + FORGE_VAULT_TOKEN are configured)
//
// If a secret name cannot be found in any source, Resolve returns an error
// with a clear message showing which flags to use.
//
// # .env file format
//
// Standard dotenv format: one KEY=VALUE per line.
//
//	# Comments are supported
//	API_KEY=my-secret-value
//	DATABASE_URL=postgres://localhost:5432/dev
//	TOKEN="value with spaces"
//	QUOTED_SINGLE='another value'
//	EXPORT export ALSO_WORKS=yes
//
// Blank lines are ignored. Lines starting with # are comments.
// Values may be quoted with " or ' (quotes are stripped, no escape processing).
// The `export` prefix is accepted and ignored.
package localenv

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Resolver resolves secret names to values using a priority chain of sources.
type Resolver struct {
	// inlineSecrets holds values from --secret KEY=VALUE flags.
	inlineSecrets map[string]string
	// envFileSecrets holds values from --env-file or auto-detected .env.
	envFileSecrets map[string]string
	// vaultFallback is called when a secret isn't found in local sources.
	// nil means Vault is not configured.
	vaultFallback func(name string) (string, error)

	// sources is a human-readable description of configured sources,
	// used in error messages.
	sources []string
}

// Option configures a Resolver.
type Option func(*Resolver) error

// WithSecretFlags accepts the values of all --secret KEY=VALUE flags.
func WithSecretFlags(pairs []string) Option {
	return func(r *Resolver) error {
		for _, pair := range pairs {
			idx := strings.IndexByte(pair, '=')
			if idx < 1 {
				return fmt.Errorf("--secret %q: expected KEY=VALUE format", pair)
			}
			key := strings.TrimSpace(pair[:idx])
			val := pair[idx+1:]
			if key == "" {
				return fmt.Errorf("--secret %q: key cannot be empty", pair)
			}
			r.inlineSecrets[key] = val
		}
		if len(pairs) > 0 {
			r.sources = append(r.sources, fmt.Sprintf("--secret flags (%d value(s))", len(pairs)))
		}
		return nil
	}
}

// WithEnvFile loads an explicit --env-file path.
// Returns an error if the file doesn't exist or can't be parsed.
func WithEnvFile(path string) Option {
	return func(r *Resolver) error {
		values, err := parseEnvFile(path)
		if err != nil {
			return fmt.Errorf("--env-file %s: %w", path, err)
		}
		for k, v := range values {
			r.envFileSecrets[k] = v
		}
		r.sources = append(r.sources, fmt.Sprintf("env file %s (%d value(s))", path, len(values)))
		return nil
	}
}

// WithAutoEnvFile looks for a .env file in the given directory.
// Unlike WithEnvFile, a missing .env is not an error — it's silently skipped.
func WithAutoEnvFile(dir string) Option {
	return func(r *Resolver) error {
		path := dir + "/.env"
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil // not an error — .env is optional
		}
		values, err := parseEnvFile(path)
		if err != nil {
			return fmt.Errorf("auto-detected .env: %w", err)
		}
		for k, v := range values {
			// Auto-detected .env has lower priority than explicit --env-file.
			// Only set if not already set by a higher-priority source.
			if _, exists := r.envFileSecrets[k]; !exists {
				r.envFileSecrets[k] = v
			}
		}
		if len(values) > 0 {
			r.sources = append(r.sources, fmt.Sprintf(".env (%d value(s))", len(values)))
		}
		return nil
	}
}

// WithVault adds Vault as a fallback. fn is called when no local source has the secret.
func WithVault(fn func(name string) (string, error)) Option {
	return func(r *Resolver) error {
		r.vaultFallback = fn
		r.sources = append(r.sources, "Vault")
		return nil
	}
}

// New creates a Resolver with the given options applied in order.
func New(opts ...Option) (*Resolver, error) {
	r := &Resolver{
		inlineSecrets:  make(map[string]string),
		envFileSecrets: make(map[string]string),
	}
	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Resolve returns the value for a secret name, walking the priority chain.
// Returns a descriptive error if the secret cannot be found anywhere.
func (r *Resolver) Resolve(name string) (string, error) {
	// 1. --secret flags (highest priority)
	if val, ok := r.inlineSecrets[name]; ok {
		return val, nil
	}

	// 2. .env file(s)
	if val, ok := r.envFileSecrets[name]; ok {
		return val, nil
	}

	// 3. Vault fallback
	if r.vaultFallback != nil {
		val, err := r.vaultFallback(name)
		if err == nil {
			return val, nil
		}
		// Vault returned an error — fall through to the "not found" message
		// but include the Vault error for context.
		return "", r.notFoundError(name, fmt.Sprintf("not found in any source; Vault error: %v", err))
	}

	return "", r.notFoundError(name, "not found in any source")
}

// ResolveAll resolves all names and returns a map of name → value.
// Stops and returns an error on the first missing secret.
func (r *Resolver) ResolveAll(names []string) (map[string]string, error) {
	result := make(map[string]string, len(names))
	for _, name := range names {
		val, err := r.Resolve(name)
		if err != nil {
			return nil, err
		}
		result[name] = val
	}
	return result, nil
}

// HasAnySource returns true if at least one secret source is configured.
func (r *Resolver) HasAnySource() bool {
	return len(r.inlineSecrets) > 0 ||
		len(r.envFileSecrets) > 0 ||
		r.vaultFallback != nil
}

func (r *Resolver) notFoundError(name string, detail string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "secret %q: %s\n\n", name, detail)
	fmt.Fprintln(&sb, "Provide secrets for local runs with:")
	fmt.Fprintln(&sb, "  --secret KEY=VALUE          inline value (can be repeated)")
	fmt.Fprintln(&sb, "  --env-file .secrets.env     load from a .env file")
	fmt.Fprintln(&sb, "  (auto) .env                 in the current directory")
	if r.vaultFallback == nil {
		fmt.Fprintln(&sb, "\nOr configure Vault:")
		fmt.Fprintln(&sb, "  $env:FORGE_VAULT_ADDR  = 'http://localhost:8200'")
		fmt.Fprintln(&sb, "  $env:FORGE_VAULT_TOKEN = 'forge-dev-token'")
	}
	return fmt.Errorf("%s", sb.String())
}

// ── .env file parser ──────────────────────────────────────────────────────────

// parseEnvFile reads a .env file and returns a map of KEY → value.
// Supports comments (#), blank lines, optional `export` prefix,
// and quoted values (" or ').
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]string)
	lineNum := 0
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip optional `export ` prefix (common in shell env files).
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)

		idx := strings.IndexByte(line, '=')
		if idx < 1 {
			// Not a KEY=VALUE line — could be a bare variable name or malformed.
			// Skip silently to be permissive.
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := line[idx+1:]

		if key == "" {
			continue
		}

		// Strip inline comments (value # comment) — only when value is unquoted.
		val = stripInlineComment(val)

		// Unquote if wrapped in " or '.
		val = unquote(val)

		result[key] = val
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading line %d: %w", lineNum, err)
	}
	return result, nil
}

// stripInlineComment removes trailing `# comment` from an unquoted value.
func stripInlineComment(val string) string {
	// Don't strip from quoted values.
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		return val
	}
	// Find first # that isn't inside quotes.
	if idx := strings.Index(val, " #"); idx >= 0 {
		val = val[:idx]
	}
	return strings.TrimSpace(val)
}

// unquote strips matching leading/trailing " or ' quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
