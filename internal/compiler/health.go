package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type HealthSeverity string

const (
	HealthCritical   HealthSeverity = "critical"
	HealthWarning    HealthSeverity = "warning"
	HealthSuggestion HealthSeverity = "suggestion"
)

// HealthFinding is one issue contributing to (or, for suggestions,
// unrelated to) a pipeline's health score.
type HealthFinding struct {
	Severity HealthSeverity
	Message  string
}

// HealthReport is the result of scoring one pipeline file. History (the
// "↓ from 72 last week" delta) and org-average comparison require
// persistence and cross-project data this package doesn't have — those
// are attached by the caller (see the scheduler-side health monitor) and
// are nil/zero here.
type HealthReport struct {
	PipelineName string
	Score        int
	Findings     []HealthFinding
	Critical     []HealthFinding
	Warnings     []HealthFinding
	Suggestions  []HealthFinding
}

// scoring model: presence of any CRITICAL finding caps the score at 79
// regardless of how good everything else is; presence of any WARNING (with no criticals)
// caps at 89. Within a band, more findings still means a lower score —
// this isn't just a ceiling, each finding costs points on top of it.
const (
	criticalCeiling = 79
	warningCeiling  = 89
	criticalPenalty = 10
	warningPenalty  = 4
)

// secretPatterns are intentionally a small, high-confidence set (known
// vendor token prefixes, or a KEY=<long-opaque-value> assignment shape)
// rather than an attempt at exhaustive entropy-based secret detection —
// this is meant to catch the common, careless case the issue names
// ("Secret AWS_KEY_ID appears in run: command"), not replace a dedicated
// secret scanner.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                                                // AWS access key ID
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),                                                      // GitHub token (ghp_, gho_, ghu_, ghs_, ghr_)
	regexp.MustCompile(`(?i)\b(AWS|SECRET|API|ACCESS)[A-Z_]*KEY[A-Z_]*\s*[:=]\s*['"]?[A-Za-z0-9/+]{16,}`), // *_KEY = <opaque value>
	regexp.MustCompile(`(?i)\bpassword\s*[:=]\s*['"][^'"$]{6,}['"]`),                                      // password = "literal" (not $VAR)
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                                              // PEM private key
}

// Score analyzes one pipeline file and produces a HealthReport.
//
// lastModified is optional (nil when unavailable, e.g. linting raw text
// with no git history behind it) and drives the "hasn't been updated in
// N months" suggestion. rawSource is the pipeline file's literal bytes,
// used for the line-level secret scan and duplicate-run-block detection —
// deliberately not the compiled step list, for the same reason Lint's raw
// pass exists: normalization loses information a health check needs.
func Score(path string, rawSource []byte, lastModified *time.Time) (*HealthReport, error) {
	lintReport, err := LintData(path, rawSource)
	if err != nil {
		return nil, err
	}

	report := &HealthReport{}

	if lintReport.CompileFailed {
		// An unparseable pipeline can't be meaningfully scored beyond
		// "this is broken" — treat it as maximally critical rather than
		// silently returning a report with no findings.
		report.addCritical("pipeline does not compile — run `forge validate` for details")
		report.finalize()
		return report, nil
	}

	raw, _ := parseRawPipeline(rawSource, path) // best-effort; raw-only checks below are skipped (raw == nil) if this fails, no separate error path needed since Lint already succeeded above using the same underlying parser
	if raw != nil {
		report.PipelineName = raw.Name
	}

	unpinnedImages := 0
	noTimeout := 0
	for _, f := range lintReport.Findings {
		switch {
		case strings.Contains(f.Message, "is not pinned to a specific version"):
			unpinnedImages++
			report.addCritical(fmt.Sprintf("Step %q %s", f.Step, f.Message))
		case strings.Contains(f.Message, "no timeout set"):
			noTimeout++
		default:
			msg := f.Message
			if f.Step != "" {
				msg = fmt.Sprintf("Step %q %s", f.Step, f.Message)
			}
			if f.Severity == SeverityError {
				report.addCritical(msg)
			} else {
				report.addWarning(msg)
			}
		}
	}
	if noTimeout > 0 {
		report.addWarning(pluralize(noTimeout, "step has", "steps have") + " no timeout configured")
	}

	if raw != nil {
		checkHardcodedSecrets(report, rawSource)
		checkNoNotificationStep(report, raw.Steps)
	}

	checkDuplicateStepLogic(report, lintReport.StepCount, raw)
	checkAlwaysTogether(report, path, rawSource)

	if lastModified != nil {
		checkStaleness(report, path, *lastModified)
	}

	_ = unpinnedImages // kept for potential future weighting; not currently read beyond the loop above

	report.finalize()
	return report, nil
}

