// Package stepretry describes a step's `retry:` setting: how many more times
// a failed step is run, and how long the agent waits in between.
package stepretry

import (
	"fmt"
	"strconv"
	"time"
)

// MaxRetries is the most retries a step may ask for. A step that needs more
// than this is broken, not flaky.
const MaxRetries = 5

// withKey is the key under which the setting travels to the agent in the
// step's parameter map, which is already carried from the compiler through
// the scheduler.
const withKey = "_forge.retry"

// Validate checks a `retry:` value for a step of the given type.
func Validate(retries int, stepType string) error {
	if retries == 0 {
		return nil
	}
	if retries < 0 || retries > MaxRetries {
		return fmt.Errorf("retry must be between 0 and %d, got %d", MaxRetries, retries)
	}
	switch stepType {
	case "", "task", "generator", "docker_build":
		return nil
	}
	return fmt.Errorf("retry is not supported on %q steps: only steps that an agent runs in a container, and image builds, can be retried", stepType)
}

// AddTo returns a copy of with that also carries the retry count. The
// original map is not modified; it may be shared with a template. With zero
// retries the map is returned unchanged.
func AddTo(with map[string]string, retries int) map[string]string {
	if retries <= 0 {
		return with
	}
	out := make(map[string]string, len(with)+1)
	for k, v := range with {
		out[k] = v
	}
	out[withKey] = strconv.Itoa(retries)
	return out
}

// FromWith reads the retry count a step carries. Anything missing,
// unreadable, or out of range yields a safe value: the agent never retries
// more than MaxRetries times, whatever it is sent.
func FromWith(with map[string]string) int {
	retries, err := strconv.Atoi(with[withKey])
	if err != nil || retries < 0 {
		return 0
	}
	if retries > MaxRetries {
		return MaxRetries
	}
	return retries
}

// Delay returns how long to wait before the given retry, counted from 1.
// The waits grow so that a short outage is ridden out by the later retries
// instead of all of them failing within seconds.
func Delay(retry int) time.Duration {
	switch {
	case retry <= 1:
		return 10 * time.Second
	case retry == 2:
		return 30 * time.Second
	default:
		return time.Minute
	}
}
