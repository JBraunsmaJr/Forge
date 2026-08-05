// Package buildnumber implements the build-number format language used
// by issue #57: per-(project, pipeline) format strings such as
// "%year%-%month%.%counter%" or "%major%.%minor%.%counter%" that
// combine literal text with a small set of tokens.
//
// A Format is parsed once (and validated — unknown or malformed tokens
// are rejected here, at parse time, rather than being discovered when a
// run is submitted) and can then be rendered twice for different
// purposes:
//
//   - Render produces the build number shown to users and injected into
//     FORGE_BUILD_NUMBER.
//   - VersionKey produces the non-counter portion of that same render,
//     used to decide whether a build's counter should reset. Two runs
//     that produce the same VersionKey share one counter; a run whose
//     VersionKey differs from the last-recorded one for that
//     (project, pipeline) scope starts its counter over at 1.
package buildnumber

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultFormat is used for any (project, pipeline) scope that hasn't
// had a format explicitly configured yet.
const DefaultFormat = "%year%-%month%.%counter%"

// LocalFallback is the FORGE_BUILD_NUMBER value used when there is no
// scheduler/project context to assign a real build number from (e.g.
// `forge run` executed locally). It's deliberately not a value any
// real format could ever render, so pipelines can tell the difference
// between a genuine scheduler-assigned build number and this fallback.
const LocalFallback = "local"

// TokenKind identifies what a Token contributes when rendered.
type TokenKind int

const (
	// TokenLiteral is verbatim text carried over from the format string.
	TokenLiteral TokenKind = iota
	// TokenCounter is the build counter itself, optionally zero-padded.
	TokenCounter
	// TokenYear is the current UTC year, e.g. "2026".
	TokenYear
	// TokenMonth is the current UTC month, zero-padded to 2 digits.
	TokenMonth
	// TokenDay is the current UTC day-of-month, zero-padded to 2 digits.
	TokenDay
	// TokenMajor is the pipeline's configured major version.
	TokenMajor
	// TokenMinor is the pipeline's configured minor version.
	TokenMinor
)

// Token is one piece of a parsed Format: either a run of literal text,
// or a single token such as %counter:3%.
type Token struct {
	Kind    TokenKind
	Literal string // populated only when Kind == TokenLiteral
	Width   int    // populated only when Kind == TokenCounter; 0 = no zero-padding
}

// Format is a parsed, validated build-number format string.
type Format struct {
	Tokens []Token
	Raw    string
}

// tokenPattern matches anything wrapped in a single pair of %...%,
// including malformed ones (e.g. "%bogus%") so parseToken can produce a
// precise error instead of silently treating it as literal text.
var tokenPattern = regexp.MustCompile(`%[^%]*%`)

// Parse validates raw and returns its parsed representation. It never
// renders anything — callers running purely as a validator (forge
// validate, the UI's format editor, or a config-save endpoint) can call
// Parse and stop as soon as it returns a nil error.
func Parse(raw string) (*Format, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("format string must not be empty")
	}

	var tokens []Token
	counterTokens := 0
	last := 0

	for _, m := range tokenPattern.FindAllStringIndex(raw, -1) {
		start, end := m[0], m[1]
		if start > last {
			tokens = append(tokens, Token{Kind: TokenLiteral, Literal: raw[last:start]})
		}

		tok, err := parseToken(raw[start:end])
		if err != nil {
			return nil, fmt.Errorf("invalid token %q: %w", raw[start:end], err)
		}
		if tok.Kind == TokenCounter {
			counterTokens++
		}
		tokens = append(tokens, tok)
		last = end
	}

	if last < len(raw) {
		rest := raw[last:]
		if strings.Contains(rest, "%") {
			return nil, fmt.Errorf("unterminated token near %q", rest)
		}
		tokens = append(tokens, Token{Kind: TokenLiteral, Literal: rest})
	}

	switch counterTokens {
	case 0:
		return nil, fmt.Errorf("format string must contain a %%counter%% token")
	case 1:
		// ok
	default:
		return nil, fmt.Errorf("format string must contain exactly one %%counter%% token, found %d", counterTokens)
	}

	return &Format{Tokens: tokens, Raw: raw}, nil
}

