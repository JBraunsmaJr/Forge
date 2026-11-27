package imagebuild

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		Registry:   "registry.example.com",
		Repository: "team/api",
		Tags:       []string{"test-42"},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring; "" means valid
	}{
		{"minimal", func(c *Config) {}, ""},
		{"registry with a port", func(c *Config) { c.Registry = "localhost:5000" }, ""},
		{"nested repository", func(c *Config) { c.Repository = "group/sub-group/my_app.api" }, ""},
		{"several tags", func(c *Config) { c.Tags = []string{"1.2.3", "latest", "test_42"} }, ""},
		{"context and dockerfile", func(c *Config) { c.Context = "services/api"; c.Dockerfile = "services/api/Dockerfile.prod" }, ""},
		{"context with a harmless dot-dot", func(c *Config) { c.Context = "services/../services/api" }, ""},
		{"target and build args", func(c *Config) { c.Target = "runtime"; c.BuildArgs = map[string]string{"VERSION": "1.2.3; rm -rf /"} }, ""},

		{"no registry", func(c *Config) { c.Registry = "" }, "registry is required"},
		{"registry with a scheme", func(c *Config) { c.Registry = "https://registry.example.com" }, "not a registry host"},
		{"registry with a path", func(c *Config) { c.Registry = "registry.example.com/team" }, "not a registry host"},
		{"registry that is a flag", func(c *Config) { c.Registry = "--privileged" }, "not a registry host"},
		{"no repository", func(c *Config) { c.Repository = "" }, "repository is required"},
		{"uppercase repository", func(c *Config) { c.Repository = "Team/API" }, "not a valid image path"},
		{"repository with a tag", func(c *Config) { c.Repository = "team/api:latest" }, "not a valid image path"},
		{"repository with a space", func(c *Config) { c.Repository = "team/api --build-arg X" }, "not a valid image path"},
		{"no tags", func(c *Config) { c.Tags = nil }, "at least one tag"},
		{"empty tag", func(c *Config) { c.Tags = []string{""} }, "not a valid tag"},
		{"tag with a slash", func(c *Config) { c.Tags = []string{"feature/x"} }, "not a valid tag"},
		{"tag starting with a dash", func(c *Config) { c.Tags = []string{"-rf"} }, "not a valid tag"},
		{"unresolved placeholder in a tag", func(c *Config) { c.Tags = []string{"test-${{ env.N }}"} }, "not a valid tag"},
		{"duplicate tag", func(c *Config) { c.Tags = []string{"a", "a"} }, "listed twice"},
		{"bad target", func(c *Config) { c.Target = "--no-cache" }, "not a valid stage name"},
		{"bad build arg name", func(c *Config) { c.BuildArgs = map[string]string{"X=1 --privileged": "v"} }, "not a valid argument name"},
		{"absolute context", func(c *Config) { c.Context = "/etc" }, "must be relative"},
		{"context outside the workspace", func(c *Config) { c.Context = "../other" }, "must stay inside"},
		{"context climbing out through a subdirectory", func(c *Config) { c.Context = "a/../../other" }, "must stay inside"},
		{"absolute dockerfile", func(c *Config) { c.Dockerfile = "/etc/passwd" }, "must be relative"},
		{"dockerfile outside the workspace", func(c *Config) { c.Dockerfile = "../Dockerfile" }, "must stay inside"},
		{"windows absolute path", func(c *Config) { c.Context = `C:\Users` }, "must be relative"},
		{"windows-style climb", func(c *Config) { c.Context = `..\other` }, "must stay inside"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v; want valid", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("Validate() accepted the configuration; want an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("Validate() = %q; want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestReferences(t *testing.T) {
	cfg := validConfig()
	cfg.Tags = []string{"test-42", "latest"}
	want := []string{"registry.example.com/team/api:test-42", "registry.example.com/team/api:latest"}
	if got := cfg.References(); !reflect.DeepEqual(got, want) {
		t.Errorf("References() = %v; want %v", got, want)
	}
}

func TestPaths(t *testing.T) {
	workspace := filepath.Join(string(filepath.Separator), "work", "ws")

	cfg := validConfig()
	if got, want := cfg.ContextDir(workspace), workspace; got != want {
		t.Errorf("ContextDir() with no context = %q; want the workspace %q", got, want)
	}
	if got, want := cfg.DockerfilePath(workspace), filepath.Join(workspace, "Dockerfile"); got != want {
		t.Errorf("DockerfilePath() default = %q; want %q", got, want)
	}

	cfg.Context = "services/api"
	if got, want := cfg.DockerfilePath(workspace), filepath.Join(workspace, "services", "api", "Dockerfile"); got != want {
		t.Errorf("DockerfilePath() with a context = %q; want %q", got, want)
	}

	cfg.Dockerfile = "docker/Dockerfile.prod"
	if got, want := cfg.DockerfilePath(workspace), filepath.Join(workspace, "docker", "Dockerfile.prod"); got != want {
		t.Errorf("DockerfilePath() explicit = %q; want it relative to the workspace, %q", got, want)
	}
}

func TestBuildCommand(t *testing.T) {
	workspace := filepath.Join(string(filepath.Separator), "ws")
	authDir := filepath.Join(string(filepath.Separator), "tmp", "auth")
	dockerfile := filepath.Join(workspace, "Dockerfile")

	cfg := validConfig()
	cfg.Tags = []string{"test-42", "latest"}
	cfg.Target = "runtime"
	cfg.BuildArgs = map[string]string{"ZED": "last", "ALPHA": "first"}

	tagsAndContext := []string{
		"--file", dockerfile,
		"--target", "runtime",
		"--build-arg", "ALPHA=first",
		"--build-arg", "ZED=last",
		"--tag", "registry.example.com/team/api:test-42",
		"--tag", "registry.example.com/team/api:latest",
		workspace,
	}

	tests := []struct {
		name    string
		cli     CLI
		authDir string
		want    []string
	}{
		{"docker without credentials", Docker, "", append([]string{"build"}, tagsAndContext...)},
		{"docker with credentials", Docker, authDir, append([]string{"--config", authDir, "build"}, tagsAndContext...)},
		{"podman without credentials", Podman, "", append([]string{"build"}, tagsAndContext...)},
		{"podman with credentials", Podman, authDir,
			append([]string{"build", "--authfile", filepath.Join(authDir, AuthFileName)}, tagsAndContext...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildCommand(tt.cli, cfg, workspace, tt.authDir); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BuildCommand() =\n  %v\nwant\n  %v", got, tt.want)
			}
		})
	}
}

func TestBuildCommand_Minimal(t *testing.T) {
	workspace := filepath.Join(string(filepath.Separator), "ws")
	want := []string{
		"build",
		"--file", filepath.Join(workspace, "Dockerfile"),
		"--tag", "registry.example.com/team/api:test-42",
		workspace,
	}
	if got := BuildCommand(Docker, validConfig(), workspace, ""); !reflect.DeepEqual(got, want) {
		t.Errorf("BuildCommand() = %v; want %v", got, want)
	}
}

func TestPushCommand(t *testing.T) {
	const ref = "registry.example.com/team/api:test-42"
	authDir := filepath.Join(string(filepath.Separator), "tmp", "auth")
	digestFile := filepath.Join(string(filepath.Separator), "tmp", "digest")

	tests := []struct {
		name       string
		cli        CLI
		authDir    string
		digestFile string
		want       []string
	}{
		{"docker", Docker, authDir, digestFile, []string{"--config", authDir, "push", ref}},
		{"docker without credentials", Docker, "", "", []string{"push", ref}},
		{"podman", Podman, authDir, digestFile,
			[]string{"push", "--authfile", filepath.Join(authDir, AuthFileName), "--digestfile", digestFile, ref}},
		{"podman without a digest file", Podman, authDir, "",
			[]string{"push", "--authfile", filepath.Join(authDir, AuthFileName), ref}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PushCommand(tt.cli, ref, tt.authDir, tt.digestFile); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PushCommand() = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestAuthFile(t *testing.T) {
	data, err := AuthFile("registry.example.com", "robot", "s3cr3t:with:colons")
	if err != nil {
		t.Fatalf("AuthFile(): %v", err)
	}
	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("AuthFile() is not valid JSON: %v", err)
	}
	entry, ok := parsed.Auths["registry.example.com"]
	if !ok || len(parsed.Auths) != 1 {
		t.Fatalf("AuthFile() auths = %v; want exactly the one registry", parsed.Auths)
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil || string(decoded) != "robot:s3cr3t:with:colons" {
		t.Errorf("AuthFile() auth decodes to %q (%v); want robot:s3cr3t:with:colons", decoded, err)
	}
}

func TestParsePushDigest(t *testing.T) {
	const (
		layer    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		manifest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	)
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"docker push output", "The push refers to repository [registry.example.com/team/api]\n5f70bf18a086: Pushed\ntest-42: digest: " + manifest + " size: 1234\n", manifest},
		{"layer digests before the manifest", layer + ": Pushed\ntest-42: digest: " + manifest + " size: 1234\n", manifest},
		{"podman digest file", manifest + "\n", manifest},
		{"no digest", "denied: requested access to the resource is denied\n", ""},
		{"truncated digest", "digest: sha256:abc123\n", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParsePushDigest(tt.output); got != tt.want {
				t.Errorf("ParsePushDigest() = %q; want %q", got, tt.want)
			}
		})
	}
}
