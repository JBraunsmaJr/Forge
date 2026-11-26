package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustParseCreate(t *testing.T, body string) *createRequest {
	t.Helper()
	var req createRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("test body is not valid JSON: %v", err)
	}
	return &req
}

func TestValidateCreate(t *testing.T) {
	const ownSocket = "/run/forge-sockets/agent-1.sock"
	allowed := []string{ownSocket, "/srv/cache"}

	tests := []struct {
		name    string
		body    string
		wantErr string // substring of the expected error; "" means allowed
	}{
		{"plain container", `{"Image":"alpine"}`, ""},
		{"explicit defaults", `{"HostConfig":{"Privileged":false,"NetworkMode":"default","CapAdd":null,"Binds":null}}`, ""},
		{"named volume", `{"HostConfig":{"Binds":["cache:/root/.cache"]}}`, ""},
		{"named network", `{"HostConfig":{"NetworkMode":"forge_forge"}}`, ""},
		{"another container's PID namespace", `{"HostConfig":{"PidMode":"container:abc"}}`, ""},
		{"own proxy socket", `{"HostConfig":{"Binds":["` + ownSocket + `:/var/run/docker.sock"]}}`, ""},
		{"allowed directory", `{"HostConfig":{"Binds":["/srv/cache/pip:/root/.cache/pip:ro"]}}`, ""},
		{"allowed directory as a mount", `{"HostConfig":{"Mounts":[{"Type":"bind","Source":"/srv/cache","Target":"/c"}]}}`, ""},
		{"volume mount", `{"HostConfig":{"Mounts":[{"Type":"volume","Source":"data","Target":"/d"}]}}`, ""},
		{"harmless security option", `{"HostConfig":{"SecurityOpt":["no-new-privileges"]}}`, ""},

		{"privileged", `{"HostConfig":{"Privileged":true}}`, "privileged"},
		{"host network", `{"HostConfig":{"NetworkMode":"host"}}`, "network namespace"},
		{"host PID", `{"HostConfig":{"PidMode":"host"}}`, "PID namespace"},
		{"host IPC", `{"HostConfig":{"IpcMode":"host"}}`, "IPC namespace"},
		{"host UTS", `{"HostConfig":{"UTSMode":"host"}}`, "UTS namespace"},
		{"host user namespace", `{"HostConfig":{"UsernsMode":"host"}}`, "user namespace"},
		{"host cgroup namespace", `{"HostConfig":{"CgroupnsMode":"host"}}`, "cgroup namespace"},
		{"host namespace, odd case", `{"HostConfig":{"PidMode":" Host "}}`, "PID namespace"},
		{"added capability", `{"HostConfig":{"CapAdd":["SYS_ADMIN"]}}`, "capabilities"},
		{"host device", `{"HostConfig":{"Devices":[{"PathOnHost":"/dev/sda"}]}}`, "devices"},
		{"seccomp unconfined", `{"HostConfig":{"SecurityOpt":["seccomp=unconfined"]}}`, "security option"},
		{"apparmor unconfined, colon form", `{"HostConfig":{"SecurityOpt":["apparmor:unconfined"]}}`, "security option"},
		{"host root", `{"HostConfig":{"Binds":["/:/host"]}}`, "bind-mounting"},
		{"real docker socket", `{"HostConfig":{"Binds":["/var/run/docker.sock:/var/run/docker.sock"]}}`, "bind-mounting"},
		{"another agent's socket", `{"HostConfig":{"Binds":["/run/forge-sockets/agent-2.sock:/var/run/docker.sock"]}}`, "bind-mounting"},
		{"sibling of an allowed directory", `{"HostConfig":{"Binds":["/srv/cache-other:/c"]}}`, "bind-mounting"},
		{"climbing out of an allowed directory", `{"HostConfig":{"Binds":["/srv/cache/../../etc:/c"]}}`, "bind-mounting"},
		{"bind mount outside the allowed paths", `{"HostConfig":{"Mounts":[{"Type":"bind","Source":"/etc","Target":"/e"}]}}`, "bind-mounting"},
		{"named pipe mount", `{"HostConfig":{"Mounts":[{"Type":"npipe","Source":"\\\\.\\pipe\\docker_engine"}]}}`, "named pipe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreate(mustParseCreate(t, tt.body), allowed)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("validateCreate() = %v; want allowed", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("validateCreate() allowed the request; want an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("validateCreate() = %q; want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCreate_NoAllowedBinds(t *testing.T) {
	req := mustParseCreate(t, `{"HostConfig":{"Binds":["/srv/cache:/c"]}}`)
	if err := validateCreate(req, nil); err == nil {
		t.Error("a host bind was allowed with an empty allow list")
	}
}

func TestBindAllowed(t *testing.T) {
	tests := []struct {
		source  string
		allowed []string
		want    bool
	}{
		{"/srv/cache", []string{"/srv/cache"}, true},
		{"/srv/cache/a/b", []string{"/srv/cache"}, true},
		{"/srv/cache/", []string{"/srv/cache"}, true},
		{"/srv/cachex", []string{"/srv/cache"}, false},
		{"/srv", []string{"/srv/cache"}, false},
		{"/srv/cache/../secret", []string{"/srv/cache"}, false},
		{"relative/path", []string{"/srv/cache"}, false},
		{"/anything", []string{"/"}, true},
		{"/anything", []string{""}, false},
		{"/anything", nil, false},
	}
	for _, tt := range tests {
		if got := bindAllowed(tt.source, tt.allowed); got != tt.want {
			t.Errorf("bindAllowed(%q, %v) = %v; want %v", tt.source, tt.allowed, got, tt.want)
		}
	}
}

func TestAllowedBindsFromEnv(t *testing.T) {
	t.Setenv(EnvAllowedBinds, " /srv/cache/ , relative, ,/var/lib/forge/../forge/data")
	got := allowedBindsFromEnv()
	want := []string{"/srv/cache", "/var/lib/forge/data"}
	if len(got) != len(want) {
		t.Fatalf("allowedBindsFromEnv() = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allowedBindsFromEnv()[%d] = %q; want %q", i, got[i], want[i])
		}
	}

	t.Setenv(EnvAllowedBinds, "")
	if got := allowedBindsFromEnv(); len(got) != 0 {
		t.Errorf("allowedBindsFromEnv() with nothing set = %v; want none", got)
	}
}
