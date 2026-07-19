package dockerutil

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// IsRunningInContainer checks if we are currently running inside a Docker container.
func IsRunningInContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// ParseContainerID extracts the container ID from Docker output,
// handling potential pull logs that may precede the ID.
func ParseContainerID(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	// The container ID is always the last line of output.
	return strings.TrimSpace(lines[len(lines)-1])
}

// DockerCp copies files between host and container with retries.
func DockerCp(ctx context.Context, src, dst string) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		cmd := exec.CommandContext(ctx, "docker", "cp", src, dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
			if i < 2 {
				time.Sleep(1 * time.Second)
				continue
			}
			return lastErr
		}
		return nil
	}
	return lastErr
}

// RunDockerCreate runs 'docker create' with the given arguments and streams output to logFn.
// It returns the created container ID.
func RunDockerCreate(ctx context.Context, logFn func(string), args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"create"}, args...)...)
	var outBuf bytes.Buffer

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return "", err
	}

	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if logFn != nil {
				logFn(line)
			}
			outBuf.WriteString(line + "\n")
		}
		close(done)
	}()

	err := cmd.Wait()
	pw.Close()
	<-done

	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(outBuf.String()))
	}

	return ParseContainerID(outBuf.Bytes()), nil
}

// DockerStopAndRm stops and removes a container.
func DockerStopAndRm(ctx context.Context, id string) {
	exec.CommandContext(ctx, "docker", "stop", "-t", "2", id).Run()
	exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
}
