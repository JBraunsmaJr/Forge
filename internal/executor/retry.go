package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	forgelog "github.com/JBraunsmaJr/forge/internal/log"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
	"github.com/JBraunsmaJr/forge/internal/stepretry"
)

// RunStepWithRetry runs a step and, if the step asks for it with `retry:`,
// runs it again when it fails. The log of every attempt is kept, in order,
// in the step's log file.
//
// The step's timeout covers all attempts together: ctx is not renewed
// between them, so a step cannot run longer by being retried.
func (e *Executor) RunStepWithRetry(ctx context.Context, step *pipeline.Step) (*pipeline.StepResult, error) {
	retries := stepretry.FromWith(step.With)
	if retries == 0 {
		return e.RunStep(ctx, step)
	}
	return e.retryStep(ctx, step, retries, e.RunStep, sleepContext)
}

// stepRunner runs one attempt of a step.
type stepRunner func(ctx context.Context, step *pipeline.Step) (*pipeline.StepResult, error)

// retryStep is RunStepWithRetry with its collaborators passed in, so the
// loop can be tested without a container engine or real waits.
func (e *Executor) retryStep(ctx context.Context, step *pipeline.Step, retries int, run stepRunner,
	sleep func(context.Context, time.Duration) error) (*pipeline.StepResult, error) {

	var earlierLogs []string
	defer func() {
		for _, path := range earlierLogs {
			os.Remove(path)
		}
	}()

	attempts := retries + 1
	for attempt := 1; ; attempt++ {
		result, err := run(ctx, step)

		if attempt == attempts || !retryable(ctx, result) {
			keepEarlierLogs(step.ID, result, earlierLogs)
			return result, err
		}

		delay := stepretry.Delay(attempt)
		notice := fmt.Sprintf("↻ step failed (attempt %d of %d); retrying in %s", attempt, attempts, delay)
		e.announceRetry(step.ID, result.LogFile, notice)

		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			// Canceled or timed out while waiting: report the failed
			// attempt as the step's result.
			keepEarlierLogs(step.ID, result, earlierLogs)
			return result, sleepErr
		}

		// Set this attempt's log aside; the next attempt starts a new file.
		if result.LogFile != "" {
			saved := fmt.Sprintf("%s.attempt-%d", result.LogFile, attempt)
			if renameErr := os.Rename(result.LogFile, saved); renameErr == nil {
				earlierLogs = append(earlierLogs, saved)
			}
		}
	}
}

// keepEarlierLogs puts the logs of earlier attempts in front of the final
// attempt's log. Losing them is not worth failing the step for, so a problem
// is only reported.
func keepEarlierLogs(stepID string, result *pipeline.StepResult, earlierLogs []string) {
	if result == nil || len(earlierLogs) == 0 {
		return
	}
	if err := prependLogs(result.LogFile, earlierLogs); err != nil {
		fmt.Printf("[executor] step %s: could not keep the logs of earlier attempts: %v\n", stepID, err)
	}
}

// retryable reports whether an attempt's outcome is worth another attempt:
// the step ran and failed for a reason that may pass, and the step's time is
// not up.
func retryable(ctx context.Context, result *pipeline.StepResult) bool {
	return ctx.Err() == nil && result != nil && result.Status == pipeline.StatusFailed && !result.NoRetry
}

// announceRetry records the retry in the attempt's log, forwards it to
// anyone following the step live, and prints it for local runs.
func (e *Executor) announceRetry(stepID, logPath, notice string) {
	now := time.Now()
	fmt.Println(notice)
	if e.StreamCallback != nil {
		e.StreamCallback(stepID, now, forgelog.LevelWarn.String(), notice)
	}
	if logPath == "" {
		return
	}
	line, err := json.Marshal(forgelog.Event{
		Timestamp: now,
		Level:     forgelog.LevelWarn.String(),
		StepID:    stepID,
		Message:   notice,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// prependLogs rewrites the log file at path so that it starts with the
// contents of earlier, in order, followed by what it held before.
func prependLogs(path string, earlier []string) error {
	if path == "" || len(earlier) == 0 {
		return nil
	}

	merged, err := os.CreateTemp(filepath.Dir(path), ".merged-log-*")
	if err != nil {
		return err
	}
	mergedPath := merged.Name()
	defer os.Remove(mergedPath) // a no-op once the file has been renamed

	for _, source := range append(append([]string{}, earlier...), path) {
		if err := appendFile(merged, source); err != nil {
			merged.Close()
			return err
		}
	}
	if err := merged.Close(); err != nil {
		return err
	}
	return os.Rename(mergedPath, path)
}

func appendFile(dst io.Writer, sourcePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(dst, source)
	return err
}
