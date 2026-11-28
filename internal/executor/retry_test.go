package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
	"github.com/JBraunsmaJr/forge/internal/stepretry"
)

// scriptedStep stands in for running a step: each attempt writes a line to
// the step's log, as RunStep does (creating the file anew), and ends with
// the next scripted status.
type scriptedStep struct {
	logPath  string
	statuses []pipeline.StepStatus
	attempts int
}

func (s *scriptedStep) run(_ context.Context, step *pipeline.Step) (*pipeline.StepResult, error) {
	s.attempts++
	status := s.statuses[len(s.statuses)-1]
	if s.attempts <= len(s.statuses) {
		status = s.statuses[s.attempts-1]
	}
	line := `{"level":"INFO","message":"output of attempt ` + string(rune('0'+s.attempts)) + `"}` + "\n"
	if err := os.WriteFile(s.logPath, []byte(line), 0o644); err != nil {
		return nil, err
	}
	result := &pipeline.StepResult{Step: step, Status: status, LogFile: s.logPath}
	if status == pipeline.StatusFailed {
		result.ExitCode = 1
		return result, errors.New("exit status 1")
	}
	return result, nil
}

type retryHarness struct {
	exec   *Executor
	script *scriptedStep
	step   *pipeline.Step
	waits  []time.Duration
	live   []string
}

func newRetryHarness(t *testing.T, statuses ...pipeline.StepStatus) *retryHarness {
	t.Helper()
	h := &retryHarness{
		script: &scriptedStep{logPath: filepath.Join(t.TempDir(), "build.jsonl"), statuses: statuses},
		step:   &pipeline.Step{ID: "build"},
	}
	h.exec = &Executor{StreamCallback: func(_ string, _ time.Time, level, message string) {
		h.live = append(h.live, level+": "+message)
	}}
	return h
}

func (h *retryHarness) retry(ctx context.Context, retries int) (*pipeline.StepResult, error) {
	sleep := func(ctx context.Context, d time.Duration) error {
		h.waits = append(h.waits, d)
		return ctx.Err()
	}
	return h.exec.retryStep(ctx, h.step, retries, h.script.run, sleep)
}

func (h *retryHarness) log(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.script.logPath)
	if err != nil {
		t.Fatalf("reading the step log: %v", err)
	}
	return string(data)
}

func TestRetryStep_PassesOnALaterAttempt(t *testing.T) {
	h := newRetryHarness(t, pipeline.StatusFailed, pipeline.StatusFailed, pipeline.StatusPassed)

	result, err := h.retry(context.Background(), 3)
	if err != nil || result.Status != pipeline.StatusPassed {
		t.Fatalf("retryStep() = %+v, %v; want the step passed", result, err)
	}
	if h.script.attempts != 3 {
		t.Errorf("ran %d attempts; want 3 (it must stop once the step passes)", h.script.attempts)
	}
	if len(h.waits) != 2 || h.waits[0] != stepretry.Delay(1) || h.waits[1] != stepretry.Delay(2) {
		t.Errorf("waits = %v; want the first two retry delays", h.waits)
	}

	// The log holds every attempt, in order, with a notice between them.
	log := h.log(t)
	order := []string{"output of attempt 1", "attempt 1 of 4", "output of attempt 2", "attempt 2 of 4", "output of attempt 3"}
	position := 0
	for _, want := range order {
		i := strings.Index(log[position:], want)
		if i < 0 {
			t.Fatalf("log is missing %q, or has it out of order:\n%s", want, log)
		}
		position += i
	}
	if len(h.live) != 2 || !strings.HasPrefix(h.live[0], "WARN: ") {
		t.Errorf("live notices = %v; want one warning per retry", h.live)
	}

	// No per-attempt files are left behind.
	leftovers, _ := filepath.Glob(h.script.logPath + ".attempt-*")
	if len(leftovers) != 0 {
		t.Errorf("left behind %v", leftovers)
	}
}

func TestRetryStep_GivesUpAfterTheLastRetry(t *testing.T) {
	h := newRetryHarness(t, pipeline.StatusFailed)

	result, err := h.retry(context.Background(), 2)
	if err == nil || result.Status != pipeline.StatusFailed {
		t.Fatalf("retryStep() = %+v, %v; want the step failed", result, err)
	}
	if h.script.attempts != 3 {
		t.Errorf("ran %d attempts; want 3 (one run and two retries)", h.script.attempts)
	}
	log := h.log(t)
	for _, want := range []string{"output of attempt 1", "output of attempt 2", "output of attempt 3"} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "attempt 3 of 3); retrying") {
		t.Errorf("log announces a retry after the last attempt:\n%s", log)
	}
}

