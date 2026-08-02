// Package rootcause implements Forge's build-failure root-cause
// classifier (issue #44).
//
// When a step fails, developers have to read the raw logs and figure out
// whether it's a code problem, an infrastructure problem, a dependency
// problem, or a flaky network call — that takes time and domain
// knowledge. This package pattern-matches a failed job's log output
// against a small library of known failure signatures and returns a
// best-guess classification (infrastructure / dependency / flaky test /
// code defect / configuration / network), along with a human-readable
// explanation and a suggested next step.
//
// The library is intentionally simple (ordered regexes, first match
// wins) so it's easy to extend over time as new failure signatures are
// identified
package rootcause

import (
	"regexp"
	"unicode/utf8"
)

// Category is one of the failure buckets from issue #44's classification
// table.
type Category string

const (
	CategoryInfrastructure Category = "infrastructure"
	CategoryDependency     Category = "dependency"
	CategoryFlakyTest      Category = "flaky_test"
	CategoryCodeDefect     Category = "code_defect"
	CategoryConfiguration  Category = "configuration"
	CategoryNetwork        Category = "network"
	CategoryUnknown        Category = "unknown"
)

// Pattern is a single entry in the signature library.
type Pattern struct {
	// ID is a stable identifier for this pattern, used for frequency
	// tracking ("N of the last M failures on this step matched the same
	// pattern") — keep it stable across edits, don't reuse IDs for a
	// different signature.
	ID           string
	Category     Category
	Regex        *regexp.Regexp
	Description  string
	SuggestedFix string
}

// Match is the result of classifying a job's log output.
type Match struct {
	Pattern     Pattern
	MatchedLine string
}

// maxMatchedLineLen bounds how much of a matched log line is persisted —
// long enough to be useful context, short enough to never look like a
// dumped stack trace in a summary UI.
const maxMatchedLineLen = 300

