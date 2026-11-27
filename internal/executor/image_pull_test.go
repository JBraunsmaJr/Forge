package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeEngine stands in for the container CLI.
type fakeEngine struct {
	present     bool     // whether "image inspect" finds the image
	pullOutputs []string // output of each successive failing pull; "" + succeed when exhausted
	succeedAt   int      // pull attempt (1-based) that succeeds; 0 means never

	inspects int
	pulls    int
	slept    []time.Duration
	log      []string
	lastArgs []string
}

func (f *fakeEngine) puller() imagePuller {
	return imagePuller{
		run: func(_ context.Context, args []string, onLine func(string)) error {
			f.lastArgs = args
			switch args[0] {
			case "image":
				f.inspects++
				if f.present {
					return nil
				}
				return errors.New("exit status 1")
			case "pull":
				f.pulls++
				if f.succeedAt != 0 && f.pulls >= f.succeedAt {
					return nil
				}
				if i := f.pulls - 1; i < len(f.pullOutputs) {
					onLine(f.pullOutputs[i])
				}
				return errors.New("exit status 1")
			}
			return errors.New("unexpected command")
		},
		info:   func(msg string) { f.log = append(f.log, "info: "+msg) },
		warn:   func(msg string) { f.log = append(f.log, "warn: "+msg) },
		output: func(string) {},
		sleep: func(_ context.Context, d time.Duration) error {
			f.slept = append(f.slept, d)
			return nil
		},
		delays: []time.Duration{time.Second, 2 * time.Second, 3 * time.Second},
	}
}

const pullTestImage = "registry.example.com/team/api:1.0"

func TestImagePuller_CachedImageNeverContactsTheRegistry(t *testing.T) {
	engine := &fakeEngine{present: true}
	if err := engine.puller().ensure(context.Background(), pullTestImage); err != nil {
		t.Fatalf("ensure(): %v", err)
	}
	if engine.pulls != 0 {
		t.Errorf("pulled %d time(s) although the image is on the agent", engine.pulls)
	}
}

func TestImagePuller_MissingImageIsPulled(t *testing.T) {
	engine := &fakeEngine{succeedAt: 1}
	if err := engine.puller().ensure(context.Background(), pullTestImage); err != nil {
		t.Fatalf("ensure(): %v", err)
	}
	if engine.pulls != 1 || len(engine.slept) != 0 {
		t.Errorf("pulls = %d, waits = %v; want one pull and no wait", engine.pulls, engine.slept)
	}
	if got := engine.lastArgs; len(got) != 2 || got[0] != "pull" || got[1] != pullTestImage {
		t.Errorf("pull command = %v", got)
	}
}

func TestImagePuller_RetriesTemporaryFailures(t *testing.T) {
	engine := &fakeEngine{
		succeedAt: 3,
		pullOutputs: []string{
			"Error response from daemon: Get \"https://registry.example.com/v2/\": dial tcp: i/o timeout",
			"error pulling image: received unexpected HTTP status: 503 Service Unavailable",
		},
	}
	if err := engine.puller().ensure(context.Background(), pullTestImage); err != nil {
		t.Fatalf("ensure(): %v; want success on the third attempt", err)
	}
	if engine.pulls != 3 {
		t.Errorf("pulls = %d; want 3", engine.pulls)
	}
	if len(engine.slept) != 2 || engine.slept[0] != time.Second || engine.slept[1] != 2*time.Second {
		t.Errorf("waits = %v; want the first two delays, in order", engine.slept)
	}
	if joined := strings.Join(engine.log, "\n"); !strings.Contains(joined, "attempt 3 of 4") || !strings.Contains(joined, "warn: ") {
		t.Errorf("log does not show the attempts and a warning:\n%s", joined)
	}
}

func TestImagePuller_GivesUpAfterTheLastAttempt(t *testing.T) {
	engine := &fakeEngine{pullOutputs: []string{"connection refused", "connection refused", "connection refused", "connection refused"}}
	err := engine.puller().ensure(context.Background(), pullTestImage)
	if err == nil || !strings.Contains(err.Error(), "4 times") {
		t.Fatalf("ensure() = %v; want an error after four attempts", err)
	}
	if engine.pulls != 4 || len(engine.slept) != 3 {
		t.Errorf("pulls = %d, waits = %d; want 4 and 3", engine.pulls, len(engine.slept))
	}
}

func TestImagePuller_DoesNotRetryWhatCannotSucceed(t *testing.T) {
	outputs := []string{
		"Error response from daemon: manifest for registry.example.com/team/api:1.0 not found: manifest unknown",
		"Error response from daemon: pull access denied for team/api, repository does not exist or may require 'docker login'",
		"Error: initializing source: reading manifest 1.0: unauthorized: authentication required",
		"invalid reference format",
	}
	for _, output := range outputs {
		engine := &fakeEngine{pullOutputs: []string{output}}
		err := engine.puller().ensure(context.Background(), pullTestImage)
		if err == nil || !strings.Contains(err.Error(), "will not be retried") {
			t.Errorf("output %q: ensure() = %v; want a permanent failure", output, err)
		}
		if engine.pulls != 1 {
			t.Errorf("output %q: pulled %d time(s); want 1", output, engine.pulls)
		}
	}
}

func TestImagePuller_StopsWhenTheStepIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	engine := &fakeEngine{pullOutputs: []string{"connection refused"}}
	puller := engine.puller()
	puller.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel() // the step is canceled while waiting to retry
		return ctx.Err()
	}

	err := puller.ensure(ctx, pullTestImage)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ensure() = %v; want context.Canceled", err)
	}
	if engine.pulls != 1 {
		t.Errorf("pulled %d time(s) after cancellation; want 1", engine.pulls)
	}
}

func TestImagePuller_MissingCLIFailsAtOnce(t *testing.T) {
	engine := &fakeEngine{}
	puller := engine.puller()
	pulls := 0
	puller.run = func(_ context.Context, args []string, _ func(string)) error {
		if args[0] == "pull" {
			pulls++
		}
		return fmt.Errorf("starting docker: %w", exec.ErrNotFound)
	}
	if err := puller.ensure(context.Background(), pullTestImage); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("ensure() = %v; want the missing CLI reported", err)
	}
	if pulls != 1 || len(engine.slept) != 0 {
		t.Errorf("pulls = %d, waits = %d; want one attempt and no wait", pulls, len(engine.slept))
	}
}

func TestSleepContext(t *testing.T) {
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepContext() = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepContext(canceled) = %v; want context.Canceled", err)
	}
}
