package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/imagebuild"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// recordedBuild is an imageBuild whose CLI is replaced by a recorder.
type recordedBuild struct {
	imageBuild
	commands [][]string
	info     []string
	// authFiles holds the content of the credentials file as it was when
	// each command ran, since the file is removed afterwards.
	authFiles []string
	failOn    string // subcommand to fail, e.g. "push"
}

func newRecordedBuild(t *testing.T, cli imagebuild.CLI) *recordedBuild {
	t.Helper()
	r := &recordedBuild{}
	r.imageBuild = imageBuild{
		workspaceDir: t.TempDir(),
		cli:          cli,
		info:         func(msg string) { r.info = append(r.info, msg) },
		output:       func(string) {},
		run: func(_ context.Context, args []string, onLine func(string)) error {
			r.commands = append(r.commands, args)
			r.authFiles = append(r.authFiles, readAuthFile(args))
			if r.failOn != "" && contains(args, r.failOn) {
				return errors.New("exit status 1")
			}
			if contains(args, "push") {
				onLine("latest: digest: " + testDigest + " size: 1234")
			}
			return nil
		},
	}
	return r
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// readAuthFile returns the content of the credentials file a command points
// at, or "" if it points at none.
func readAuthFile(args []string) string {
	for i, a := range args {
		if i+1 >= len(args) {
			break
		}
		path := ""
		switch a {
		case "--config":
			path = filepath.Join(args[i+1], imagebuild.AuthFileName)
		case "--authfile":
			path = args[i+1]
		}
		if path != "" {
			data, _ := os.ReadFile(path)
			return string(data)
		}
	}
	return ""
}

func buildStep(t *testing.T, cfg imagebuild.Config, env map[string]string) *pipeline.Step {
	t.Helper()
	encoded, err := cfg.Encode()
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	return &pipeline.Step{
		ID:   "build-image",
		Type: imagebuild.StepType,
		With: map[string]string{imagebuild.WithKey: encoded},
		Env:  env,
	}
}

func testBuildConfig() imagebuild.Config {
	return imagebuild.Config{
		Registry:   "registry.example.com",
		Repository: "team/api",
		Tags:       []string{"test-${{ env.FORGE_BUILD_NUMBER }}", "candidate"},
	}
}

func TestImageBuild_BuildsOnceThenPushesEveryTag(t *testing.T) {
	for _, cli := range []imagebuild.CLI{imagebuild.Docker, imagebuild.Podman} {
		r := newRecordedBuild(t, cli)
		step := buildStep(t, testBuildConfig(), map[string]string{"FORGE_BUILD_NUMBER": "42"})

		if err := r.execute(context.Background(), step); err != nil {
			t.Fatalf("execute(): %v", err)
		}
		if len(r.commands) != 3 {
			t.Fatalf("ran %d commands; want one build and two pushes: %v", len(r.commands), r.commands)
		}
		if !contains(r.commands[0], "build") || !contains(r.commands[0], "registry.example.com/team/api:test-42") {
			t.Errorf("first command = %v; want a build tagged with the expanded tag", r.commands[0])
		}
		wantPushed := []string{"registry.example.com/team/api:test-42", "registry.example.com/team/api:candidate"}
		for i, ref := range wantPushed {
			cmd := r.commands[i+1]
			if !contains(cmd, "push") || cmd[len(cmd)-1] != ref {
				t.Errorf("command %d = %v; want a push of %s", i+1, cmd, ref)
			}
		}
		if joined := strings.Join(r.info, "\n"); !strings.Contains(joined, "pushed registry.example.com/team/api:test-42@"+testDigest) {
			t.Errorf("log does not record the pushed digest:\n%s", joined)
		}
	}
}

func TestImageBuild_Credentials(t *testing.T) {
	t.Run("both secrets are written to a private file and removed afterwards", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		step := buildStep(t, testBuildConfig(), map[string]string{
			"FORGE_BUILD_NUMBER": "42",
			"REGISTRY_USERNAME":  "builder",
			"REGISTRY_PASSWORD":  "s3cret",
		})
		if err := r.execute(context.Background(), step); err != nil {
			t.Fatalf("execute(): %v", err)
		}
		for i, content := range r.authFiles {
			if !strings.Contains(content, "registry.example.com") {
				t.Errorf("command %d ran without credentials for the registry", i)
			}
			if strings.Contains(content, "s3cret") {
				t.Errorf("command %d: the credentials file holds the password in plain text", i)
			}
		}
		for _, cmd := range r.commands {
			if strings.Contains(strings.Join(cmd, " "), "s3cret") {
				t.Errorf("the password appears on the command line: %v", cmd)
			}
			if strings.HasPrefix(cmd[1], r.workspaceDir) {
				t.Errorf("credentials directory %s is inside the workspace", cmd[1])
			}
			if _, err := os.Stat(cmd[1]); !os.IsNotExist(err) {
				t.Errorf("credentials directory %s still exists after the step", cmd[1])
			}
		}
	})

	t.Run("no secrets means an anonymous push", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		step := buildStep(t, testBuildConfig(), map[string]string{"FORGE_BUILD_NUMBER": "42"})
		if err := r.execute(context.Background(), step); err != nil {
			t.Fatalf("execute(): %v", err)
		}
		for _, cmd := range r.commands {
			if contains(cmd, "--config") || contains(cmd, "--authfile") {
				t.Errorf("command carries credential flags without credentials: %v", cmd)
			}
		}
	})

	t.Run("one secret without the other is an error", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		step := buildStep(t, testBuildConfig(), map[string]string{
			"FORGE_BUILD_NUMBER": "42",
			"REGISTRY_USERNAME":  "builder",
		})
		err := r.execute(context.Background(), step)
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Errorf("execute() = %v; want incomplete credentials reported", err)
		}
		if len(r.commands) != 0 {
			t.Errorf("ran %v despite incomplete credentials", r.commands)
		}
	})
}

