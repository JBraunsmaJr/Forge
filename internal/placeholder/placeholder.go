// Package placeholder expands ${{ env.NAME }} placeholders in pipeline
// values that are resolved when a step runs, such as image references and
// image build settings.
package placeholder

import "regexp"

// envPattern matches ${{ env.NAME }}, with optional spaces inside the braces.
var envPattern = regexp.MustCompile(`\$\{\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// HasEnv reports whether value contains at least one env placeholder.
func HasEnv(value string) bool {
	return envPattern.MatchString(value)
}

// EnvNames returns the variable names value refers to, in order of first
// appearance and without duplicates.
func EnvNames(value string) []string {
	var names []string
	seen := make(map[string]bool)
	for _, match := range envPattern.FindAllStringSubmatch(value, -1) {
		if name := match[1]; !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// ExpandEnv replaces every placeholder whose variable is in env with that
// variable's value. A placeholder that names a variable not in env is left
// as written, so the caller's validation reports it instead of the value
// silently becoming empty.
func ExpandEnv(value string, env map[string]string) string {
	return envPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := envPattern.FindStringSubmatch(match)[1]
		if resolved, ok := env[name]; ok {
			return resolved
		}
		return match
	})
}

// ReplaceEnv replaces every placeholder with replacement, whatever variable
// it names. It is for checking the shape of a value before its variables
// are known.
func ReplaceEnv(value, replacement string) string {
	return envPattern.ReplaceAllLiteralString(value, replacement)
}
