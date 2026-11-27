package imageref

import (
	"strings"
	"testing"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestValidate(t *testing.T) {
	valid := []string{
		"alpine",
		"alpine:3.20",
		"golang:1.26-alpine",
		"library/alpine",
		"docker.io/library/alpine:3.20",
		"ghcr.io/my-org/my_app:v1.2.3",
		"registry.example.com/team/sub/api:test-2026-11.42",
		"localhost:5000/forge-examples/hello:test-local",
		"localhost:5000/hello",
		"registry.example.com:8443/team/api@" + testDigest,
		"alpine@" + testDigest,
		"alpine:3.20@" + testDigest,
		"MyOrg/App:1", // accepted here; the engine has the final say
		"a__b/c",
	}
	for _, ref := range valid {
		if err := Validate(ref); err != nil {
			t.Errorf("Validate(%q) = %v; want valid", ref, err)
		}
	}

	invalid := []string{
		"",
		"-v",
		"--privileged",
		"-alpine:3",
		"/alpine",
		"alpine/",
		"alpine:",
		"alpine:-tag",
		"alpine:tag:extra",
		"alpine 3.20",
		"alpine;rm",
		"alpine$(id)",
		"alpine`id`",
		"alpine:3.20\n--privileged",
		"registry.example.com:port/api",
		"alpine@sha256:short",
		"alpine@md5:0123",
		"alpine:${{ env.TAG }}",
		"_docker_build_",
		"alpine:" + strings.Repeat("a", 129),
		strings.Repeat("a/", 300) + "x",
	}
	for _, ref := range invalid {
		if err := Validate(ref); err == nil {
			t.Errorf("Validate(%q) accepted an invalid reference", ref)
		}
	}
}

func TestValidateTemplate(t *testing.T) {
	valid := []string{
		"alpine:3.20",
		"registry.example.com/team/api:test-${{ env.FORGE_BUILD_NUMBER }}",
		"localhost:5000/team/api:${{env.TAG}}",
		"${{ env.REGISTRY }}/team/api:1.0",
		"${{ env.IMAGE }}",
		"${{ env.REGISTRY }}/${{ env.REPO }}:${{ env.TAG }}",
	}
	for _, ref := range valid {
		if err := ValidateTemplate(ref); err != nil {
			t.Errorf("ValidateTemplate(%q) = %v; want valid", ref, err)
		}
	}

	invalid := []string{
		"",
		"--privileged",
		"-${{ env.X }}",
		"alpine:${{ matrix.tag }}",
		"alpine:${{ secrets.TAG }}",
		"alpine:${{ env.TAG }} --privileged",
		"alpine:${{ env.TAG",
		"alpine:$TAG",
	}
	for _, ref := range invalid {
		if err := ValidateTemplate(ref); err == nil {
			t.Errorf("ValidateTemplate(%q) accepted an invalid reference", ref)
		}
	}
}

func TestResolve(t *testing.T) {
	env := map[string]string{
		"FORGE_BUILD_NUMBER": "2026-11.42",
		"REGISTRY":           "localhost:5000",
		"EVIL":               "x --privileged",
		"OPTION":             "--privileged",
		"EMPTY":              "",
	}

	t.Run("expands and validates", func(t *testing.T) {
		got, err := Resolve("${{ env.REGISTRY }}/team/api:test-${{ env.FORGE_BUILD_NUMBER }}", env)
		if err != nil {
			t.Fatalf("Resolve(): %v", err)
		}
		if want := "localhost:5000/team/api:test-2026-11.42"; got != want {
			t.Errorf("Resolve() = %q; want %q", got, want)
		}
	})

	t.Run("a reference without placeholders is returned as is", func(t *testing.T) {
		for _, ref := range []string{"alpine:3.20", "_docker_build_", ""} {
			got, err := Resolve(ref, env)
			if err != nil || got != ref {
				t.Errorf("Resolve(%q) = %q, %v; want it unchanged", ref, got, err)
			}
		}
	})

	t.Run("a missing variable is named", func(t *testing.T) {
		_, err := Resolve("alpine:${{ env.NOT_SET }}", env)
		if err == nil || !strings.Contains(err.Error(), "NOT_SET") {
			t.Errorf("Resolve() = %v; want the missing variable named", err)
		}
	})

	t.Run("a value cannot turn the reference into something else", func(t *testing.T) {
		for _, ref := range []string{
			"alpine:${{ env.EVIL }}",
			"${{ env.OPTION }}",
			"alpine:${{ env.EMPTY }}",
			"${{ env.EMPTY }}",
		} {
			if got, err := Resolve(ref, env); err == nil {
				t.Errorf("Resolve(%q) = %q; want it refused", ref, got)
			}
		}
	})
}

func TestPinned(t *testing.T) {
	cases := map[string]bool{
		"alpine":                             false,
		"alpine:latest":                      false,
		"alpine:3.20":                        true,
		"localhost:5000/hello":               false,
		"localhost:5000/hello:latest":        false,
		"localhost:5000/hello:1.0":           true,
		"alpine@" + testDigest:               true,
		"team/api:test-${{ env.BUILD }}":     true,
		"localhost:5000/api:${{ env.TAG }}":  true,
		"${{ env.REGISTRY }}/team/api":       false,
		"registry.example.com/team/api:v1":   true,
		"registry.example.com:8443/team/api": false,
	}
	for ref, want := range cases {
		if got := Pinned(ref); got != want {
			t.Errorf("Pinned(%q) = %v; want %v", ref, got, want)
		}
	}
}
