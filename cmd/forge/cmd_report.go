package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/spf13/cobra"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Convert test framework output to Forge report format",
}

var workspaceDir string

var fromGoTestCmd = &cobra.Command{
	Use:   "from-go-test <input-file> [output-file]",
	Short: "Convert go test -json output",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]
		output := ".forge/test-report.json"
		if len(args) == 2 {
			output = args[1]
		}
		return fromGoTest(input, output)
	},
}

var streamGoTestCmd = &cobra.Command{
	Use:   "stream-go-test",
	Short: "Convert `go test -json` on stdin to human-readable output",
	Long: `Reads go test -json events from stdin and prints only the human-readable
test output, so CI log viewers show classic "go test -v" text instead of raw
JSONL. Non-JSON lines (e.g. build errors) pass through untouched.

Typical use, keeping the raw JSON for from-go-test:

  go test -v -json ./... 2>&1 | tee /tmp/go-test.json | forge report stream-go-test
  forge report from-go-test /tmp/go-test.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		sc := bufio.NewScanner(os.Stdin)
		// go test output lines can be long (giant assertion diffs).
		sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			var ev struct {
				Action string `json:"Action"`
				Output string `json:"Output"`
			}
			if err := json.Unmarshal(line, &ev); err != nil || ev.Action == "" {
				// Not a go-test event — pass through as-is.
				os.Stdout.Write(append(line, '\n'))
				continue
			}
			if ev.Output != "" {
				// Output already carries its own trailing newline.
				// Unbuffered on purpose: these lines stream into live CI logs.
				os.Stdout.WriteString(ev.Output)
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "stream-go-test: scanner error: %v\n", err)
			return err
		}
		return nil
	},
}

var fromPytestCmd = &cobra.Command{
	Use:   "from-pytest <input-file> [output-file]",
	Short: "Convert pytest --json-report output",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]
		output := ".forge/test-report.json"
		if len(args) == 2 {
			output = args[1]
		}
		return fromPytest(input, output)
	},
}

var fromJestCmd = &cobra.Command{
	Use:   "from-jest <input-file> [output-file]",
	Short: "Convert jest --json output",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]
		output := ".forge/test-report.json"
		if len(args) == 2 {
			output = args[1]
		}
		return fromJest(input, output)
	},
}

var fromRSpecCmd = &cobra.Command{
	Use:   "from-rspec <input-file> [output-file]",
	Short: "Convert rspec --format json output",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]
		output := ".forge/test-report.json"
		if len(args) == 2 {
			output = args[1]
		}
		return fromRSpec(input, output)
	},
}

func init() {
	reportCmd.PersistentFlags().StringVar(&workspaceDir, "workspace", ".", "Workspace root directory")
	reportCmd.AddCommand(fromGoTestCmd)
	reportCmd.AddCommand(streamGoTestCmd)
	reportCmd.AddCommand(fromPytestCmd)
	reportCmd.AddCommand(fromJestCmd)
	reportCmd.AddCommand(fromRSpecCmd)
	rootCmd.AddCommand(reportCmd)
}

// fromGoTest converts `go test -json` output into a Forge test report.
//
// Granularity note: entries are keyed by top-level test FUNCTION NAME
// (e.g. "TestAuth_Login"), not by source file. go test -json has no
// concept of "which .go file is this test defined in" — that's
// compile-time-only information the test binary doesn't retain at
// runtime, so file-level splitting isn't something Go's own tooling
// can report. Test-function-name is the finest granularity actually
// available, and it composes with go test's own -run flag: Forge's
// shard-assignment logic (internal/scheduler/test_split.go) puts each
// shard's assigned names into FORGE_TEST_FILES exactly like it would
// file paths for other frameworks, and the calling script builds a
// `-run "^(Name1|Name2)$"` regex from it — see
// .forge/scripts/integration-tests.sh.
// fromGoTest converts `go test -json` output into a Forge test report.
//
// Granularity note: entries are keyed by top-level test FUNCTION NAME
// (e.g. "TestAuth_Login"), not by source file. go test -json has no
// concept of "which .go file is this test defined in" — that's
// compile-time-only information the test binary doesn't retain at
// runtime, so file-level splitting isn't something Go's own tooling
// can report. Test-function-name is the finest granularity actually
// available, and it composes with go test's own -run flag: Forge's
// shard-assignment logic (internal/scheduler/test_split.go) puts each
// shard's assigned names into FORGE_TEST_FILES exactly like it would
// file paths for other frameworks, and the calling script builds a
// `-run "^(Name1|Name2)$"` regex from it — see
// .forge/scripts/integration-tests.sh.
//
// Package-collision note: two different packages' identically-named
// top-level tests (e.g. two "TestBasic" in different packages) are
// tracked as separate entries internally (keyed by package+test, not
// test name alone), so their durations/outcomes never silently merge
// or overwrite each other — that used to happen when this was keyed by
// name alone. The *emitted* Path stays the plain test name in the
// common case (a single-package run, e.g. today's ./tests/integration/...
// usage, where no collision is structurally possible — Go doesn't
// allow two functions of the same name in one package), since that's
// what go test's own -run flag matches against. Only when a name
// really is ambiguous across packages does the emitted Path get a
// "package: name" prefix to keep the two entries distinguishable — at
// which point selecting that shard's tests via a plain -run regex
// would need further work to scope by package too; today's only
// caller never triggers this case.
func fromGoTest(inputPath, outputPath string) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	type goTestEvent struct {
		Action  string  `json:"Action"`
		Package string  `json:"Package"`
		Test    string  `json:"Test"`
		Elapsed float64 `json:"Elapsed"` // seconds
	}

	type testStats struct {
		pkg        string
		durationMS int64
		passed     bool
		failed     bool
		skipped    bool
	}
	// Keyed by package + a separator unlikely to appear in either a
	// package path or a test name, so a same-named test in two
	// different packages never collides in this map.
	const keySep = "\x1f"
	tests := make(map[string]*testStats)
	// Preserve first-seen order so the report's file list isn't
	// randomized run to run (map iteration order isn't stable, and a
	// stable order makes diffing/reading reports across runs saner).
	var order []string
	// How many distinct packages have produced a top-level test with
	// this exact name — >1 means the name is genuinely ambiguous and
	// needs disambiguating when emitted.
	nameSeenInPackages := make(map[string]map[string]bool)

	scanner := bufio.NewScanner(f)
	// go test output lines can be long (giant assertion diffs).
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		var e goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Test == "" || strings.Contains(e.Test, "/") {
			// e.Test == "" is a package-level event (build result,
			// package pass/fail summary) — not an individual test.
			// A "/" means this is a subtest event (e.g.
			// "TestFoo/some_case"); skip it, since the top-level
			// "TestFoo" event's Elapsed already includes all of its
			// subtests' time, and -run selects/deselects by the
			// top-level name, not per-subtest.
			continue
		}
		key := e.Package + keySep + e.Test
		t, ok := tests[key]
		if !ok {
			t = &testStats{pkg: e.Package}
			tests[key] = t
			order = append(order, key)
			if nameSeenInPackages[e.Test] == nil {
				nameSeenInPackages[e.Test] = make(map[string]bool)
			}
			nameSeenInPackages[e.Test][e.Package] = true
		}
		switch e.Action {
		case "pass":
			t.durationMS = int64(e.Elapsed * 1000)
			t.passed = true
		case "fail":
			t.durationMS = int64(e.Elapsed * 1000)
			t.failed = true
		case "skip":
			t.durationMS = int64(e.Elapsed * 1000)
			t.skipped = true
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning go test output: %w", err)
	}

	var files []api.TestFileResult
	var totalDuration int64
	for _, key := range order {
		t := tests[key]
		name := key[len(t.pkg)+len(keySep):]

		path := name
		if len(nameSeenInPackages[name]) > 1 {
			// Genuinely ambiguous — disambiguate rather than let two
			// entries collide under the same emitted Path.
			path = t.pkg + ": " + name
		}

		passed, failed, skipped := 0, 0, 0
		switch {
		case t.failed:
			failed = 1
		case t.skipped:
			skipped = 1
		case t.passed:
			passed = 1
		default:
			// Started but never resolved to pass/fail/skip (binary
			// crashed mid-test, panic, timeout) — count it as a
			// failure rather than silently dropping it from totals.
			failed = 1
		}
		files = append(files, api.TestFileResult{
			Path:       path,
			DurationMS: t.durationMS,
			Tests:      1,
			Passed:     passed,
			Failed:     failed,
			Skipped:    skipped,
		})
		totalDuration += t.durationMS
	}

	report := api.TestReport{
		Version:         1,
		Framework:       "go-test",
		TotalDurationMS: totalDuration,
		Files:           files,
	}

	return writeReport(outputPath, report)
}

func fromPytest(inputPath, outputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	var pytestReport struct {
		Duration float64 `json:"duration"`
		Tests    []struct {
			NodeID   string  `json:"nodeid"`
			Outcome  string  `json:"outcome"`
			Duration float64 `json:"duration"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &pytestReport); err != nil {
		return err
	}

	filesMap := make(map[string]*api.TestFileResult)
	for _, t := range pytestReport.Tests {
		parts := strings.SplitN(t.NodeID, "::", 2)
		filePath := parts[0]
		filePath = makeRelative(filePath)

		if _, ok := filesMap[filePath]; !ok {
			filesMap[filePath] = &api.TestFileResult{Path: filePath}
		}
		f := filesMap[filePath]
		f.DurationMS += int64(t.Duration * 1000)
		f.Tests++
		switch t.Outcome {
		case "passed":
			f.Passed++
		case "failed":
			f.Failed++
		case "skipped":
			f.Skipped++
		}
	}

	var files []api.TestFileResult
	for _, f := range filesMap {
		files = append(files, *f)
	}

	report := api.TestReport{
		Version:         1,
		Framework:       "pytest",
		TotalDurationMS: int64(pytestReport.Duration * 1000),
		Files:           files,
	}
	return writeReport(outputPath, report)
}