func TestRetryStep_DoesNotRetryWhatDidNotFail(t *testing.T) {
	for _, status := range []pipeline.StepStatus{pipeline.StatusPassed, pipeline.StatusSkipped} {
		h := newRetryHarness(t, status)
		if _, err := h.retry(context.Background(), 3); err != nil {
			t.Errorf("status %s: retryStep() = %v", status, err)
		}
		if h.script.attempts != 1 {
			t.Errorf("status %s: ran %d attempts; want 1", status, h.script.attempts)
		}
	}
}

func TestRetryStep_StopsWhenTheStepsTimeIsUp(t *testing.T) {
	t.Run("already expired when the attempt ends", func(t *testing.T) {
		h := newRetryHarness(t, pipeline.StatusFailed)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h.retry(ctx, 3)
		if h.script.attempts != 1 || len(h.waits) != 0 {
			t.Errorf("attempts = %d, waits = %d; want one attempt and no wait", h.script.attempts, len(h.waits))
		}
	})

	t.Run("expires while waiting to retry", func(t *testing.T) {
		h := newRetryHarness(t, pipeline.StatusFailed, pipeline.StatusFailed)
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		sleep := func(ctx context.Context, _ time.Duration) error {
			calls++
			if calls == 2 {
				cancel()
			}
			return ctx.Err()
		}
		result, err := h.exec.retryStep(ctx, h.step, 3, h.script.run, sleep)
		if !errors.Is(err, context.Canceled) || result == nil || result.Status != pipeline.StatusFailed {
			t.Fatalf("retryStep() = %+v, %v; want the failed attempt and context.Canceled", result, err)
		}
		if h.script.attempts != 2 {
			t.Errorf("ran %d attempts; want 2", h.script.attempts)
		}
		log := h.log(t)
		if !strings.Contains(log, "output of attempt 1") || !strings.Contains(log, "output of attempt 2") {
			t.Errorf("log lost an attempt:\n%s", log)
		}
	})
}

func TestRunStepWithRetry_ReadsTheStepsSetting(t *testing.T) {
	// A step with an unresolvable image fails before any container command,
	// so this exercises the real RunStep without a container engine.
	exec, err := New(t.TempDir(), t.TempDir(), "test-agent", nil)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	step := &pipeline.Step{ID: "test", Image: "alpine:${{ env.NOT_SET }}", Command: []string{"true"}}

	// Without the setting there is exactly one attempt and no retry notice.
	if _, err := exec.RunStepWithRetry(context.Background(), step); err == nil {
		t.Fatal("RunStepWithRetry succeeded; want the image refused")
	}
	log, _ := os.ReadFile(filepath.Join(exec.LogDir, "test.jsonl"))
	if strings.Contains(string(log), "retrying") {
		t.Errorf("a step without retry: was retried:\n%s", log)
	}

	// With the setting, a failure that another attempt cannot fix is still
	// not retried. (If it were, this test would wait for the retry delay.)
	step.With = stepretry.AddTo(nil, 3)
	start := time.Now()
	result, err := exec.RunStepWithRetry(context.Background(), step)
	if err == nil || result == nil || !result.NoRetry {
		t.Fatalf("RunStepWithRetry() = %+v, %v; want a failure marked as not retryable", result, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v; an unusable step definition must fail at once", time.Since(start))
	}
}

func TestRetryStep_DoesNotRetryAPermanentFailure(t *testing.T) {
	h := newRetryHarness(t, pipeline.StatusFailed)
	run := func(ctx context.Context, step *pipeline.Step) (*pipeline.StepResult, error) {
		result, err := h.script.run(ctx, step)
		result.NoRetry = true
		return result, err
	}
	sleep := func(context.Context, time.Duration) error { t.Error("waited to retry a permanent failure"); return nil }
	if _, err := h.exec.retryStep(context.Background(), h.step, 3, run, sleep); err == nil {
		t.Error("retryStep() succeeded; want the failure reported")
	}
	if h.script.attempts != 1 {
		t.Errorf("ran %d attempts; want 1", h.script.attempts)
	}
}