func (r *HealthReport) addCritical(msg string) {
	r.Findings = append(r.Findings, HealthFinding{Severity: HealthCritical, Message: msg})
}
func (r *HealthReport) addWarning(msg string) {
	r.Findings = append(r.Findings, HealthFinding{Severity: HealthWarning, Message: msg})
}
func (r *HealthReport) addSuggestion(msg string) {
	r.Findings = append(r.Findings, HealthFinding{Severity: HealthSuggestion, Message: msg})
}

// finalize buckets Findings into the three display groups and computes the
// final capped, graduated score.
func (r *HealthReport) finalize() {
	for _, f := range r.Findings {
		switch f.Severity {
		case HealthCritical:
			r.Critical = append(r.Critical, f)
		case HealthWarning:
			r.Warnings = append(r.Warnings, f)
		case HealthSuggestion:
			r.Suggestions = append(r.Suggestions, f)
		}
	}

	score := 100 - criticalPenalty*len(r.Critical) - warningPenalty*len(r.Warnings)
	if len(r.Critical) > 0 && score > criticalCeiling {
		score = criticalCeiling
	} else if len(r.Warnings) > 0 && score > warningCeiling {
		score = warningCeiling
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	r.Score = score
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// checkHardcodedSecrets scans the pipeline file's raw text, line by line,
// for the known-shape patterns in secretPatterns. This runs against the
// literal file content — not the parsed/compiled structure — specifically
// so it can report a real line number
func checkHardcodedSecrets(report *HealthReport, rawSource []byte) {
	lines := strings.Split(string(rawSource), "\n")
	for i, line := range lines {
		for _, pat := range secretPatterns {
			if pat.MatchString(line) {
				report.addCritical(fmt.Sprintf("possible hardcoded secret on line %d — use secrets: instead of embedding values directly", i+1))
				break // one finding per line is enough; don't pile on if multiple patterns match the same line
			}
		}
	}
}

// checkNoNotificationStep warns if no step is configured to run
// regardless of upstream failure. always_run: true is used as a proxy for
// "this pipeline has some failure-visible step" (a notification, cleanup,
// or reporting step)
func checkNoNotificationStep(report *HealthReport, steps []JSONStep) {
	for _, s := range steps {
		if s.AlwaysRun {
			return
		}
	}
	report.addWarning("pipeline has no always_run step — failures may go unnoticed with nothing guaranteed to run and report on them")
}

// checkDuplicateStepLogic looks for two steps whose run: (or, if empty,
// command:) content is byte-for-byte identical — a concrete, checkable
// stand-in for "duplicate logic" that doesn't require guessing semantic
// similarity between different scripts.
func checkDuplicateStepLogic(report *HealthReport, stepCount int, raw *jsonPipeline) {
	if raw == nil {
		return
	}
	seen := make(map[string]string) // content hash -> first step ID that had it
	for i, js := range raw.Steps {
		content := js.Run
		if content == "" && len(js.Command) > 0 {
			content = strings.Join(js.Command, " ")
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
		key := hex.EncodeToString(sum[:])
		label := stepLabel(js.ID, i)
		if firstID, ok := seen[key]; ok {
			report.addSuggestion(fmt.Sprintf("steps %q and %q have identical run/command content — consider extracting to a shared script: file", firstID, label))
		} else {
			seen[key] = label
		}
	}
}

// checkAlwaysTogether flags pairs of steps with an identical depends_on
// set. Steps that depend on exactly the same prerequisites are always
// scheduled in lockstep relative to each other
func checkAlwaysTogether(report *HealthReport, path string, rawSource []byte) {
	p, err := compileDataNoValidate(rawSource, path)
	if err != nil {
		return
	}
	byDeps := make(map[string][]string)
	for _, s := range p.Steps {
		if len(s.DependsOn) == 0 {
			continue // "always run at the start" isn't a meaningful pairing signal
		}
		deps := append([]string(nil), s.DependsOn...)
		sort.Strings(deps)
		key := strings.Join(deps, ",")
		byDeps[key] = append(byDeps[key], s.ID)
	}
	for _, ids := range byDeps {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		report.addSuggestion(fmt.Sprintf("steps %s share the exact same dependencies and always run together — consider merging", quoteJoin(ids)))
	}
}

func quoteJoin(ids []string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return strings.Join(quoted, ", ")
}

// checkStaleness is best-effort: lastModified is resolved by the caller
// (e.g. `git log -1 --format=%ct -- <path>` locally, or commit metadata
// from the scheduler's git cache server-side), not by this package, which
// has no git access of its own.
func checkStaleness(report *HealthReport, path string, lastModified time.Time) {
	age := time.Since(lastModified)
	const sixMonths = 6 * 30 * 24 * time.Hour
	if age > sixMonths {
		months := int(age.Hours() / 24 / 30)
		report.addSuggestion(fmt.Sprintf("%q hasn't been updated in %d months — review for drift", path, months))
	}
}
