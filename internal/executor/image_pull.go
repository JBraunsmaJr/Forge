package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/dockerutil"
	forgelog "github.com/JBraunsmaJr/forge/internal/log"
)

// pullRetryDelays are the waits between attempts to pull a step's image.
// Four attempts spread over about a minute ride out a registry or mirror
// that is restarting, without holding a failing step for long.
var pullRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

// permanentPullErrors are fragments of the messages container engines print
// when pulling again cannot help: the image does not exist, or the registry
// refused the credentials.
var permanentPullErrors = []string{
	"manifest unknown",
	"not found",
	"repository does not exist",
	"pull access denied",
	"access denied",
	"unauthorized",
	"authentication required",
	"invalid reference format",
	"requested access to the resource is denied",
}

// imagePuller makes a step's image available on the agent before the step's
// container is created. Its collaborators are fields so the retry logic can
// be tested without a container engine.
type imagePuller struct {
	run    commandRunner
	info   func(string)
	warn   func(string)
	output func(string)
	sleep  func(ctx context.Context, d time.Duration) error
	delays []time.Duration
}

// newImagePuller returns a puller that drives the real container CLI and
// writes to the step's log.
func newImagePuller(logger *forgelog.Logger) imagePuller {
	return imagePuller{
		run:    runContainerCLI,
		info:   func(msg string) { logger.Info(msg) },
		warn:   func(msg string) { logger.Warn(msg) },
		output: logger.Output,
		sleep:  sleepContext,
		delays: pullRetryDelays,
	}
}

// ensure makes image available locally.
//
// An image the agent already has is used as it is, without contacting the
// registry, so a step whose image is cached keeps working while a registry
// or mirror is down. A missing image is pulled, and a pull that fails for a
// reason that may pass (a timeout, a refused connection, a 5xx answer) is
// retried with increasing waits.
func (p imagePuller) ensure(ctx context.Context, image string) error {
	image = dockerutil.ResolveImage(image)

	discard := func(string) {}
	if err := p.run(ctx, []string{"image", "inspect", "--format", "{{.Id}}", image}, discard); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	attempts := len(p.delays) + 1
	for attempt := 1; ; attempt++ {
		p.info(fmt.Sprintf("pulling image %s (attempt %d of %d)", image, attempt, attempts))

		var pullOutput strings.Builder
		err := p.run(ctx, []string{"pull", image}, func(line string) {
			pullOutput.WriteString(line)
			pullOutput.WriteByte('\n')
			p.output(line)
		})
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, exec.ErrNotFound) {
			// The container CLI itself is missing; waiting will not install it.
			return fmt.Errorf("pulling image %s: %w", image, err)
		}
		if reason := permanentPullError(pullOutput.String()); reason != "" {
			return fmt.Errorf("pulling image %s failed and will not be retried (%s): check the image name and the agent's registry login", image, reason)
		}
		if attempt == attempts {
			return fmt.Errorf("pulling image %s failed %d times: the registry or mirror may be unreachable from this agent", image, attempts)
		}

		delay := p.delays[attempt-1]
		p.warn(fmt.Sprintf("pulling image %s failed; retrying in %s", image, delay))
		if err := p.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// permanentPullError returns the fragment of output that marks a pull as
// not worth retrying, or "".
func permanentPullError(output string) string {
	output = strings.ToLower(output)
	for _, fragment := range permanentPullErrors {
		if strings.Contains(output, fragment) {
			return fragment
		}
	}
	return ""
}

// sleepContext waits for d, or returns the context's error if it ends first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
