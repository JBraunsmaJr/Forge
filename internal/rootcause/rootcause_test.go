package rootcause

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		lines    []string
		wantID   string
		wantCat  Category
		wantNone bool
	}{
		{
			name:    "postgres connection refused (issue #44's own example)",
			lines:   []string{"Running integration-test", "dial tcp 127.0.0.1:5432: connect: connection refused", "exit status 1"},
			wantID:  "connection-refused",
			wantCat: CategoryInfrastructure,
		},
		{
			name:    "npm 404",
			lines:   []string{"npm ERR! code E404", "npm ERR! 404 Not Found - GET https://registry.npmjs.org/left-pad"},
			wantID:  "registry-404",
			wantCat: CategoryDependency,
		},
		{
			name:    "dns failure",
			lines:   []string{"curl: (6) Could not resolve host: api.internal.example.com"},
			wantID:  "dns-failure",
			wantCat: CategoryNetwork,
		},
		{
			name:    "missing env var",
			lines:   []string{"Error: required environment variable DATABASE_URL is not set"},
			wantID:  "missing-env-var",
			wantCat: CategoryConfiguration,
		},
		{
			name:    "flaky retry",
			lines:   []string{"Test suite passed on retry (1 flaky test detected)"},
			wantID:  "flaky-signal",
			wantCat: CategoryFlakyTest,
		},
		{
			name:    "go panic",
			lines:   []string{"panic: runtime error: index out of range [3] with length 2", "goroutine 1 [running]:"},
			wantID:  "panic-or-exception",
			wantCat: CategoryCodeDefect,
		},
		{
			name:    "assertion failure",
			lines:   []string{"    AssertionError: expected 200 to equal 500"},
			wantID:  "test-assertion-failure",
			wantCat: CategoryCodeDefect,
		},
		{
			name:    "forge's own timeout wrapper message",
			lines:   []string{"◯ step timed out after 30m0s"},
			wantID:  "forge-step-timeout",
			wantCat: CategoryInfrastructure,
		},
		{
			name:     "no known signature",
			lines:    []string{"Building...", "Done."},
			wantNone: true,
		},
		{
			name:     "empty log",
			lines:    []string{},
			wantNone: true,
		},
		{
			// The ordering contract documented on Library: more specific
			// patterns are listed (and must win) ahead of the broad,
			// catch-all code-defect patterns — even when the broad
			// signature's line appears first in the log.
			name:    "panic caused by a refused connection",
			lines:   []string{"panic: dial error", "dial tcp 127.0.0.1:5432: connect: connection refused"},
			wantID:  "connection-refused",
			wantCat: CategoryInfrastructure,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Classify(tc.lines)
			if tc.wantNone {
				if m != nil {
					t.Fatalf("expected no match, got pattern %q (%s)", m.Pattern.ID, m.Pattern.Category)
				}
				return
			}
			if m == nil {
				t.Fatalf("expected a match for pattern %q, got none", tc.wantID)
			}
			if m.Pattern.ID != tc.wantID {
				t.Errorf("pattern ID = %q, want %q", m.Pattern.ID, tc.wantID)
			}
			if m.Pattern.Category != tc.wantCat {
				t.Errorf("category = %q, want %q", m.Pattern.Category, tc.wantCat)
			}
			if m.MatchedLine == "" {
				t.Errorf("expected a non-empty matched line")
			}
		})
	}
}

// A matched line long enough to need truncation, with a multi-byte rune
// sitting right at the cutoff, must still truncate to valid UTF-8 —
// otherwise the insert into job_root_causes (a UTF8 column) fails and the
// whole classification is silently lost, not just the tail of the line.
func TestClassifyTruncatesOnRuneBoundary(t *testing.T) {
	long := "panic: " + strings.Repeat("é", 400)
	m := Classify([]string{long})
	if m == nil {
		t.Fatal("expected a match")
	}
	if !utf8.ValidString(m.MatchedLine) {
		t.Errorf("matched line is not valid UTF-8: %q", m.MatchedLine)
	}
	if len(m.MatchedLine) > maxMatchedLineLen+len("…") {
		t.Errorf("matched line longer than expected: %d bytes", len(m.MatchedLine))
	}
}

// Every pattern must have a description and suggested fix — otherwise
// the UI has nothing useful to show for that classification.
func TestLibraryPatternsAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Library {
		if p.ID == "" {
			t.Errorf("pattern with empty ID (category %s)", p.Category)
		}
		if seen[p.ID] {
			t.Errorf("duplicate pattern ID %q", p.ID)
		}
		seen[p.ID] = true
		if p.Regex == nil {
			t.Errorf("pattern %q has a nil regex", p.ID)
		}
		if p.Description == "" {
			t.Errorf("pattern %q has no description", p.ID)
		}
		if p.SuggestedFix == "" {
			t.Errorf("pattern %q has no suggested fix", p.ID)
		}
	}
}
