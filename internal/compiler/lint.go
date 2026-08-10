package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// LintSeverity distinguishes a hard problem (the pipeline will fail to
// submit or run) from a style suggestion (the pipeline works, but an
// author probably wants to know).
type LintSeverity string

const (
	SeverityError   LintSeverity = "error"
	SeverityWarning LintSeverity = "warning"
)

// LintFinding is one issue surfaced by Lint. Step is empty for
// pipeline-level findings (e.g. a parse error).
type LintFinding struct {
	Severity LintSeverity
	Step     string
	Message  string
}

// LintReport collects every finding from one Lint call. Unlike Compile,
// which returns on the first error, Lint keeps going so an author sees
// the full picture — everything wrong, not just the first thing — in one
// pass, matching how a real linter behaves.
type LintReport struct {
	Path      string
	StepCount int
	// CompileFailed is true only when the pipeline itself couldn't be
	// parsed/compiled at all — distinct from StepCount==0, which could
	// also mean a pipeline that compiled fine but legitimately declares no
	// steps.
	CompileFailed bool
	Findings      []LintFinding
}

func (r *LintReport) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (r *LintReport) errorf(step, format string, args ...any) {
	r.Findings = append(r.Findings, LintFinding{Severity: SeverityError, Step: step, Message: fmt.Sprintf(format, args...)})
}

func (r *LintReport) warnf(step, format string, args ...any) {
	r.Findings = append(r.Findings, LintFinding{Severity: SeverityWarning, Step: step, Message: fmt.Sprintf(format, args...)})
}

// recognizedTypes mirrors what compileStep actually understands. Anything
// else silently becomes a plain task there — Lint is what catches the typo
// before that silent fallback hides it.
var recognizedTypes = map[string]bool{
	"": true, "task": true, "generator": true, "pipeline": true, "approval": true, "release": true, "docker_publish": true,
}

// Lint performs an offline structural and style check of a pipeline file
// without submitting or running anything (issue #20).
//
// Two passes happen against different representations of the pipeline,
// deliberately:
//
//  1. The real parse+expand+compile path (CompileData) — identical to what
//     actually runs, including uses: template resolution, matrix
//     expansion, and With substitution. Checks that are only meaningful
//     post-expansion (duplicate IDs a matrix produced, dangling
//     depends_on, cycles, artifact cross-references) run against this.
//  2. A raw pass over the author's literal YAML/JSON, before any
//     normalization. compileStep silently defaults an empty or
//     unrecognized `type:` to a plain task and discards the original
//     `script:` path once it's rewritten into a command — so those two
//     checks, plus the purely stylistic ones (:latest images, long inline
//     run: blocks, no timeout), have to run against the raw source or the
//     information they need is already gone by the time step 1 finishes.
//
// Known limitation: the raw pass only sees the main pipeline file. A
// script: or type: typo inside a `uses:` template file isn't caught here.
func Lint(path string) (*LintReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline file %s: %w", path, err)
	}
	workspaceDir, _ := os.Getwd()
	return lintData(path, data, workspaceDir)
}

// LintData is Lint, but for content already in memory instead of on disk
// — for callers (like Score, below) that fetched the pipeline file from
// somewhere other than the local filesystem (e.g. a server-side git
// cache) and would have nothing at `path` for os.ReadFile to find. path
// is still used: for its file extension (YAML vs JSON) and in finding
// labels.
//
// The script: existence check is skipped here (not run against the wrong
// directory): there's no "current working directory" that means anything
// for a caller with no real local checkout, and silently checking against
// os.Getwd() would flag every real script as missing rather than doing
// nothing. Lint (the disk-reading entry point above) keeps checking it,
// since a real CLI invocation does have a meaningful workspace root.
func LintData(path string, data []byte) (*LintReport, error) {
	return lintData(path, data, "")
}

func lintData(path string, data []byte, workspaceDir string) (*LintReport, error) {
	report := &LintReport{Path: path}

	// Deliberately compileDataNoValidate, not CompileData: the latter is
	// fail-fast (returns on the very first duplicate ID / dangling
	// depends_on / cycle it finds), which is correct for actually
	// submitting a pipeline but wrong for a linter that's supposed to show
	// everything wrong in one pass. The checks below are a richer,
	// collect-everything superset of what that fail-fast gate does.
	p, compileErr := compileDataNoValidate(data, path, nil)
	if compileErr != nil {
		report.CompileFailed = true
		report.errorf("", "%v", compileErr)
		return report, nil
	}
	report.StepCount = len(p.Steps)

	checkDuplicateIDs(report, p.Steps)
	checkDependsOnExist(report, p.Steps)
	checkCycles(report, p.Steps)
	checkArtifactReferences(report, p.Steps)
	checkRedundantDependsOn(report, p.Steps)

	if raw, err := parseRawPipeline(data, path); err == nil {
		checkRawSteps(report, raw.Steps, workspaceDir)
	}

	return report, nil
}