// Library is Forge's built-in signature set. Order matters: Classify
// checks patterns in list order and returns the first one that matches
// any log line, so more specific signatures are listed before broad,
// catch-all ones (code-defect patterns are deliberately last).
var Library = []Pattern{
	// --- Infrastructure --------------------------------------------------
	{
		ID:           "forge-step-timeout",
		Category:     CategoryInfrastructure,
		Regex:        regexp.MustCompile(`(?i)step timed out after`),
		Description:  "The step was killed by Forge for exceeding its configured timeout.",
		SuggestedFix: "Increase the step's timeout, or investigate why it's running slower than usual (resource contention, a hung process, waiting on input).",
	},
	{
		ID:           "connection-refused",
		Category:     CategoryInfrastructure,
		Regex:        regexp.MustCompile(`(?i)connection refused|dial tcp.*refused|econnrefused`),
		Description:  "A dependent service (database, cache, internal API, etc.) refused the connection.",
		SuggestedFix: "Add a health-check / wait step before this one runs, and confirm the dependent service container actually started successfully.",
	},
	{
		ID:           "oom-killed",
		Category:     CategoryInfrastructure,
		Regex:        regexp.MustCompile(`(?i)\boom.?killed\b|out of memory|cannot allocate memory|killed \(signal 9\)`),
		Description:  "The step's container ran out of memory and was killed.",
		SuggestedFix: "Increase the job's memory limit, or look for a memory leak / unbounded cache in the step's own process.",
	},
	{
		ID:           "disk-full",
		Category:     CategoryInfrastructure,
		Regex:        regexp.MustCompile(`(?i)no space left on device|disk quota exceeded`),
		Description:  "The runner ran out of disk space.",
		SuggestedFix: "Prune build caches/artifacts on the agent host, or provision more disk for the agent pool.",
	},
	{
		ID:           "generic-timeout",
		Category:     CategoryInfrastructure,
		Regex:        regexp.MustCompile(`(?i)i/o timeout|context deadline exceeded|operation timed out|request timed out`),
		Description:  "A network or I/O call inside the step exceeded its own timeout.",
		SuggestedFix: "Check the health of the service being called, and consider whether the timeout value itself is too aggressive.",
	},

	// --- Dependency --------------------------------------------------
	{
		ID:           "registry-404",
		Category:     CategoryDependency,
		Regex:        regexp.MustCompile(`(?i)npm err!.*404|pypi.*404 not found|could not find a version that satisfies|no matching (package|version) found`),
		Description:  "A package registry returned 404, or the package manager couldn't find a version satisfying the request.",
		SuggestedFix: "Verify the dependency name/version still exists on the registry, and check the registry's status page for an outage.",
	},
	{
		ID:           "checksum-mismatch",
		Category:     CategoryDependency,
		Regex:        regexp.MustCompile(`(?i)checksum (mismatch|verification failed)|integrity check failed|hash mismatch|sri check failed`),
		Description:  "A downloaded package's checksum didn't match what the lockfile expected.",
		SuggestedFix: "Regenerate the lockfile. If it keeps happening for the same package, treat it as a possible supply-chain red flag.",
	},
	{
		ID:           "dependency-resolution",
		Category:     CategoryDependency,
		Regex:        regexp.MustCompile(`(?i)could not resolve dependenc|unable to resolve dependency tree|dependency conflict|eresolve`),
		Description:  "The package manager couldn't resolve a consistent dependency tree.",
		SuggestedFix: "Review recent dependency changes for a version conflict, or pin the offending package.",
	},

	// --- Network --------------------------------------------------
	{
		ID:           "dns-failure",
		Category:     CategoryNetwork,
		Regex:        regexp.MustCompile(`(?i)no such host|dns resolution failed|could not resolve host|name or service not known`),
		Description:  "DNS resolution failed for a hostname the step tried to reach.",
		SuggestedFix: "Check that the target hostname is correct, and that the runner's DNS/network egress is healthy.",
	},
	{
		ID:           "tls-error",
		Category:     CategoryNetwork,
		Regex:        regexp.MustCompile(`(?i)x509:|tls handshake|certificate signed by unknown authority|certificate has expired|ssl.*verify failed`),
		Description:  "A TLS/certificate handshake failed.",
		SuggestedFix: "Check whether a certificate expired, or whether the runner is missing a CA bundle / sitting behind a TLS-inspecting proxy.",
	},

	// --- Configuration --------------------------------------------------
	{
		ID:           "missing-env-var",
		Category:     CategoryConfiguration,
		Regex:        regexp.MustCompile(`(?i)environment variable .*(not set|is required|is missing)|required env(ironment)? var(iable)? .*(missing|not set)`),
		Description:  "The step referenced an environment variable that wasn't set.",
		SuggestedFix: "Add the missing variable to the pipeline or project secrets/env configuration.",
	},
	{
		ID:           "file-not-found",
		Category:     CategoryConfiguration,
		Regex:        regexp.MustCompile(`(?i)no such file or directory|cannot find (the )?path|file not found:`),
		Description:  "The step tried to read a file or path that doesn't exist.",
		SuggestedFix: "Double-check the path in the pipeline config, and confirm any preceding step that should have produced the file actually ran.",
	},

	// --- Flaky test --------------------------------------------------
	{
		ID:           "flaky-signal",
		Category:     CategoryFlakyTest,
		Regex:        regexp.MustCompile(`(?i)\bflaky\b|passed on retry|non-deterministic|retrying failed test`),
		Description:  "The test runner itself flagged this result as flaky or non-deterministic.",
		SuggestedFix: "Quarantine the test and investigate shared state, timing assumptions, or test-ordering dependencies.",
	},

	// --- Code defect (checked last — deliberately the broadest/most general) ---
	{
		ID:           "panic-or-exception",
		Category:     CategoryCodeDefect,
		Regex:        regexp.MustCompile(`(?i)panic:|traceback \(most recent call last\)|unhandled exception|segmentation fault|fatal error:`),
		Description:  "The process panicked or raised an unhandled exception.",
		SuggestedFix: "Read the stack trace around the matched log line — this points at an actual defect, not the environment.",
	},
	{
		ID:           "test-assertion-failure",
		Category:     CategoryCodeDefect,
		Regex:        regexp.MustCompile(`(?i)assertionerror|assertion failed|expect(ed)?\(.*\)\.(to|not)|test failed:|^\s*FAIL:`),
		Description:  "A test assertion failed.",
		SuggestedFix: "Check git blame on the failing test and the code it exercises — this is most likely a genuine regression.",
	},
}

// Classify scans a job's log lines against the pattern library and
// returns the first match, or nil if nothing in the library matched
// (callers should treat that as CategoryUnknown rather than guessing).
func Classify(lines []string) *Match {
	for _, p := range Library {
		for _, line := range lines {
			if line == "" {
				continue
			}
			if p.Regex.MatchString(line) {
				return &Match{Pattern: p, MatchedLine: truncate(line, maxMatchedLineLen)}
			}
		}
	}
	return nil
}

// truncate cuts s to at most n bytes, never splitting a multi-byte UTF-8
// rune — a byte-boundary cut could produce an invalid UTF-8 sequence,
// which Postgres rejects outright on insert (silently losing the whole
// classification, not just the tail of the matched line).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
