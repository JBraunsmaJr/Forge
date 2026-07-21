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