// parseToken validates a single "%...%" chunk (including its percent
// signs) and returns the Token it represents.
func parseToken(tok string) (Token, error) {
	inner := strings.TrimSuffix(strings.TrimPrefix(tok, "%"), "%")
	if inner == "" || strings.Contains(inner, "%") {
		return Token{}, fmt.Errorf("malformed token")
	}

	name := inner
	width := 0
	if idx := strings.IndexByte(inner, ':'); idx >= 0 {
		name = inner[:idx]
		widthStr := inner[idx+1:]
		n, err := strconv.Atoi(widthStr)
		if err != nil || n <= 0 || n > 18 {
			return Token{}, fmt.Errorf("invalid zero-pad width %q", widthStr)
		}
		width = n
	}

	switch name {
	case "counter":
		return Token{Kind: TokenCounter, Width: width}, nil
	case "year":
		if width != 0 {
			return Token{}, fmt.Errorf("%%year%% does not accept a width")
		}
		return Token{Kind: TokenYear}, nil
	case "month":
		if width != 0 {
			return Token{}, fmt.Errorf("%%month%% does not accept a width")
		}
		return Token{Kind: TokenMonth}, nil
	case "day":
		if width != 0 {
			return Token{}, fmt.Errorf("%%day%% does not accept a width")
		}
		return Token{Kind: TokenDay}, nil
	case "major":
		if width != 0 {
			return Token{}, fmt.Errorf("%%major%% does not accept a width")
		}
		return Token{Kind: TokenMajor}, nil
	case "minor":
		if width != 0 {
			return Token{}, fmt.Errorf("%%minor%% does not accept a width")
		}
		return Token{Kind: TokenMinor}, nil
	default:
		return Token{}, fmt.Errorf("unknown token %%%s%%", name)
	}
}

// HasVolatileToken reports whether f includes a date or major/minor
// token — i.e. whether its VersionKey can ever change between runs. A
// format with no volatile token (just literal text plus %counter%)
// keeps one ever-incrementing counter, unchanged from a build with no
// versioning scheme at all.
func (f *Format) HasVolatileToken() bool {
	for _, t := range f.Tokens {
		switch t.Kind {
		case TokenYear, TokenMonth, TokenDay, TokenMajor, TokenMinor:
			return true
		}
	}
	return false
}

// Render produces the build-number string for the given counter value,
// major/minor version, and reference time (evaluated as UTC by callers
// per the spec — pass time.Now().UTC()).
func (f *Format) Render(counter int64, major, minor int, now time.Time) string {
	var sb strings.Builder
	for _, t := range f.Tokens {
		writeToken(&sb, t, &counter, major, minor, now, true)
	}
	return sb.String()
}

// VersionKey produces the non-counter portion of Render's output: every
// token renders identically except %counter%/%counter:N%, which
// contributes nothing. Two builds whose format+major+minor+now render
// the same VersionKey share a counter; a different VersionKey means the
// counter for that scope restarts at 1.
func (f *Format) VersionKey(major, minor int, now time.Time) string {
	var sb strings.Builder
	for _, t := range f.Tokens {
		writeToken(&sb, t, nil, major, minor, now, false)
	}
	return sb.String()
}

// writeToken renders a single token into sb. When counter is nil (the
// VersionKey path), TokenCounter contributes nothing; includeCounter
// exists only to make that intent explicit at call sites.
func writeToken(sb *strings.Builder, t Token, counter *int64, major, minor int, now time.Time, includeCounter bool) {
	switch t.Kind {
	case TokenLiteral:
		sb.WriteString(t.Literal)
	case TokenCounter:
		if !includeCounter || counter == nil {
			return
		}
		if t.Width > 0 {
			sb.WriteString(fmt.Sprintf("%0*d", t.Width, *counter))
		} else {
			sb.WriteString(strconv.FormatInt(*counter, 10))
		}
	case TokenYear:
		sb.WriteString(fmt.Sprintf("%04d", now.Year()))
	case TokenMonth:
		sb.WriteString(fmt.Sprintf("%02d", int(now.Month())))
	case TokenDay:
		sb.WriteString(fmt.Sprintf("%02d", now.Day()))
	case TokenMajor:
		sb.WriteString(strconv.Itoa(major))
	case TokenMinor:
		sb.WriteString(strconv.Itoa(minor))
	}
}
