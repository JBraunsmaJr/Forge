package childrun

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Limits on what a parent may pass to a child run.
const (
	maxVariables     = 100
	maxVariableValue = 8 * 1024
)

// reservedPrefix marks the variables Forge sets itself. A parent must not be
// able to make a child run believe it is for another commit, run, or agent.
const reservedPrefix = "FORGE_"

var variableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateVariables checks the variables a parent passes to a child run in
// another project.
func ValidateVariables(variables map[string]string) error {
	if len(variables) > maxVariables {
		return fmt.Errorf("too many variables: %d (the limit is %d)", len(variables), maxVariables)
	}

	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Strings(names) // report the same problem first on every run

	for _, name := range names {
		switch {
		case !variableNamePattern.MatchString(name):
			return fmt.Errorf("variable %q is not a valid environment variable name", name)
		case strings.HasPrefix(strings.ToUpper(name), reservedPrefix):
			return fmt.Errorf("variable %q is reserved: names starting with %s are set by Forge", name, reservedPrefix)
		case len(variables[name]) > maxVariableValue:
			return fmt.Errorf("variable %q is longer than %d bytes", name, maxVariableValue)
		case strings.ContainsRune(variables[name], 0):
			return fmt.Errorf("variable %q contains a NUL byte", name)
		}
	}
	return nil
}

// ApplyVariables sets the parent's variables in a child step's environment,
// overriding the step's own values. Variables Forge set itself are never
// overridden, whatever the caller validated.
func ApplyVariables(env, variables map[string]string) {
	for name, value := range variables {
		if strings.HasPrefix(strings.ToUpper(name), reservedPrefix) {
			continue
		}
		env[name] = value
	}
}
