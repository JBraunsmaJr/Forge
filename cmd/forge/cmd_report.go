package main

import (
	"bufio"
	"encoding/json"
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
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
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
			if ev.Action == "output" {
				// Output already carries its own trailing newline.
				// Unbuffered on purpose: these lines stream into live CI logs.
				os.Stdout.WriteString(ev.Output)
			}
		}
		return sc.Err()
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

	type pkgStats struct {
		durationMS int64
		tests      int
		passed     int
		failed     int
		skipped    int
	}
	packages := make(map[string]*pkgStats)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if _, ok := packages[e.Package]; !ok {
			packages[e.Package] = &pkgStats{}
		}
		pkg := packages[e.Package]
		switch e.Action {
		case "pass":
			if e.Test == "" { // package-level pass
				pkg.durationMS = int64(e.Elapsed * 1000)
			} else {
				pkg.tests++
				pkg.passed++
			}
		case "fail":
			if e.Test == "" {
				pkg.durationMS = int64(e.Elapsed * 1000)
			} else {
				pkg.tests++
				pkg.failed++
			}
		case "skip":
			pkg.tests++
			pkg.skipped++
		}
	}

	moduleName := getModuleName()
	var files []api.TestFileResult
	var totalDuration int64
	for pkg, stats := range packages {
		path := pkg
		if moduleName != "" && strings.HasPrefix(pkg, moduleName) {
			path = strings.TrimPrefix(pkg, moduleName)
			path = strings.TrimPrefix(path, "/")
		}

		files = append(files, api.TestFileResult{
			Path:       path,
			DurationMS: stats.durationMS,
			Tests:      stats.tests,
			Passed:     stats.passed,
			Failed:     stats.failed,
			Skipped:    stats.skipped,
		})
		totalDuration += stats.durationMS
	}

	report := api.TestReport{
		Version:         1,
		Framework:       "go-test",
		TotalDurationMS: totalDuration,
		Files:           files,
	}

	return writeReport(outputPath, report)
}

func getModuleName() string {
	data, err := os.ReadFile(filepath.Join(workspaceDir, "go.mod"))
	if err != nil {
		return ""
	}
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
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