// parseRawPipeline converts the source to JSON exactly like CompileData
// does (yamlToJSON for .yml/.yaml, passthrough for .json) but stops right
// after unmarshaling — no template resolution, no matrix expansion, no
// per-step compilation — so callers see precisely what the author wrote.
func parseRawPipeline(data []byte, filename string) (*jsonPipeline, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".yml" || ext == ".yaml" {
		converted, err := yamlToJSON(data)
		if err != nil {
			return nil, err
		}
		data = converted
	}
	var raw jsonPipeline
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &raw, nil
}

// stepLabel returns a step's ID, or a positional placeholder for steps
// that omitted one (compileStep auto-generates "step-N" for these, but at
// the raw-parse stage that generation hasn't happened yet).
func stepLabel(id string, index int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("step-%d (no id set)", index+1)
}

// checkDuplicateIDs reports every step ID used more than once, not just
// the second occurrence — a matrix expansion gone wrong can produce many.
func checkDuplicateIDs(report *LintReport, steps []*pipeline.Step) {
	seen := make(map[string]int)
	for _, s := range steps {
		seen[s.ID]++
	}
	ids := make([]string, 0, len(seen))
	for id, count := range seen {
		if count > 1 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		report.errorf(id, "step ID %q is used by %d steps — IDs must be unique (check for a matrix or uses: expansion producing duplicates)", id, seen[id])
	}
}

// checkDependsOnExist reports every depends_on entry that doesn't name a
// real step, individually — an author with three typo'd references should
// see all three at once, not fix one and re-run to find the next.
func checkDependsOnExist(report *LintReport, steps []*pipeline.Step) {
	known := make(map[string]bool, len(steps))
	for _, s := range steps {
		known[s.ID] = true
	}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if !known[dep] {
				report.errorf(s.ID, "depends on %q, which is not defined", dep)
			}
		}
	}
}