func TestImageBuild_Failures(t *testing.T) {
	t.Run("a failed build pushes nothing", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		r.failOn = "build"
		step := buildStep(t, testBuildConfig(), map[string]string{"FORGE_BUILD_NUMBER": "42"})
		if err := r.execute(context.Background(), step); err == nil {
			t.Fatal("execute() succeeded although the build failed")
		}
		if len(r.commands) != 1 {
			t.Errorf("ran %d commands; want the build only: %v", len(r.commands), r.commands)
		}
	})

	t.Run("a failed push fails the step and names the reference", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		r.failOn = "push"
		step := buildStep(t, testBuildConfig(), map[string]string{"FORGE_BUILD_NUMBER": "42"})
		err := r.execute(context.Background(), step)
		if err == nil || !strings.Contains(err.Error(), "registry.example.com/team/api:test-42") {
			t.Errorf("execute() = %v; want the failed reference named", err)
		}
	})

	t.Run("an unresolved placeholder never reaches the CLI", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		step := buildStep(t, testBuildConfig(), nil) // FORGE_BUILD_NUMBER is not set
		if err := r.execute(context.Background(), step); err == nil {
			t.Fatal("execute() accepted a tag with an unresolved placeholder")
		}
		if len(r.commands) != 0 {
			t.Errorf("ran %v with an invalid configuration", r.commands)
		}
	})

	t.Run("a step without configuration is an error", func(t *testing.T) {
		r := newRecordedBuild(t, imagebuild.Docker)
		step := &pipeline.Step{ID: "build-image", Type: imagebuild.StepType}
		if err := r.execute(context.Background(), step); err == nil {
			t.Fatal("execute() accepted a step with no docker_build configuration")
		}
	})
}

func TestImageBuild_PodmanDigestFile(t *testing.T) {
	r := newRecordedBuild(t, imagebuild.Podman)
	// Podman prints no digest; it writes one to the file it is given.
	r.run = func(_ context.Context, args []string, _ func(string)) error {
		for i, a := range args {
			if a == "--digestfile" {
				return os.WriteFile(args[i+1], []byte(testDigest), 0o600)
			}
		}
		return nil
	}
	cfg := testBuildConfig()
	cfg.Tags = []string{"v1"}
	if err := r.execute(context.Background(), buildStep(t, cfg, nil)); err != nil {
		t.Fatalf("execute(): %v", err)
	}
	if joined := strings.Join(r.info, "\n"); !strings.Contains(joined, "@"+testDigest) {
		t.Errorf("log does not record the digest from the digest file:\n%s", joined)
	}
}
