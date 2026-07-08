package runner

import (
	"fmt"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

const (
	colorReset  = "\033[0m"
	colorGray   = "\033[90m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorBold   = "\033[1m"
	colorCyan   = "\033[36m"
)

// PrintSummary prints the end-of-run results table to stdout.
func PrintSummary(result *pipeline.RunResult) {
	fmt.Printf("\n%s%s── Run Summary ─────────────────────────────%s\n",
		colorBold, colorCyan, colorReset)

	for _, sr := range result.Steps {
		icon, color := statusDisplay(sr.Status)
		cacheTag := ""
		if sr.CacheHit {
			cacheTag = fmt.Sprintf("  %s(cache hit)%s", colorGray, colorReset)
		}
		fmt.Printf(
			"  %s%s%s  %-24s  %s%s%s%s\n",
			color, icon, colorReset,
			sr.Step.ID,
			colorGray, formatDuration(sr.Duration), colorReset,
			cacheTag,
		)
	}

	fmt.Printf("\n  Total: %s%s%s", colorGray, result.Duration.Round(time.Millisecond), colorReset)

	if result.Passed {
		fmt.Printf("  %s%s✓ PASSED%s\n\n", colorBold, colorGreen, colorReset)
	} else {
		fmt.Printf("  %s%s✗ FAILED%s\n\n", colorBold, colorRed, colorReset)
	}
}

func statusDisplay(s pipeline.StepStatus) (icon, color string) {
	switch s {
	case pipeline.StatusPassed:
		return "✓", colorGreen
	case pipeline.StatusFailed:
		return "✗", colorRed
	case pipeline.StatusSkipped:
		return "◎", colorCyan
	case pipeline.StatusCanceled:
		return "–", colorGray
	case pipeline.StatusRunning:
		return "●", colorYellow
	default:
		return "?", colorGray
	}
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "–"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(time.Millisecond).String()
}