func fromJest(inputPath, outputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	var jestReport struct {
		TestResults []struct {
			TestFilePath string `json:"testFilePath"`
			PerfStats    struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"perfStats"`
			NumPassingTests int `json:"numPassingTests"`
			NumFailingTests int `json:"numFailingTests"`
			NumPendingTests int `json:"numPendingTests"`
		} `json:"testResults"`
	}
	if err := json.Unmarshal(data, &jestReport); err != nil {
		return err
	}

	var files []api.TestFileResult
	var totalDuration int64
	for _, tr := range jestReport.TestResults {
		duration := tr.PerfStats.End - tr.PerfStats.Start
		files = append(files, api.TestFileResult{
			Path:       makeRelative(tr.TestFilePath),
			DurationMS: duration,
			Passed:     tr.NumPassingTests,
			Failed:     tr.NumFailingTests,
			Skipped:    tr.NumPendingTests,
			Tests:      tr.NumPassingTests + tr.NumFailingTests + tr.NumPendingTests,
		})
		totalDuration += duration
	}

	report := api.TestReport{
		Version:         1,
		Framework:       "jest",
		TotalDurationMS: totalDuration,
		Files:           files,
	}
	return writeReport(outputPath, report)
}

func fromRSpec(inputPath, outputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	var rspecReport struct {
		Summary struct {
			Duration float64 `json:"duration"`
		} `json:"summary"`
		Examples []struct {
			FilePath string  `json:"file_path"`
			Status   string  `json:"status"`
			RunTime  float64 `json:"run_time"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(data, &rspecReport); err != nil {
		return err
	}

	filesMap := make(map[string]*api.TestFileResult)
	for _, e := range rspecReport.Examples {
		filePath := makeRelative(e.FilePath)
		if _, ok := filesMap[filePath]; !ok {
			filesMap[filePath] = &api.TestFileResult{Path: filePath}
		}
		f := filesMap[filePath]
		f.DurationMS += int64(e.RunTime * 1000)
		f.Tests++
		switch e.Status {
		case "passed":
			f.Passed++
		case "failed":
			f.Failed++
		case "pending":
			f.Skipped++
		}
	}

	var files []api.TestFileResult
	for _, f := range filesMap {
		files = append(files, *f)
	}

	report := api.TestReport{
		Version:         1,
		Framework:       "rspec",
		TotalDurationMS: int64(rspecReport.Summary.Duration * 1000),
		Files:           files,
	}
	return writeReport(outputPath, report)
}

func writeReport(path string, report api.TestReport) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func makeRelative(path string) string {
	absWorkspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return path
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		// If it's already relative but Abs fails for some reason, just trim prefix ./
		return strings.TrimPrefix(path, "./")
	}

	rel, err := filepath.Rel(absWorkspace, absPath)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