// checkCycles runs Kahn's algorithm (issue #20 names it explicitly): compute
// in-degree for every step from its (valid) dependencies, repeatedly remove
// zero-in-degree steps, and anything left unremoved when the queue empties
// is part of a cycle. Steps already flagged by checkDependsOnExist for a
// missing dependency are excluded from edges here, so one typo doesn't
// masquerade as a cycle involving unrelated steps.
func checkCycles(report *LintReport, steps []*pipeline.Step) {
	known := make(map[string]bool, len(steps))
	for _, s := range steps {
		known[s.ID] = true
	}

	inDegree := make(map[string]int, len(steps))
	dependents := make(map[string][]string) // dependency -> steps that depend on it
	for _, s := range steps {
		inDegree[s.ID] = 0
	}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if !known[dep] {
				continue // reported separately; don't let it masquerade as a cycle
			}
			inDegree[s.ID]++
			dependents[dep] = append(dependents[dep], s.ID)
		}
	}

	queue := make([]string, 0, len(steps))
	for _, s := range steps {
		if inDegree[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}
	sort.Strings(queue) // deterministic output across runs

	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		next := append([]string(nil), dependents[id]...)
		sort.Strings(next)
		for _, dep := range next {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if visited < len(steps) {
		var cyclic []string
		for id, deg := range inDegree {
			if deg > 0 {
				cyclic = append(cyclic, id)
			}
		}
		sort.Strings(cyclic)
		report.errorf("", "cycle detected among steps: %s", strings.Join(cyclic, ", "))
	}
}

// checkArtifactReferences validates that every artifacts.download name is
// actually uploaded somewhere in the pipeline, and distinguishes two cases:
// an error if the name is uploaded nowhere at all (near-certain typo), a
// warning if it's uploaded by a step that isn't actually an upstream
// dependency (the download may run before or concurrently with the
// upload — likely a missing depends_on, but not necessarily wrong if the
// author intends artifacts_receive from a different pipeline entirely).
func checkArtifactReferences(report *LintReport, steps []*pipeline.Step) {
	byID := make(map[string]*pipeline.Step, len(steps))
	uploadedBy := make(map[string][]string) // artifact name -> step IDs that upload it
	for _, s := range steps {
		byID[s.ID] = s
		for _, u := range s.ArtifactUploads {
			name := u.Name
			if name == "" {
				name = filepath.Base(u.Path)
			}
			uploadedBy[name] = append(uploadedBy[name], s.ID)
		}
	}

	ancestors := func(startID string) map[string]bool {
		seen := map[string]bool{}
		var walk func(string)
		walk = func(id string) {
			s, ok := byID[id]
			if !ok {
				return
			}
			for _, dep := range s.DependsOn {
				if !seen[dep] {
					seen[dep] = true
					walk(dep)
				}
			}
		}
		walk(startID)
		return seen
	}

	for _, s := range steps {
		up := ancestors(s.ID)
		for _, d := range s.ArtifactDownloads {
			uploaders, uploadedAnywhere := uploadedBy[d.Name]
			if !uploadedAnywhere {
				report.errorf(s.ID, "downloads artifact %q, but no step in this pipeline uploads that name", d.Name)
				continue
			}
			inUpstream := false
			for _, u := range uploaders {
				if up[u] {
					inUpstream = true
					break
				}
			}
			if !inUpstream {
				report.warnf(s.ID, "downloads artifact %q, uploaded by %s — but that step isn't in %s's dependency chain, so ordering isn't guaranteed (missing depends_on?)",
					d.Name, strings.Join(uploaders, ", "), s.ID)
			}
		}
	}
}

// checkRedundantDependsOn flags a depends_on entry that's already implied
// by another entry in the same list
func checkRedundantDependsOn(report *LintReport, steps []*pipeline.Step) {
	byID := make(map[string]*pipeline.Step, len(steps))
	for _, s := range steps {
		byID[s.ID] = s
	}

	transitivelyReaches := func(fromID, targetID string) bool {
		seen := map[string]bool{}
		var walk func(string) bool
		walk = func(id string) bool {
			s, ok := byID[id]
			if !ok || seen[id] {
				return false
			}
			seen[id] = true
			for _, dep := range s.DependsOn {
				if dep == targetID || walk(dep) {
					return true
				}
			}
			return false
		}
		for _, dep := range byID[fromID].DependsOn {
			if dep != targetID && walk(dep) {
				return true
			}
		}
		return false
	}

	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if transitivelyReaches(s.ID, dep) {
				report.warnf(s.ID, "depends_on %q is redundant — already implied transitively through another listed dependency; keeping only the direct one clarifies the real ordering and may allow more parallelism", dep)
			}
		}
	}
}

// checkRawSteps runs the checks that need the author's literal, pre-
// normalization YAML: unrecognized type:, missing timeout, :latest images,
// long inline run: blocks, and script: files that don't exist on disk.
//
// workspaceDir == "" skips the script: existence check specifically (the
// other checks don't need a filesystem and still run) — used by callers
// with no meaningful local checkout to check paths against.
func checkRawSteps(report *LintReport, steps []JSONStep, workspaceDir string) {
	const longRunThreshold = 15

	for i, js := range steps {
		label := stepLabel(js.ID, i)

		if js.Type != "" && !recognizedTypes[js.Type] {
			report.errorf(label, "unrecognized type %q (expected one of: task, generator, pipeline, approval, release, docker_publish)", js.Type)
		}

		if js.Timeout == "" {
			report.warnf(label, "no timeout set — the default will be used, which may be surprising for a long-running step")
		}

		if img := js.Image; img != "" {
			if strings.HasSuffix(img, ":latest") || !strings.Contains(img, ":") {
				report.warnf(label, "image %q is not pinned to a specific version — builds won't be reproducible", img)
			}
		}

		if js.Run != "" {
			lines := strings.Count(strings.TrimRight(js.Run, "\n"), "\n") + 1
			if lines > longRunThreshold {
				report.warnf(label, "inline run: block is %d lines — consider extracting to a script: file for readability and reuse", lines)
			}
		}

		if js.Script != "" && workspaceDir != "" {
			scriptPath := filepath.Join(workspaceDir, filepath.FromSlash(strings.TrimPrefix(js.Script, "/")))
			if _, err := os.Stat(scriptPath); err != nil {
				report.errorf(label, "script %q not found at %s (script: paths are resolved relative to the workspace root — where you run `forge validate`/`forge run` from — not the pipeline file's own directory)", js.Script, scriptPath)
			}
		}
	}
}
