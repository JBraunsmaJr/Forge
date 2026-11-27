package placeholder

import (
	"reflect"
	"testing"
)

func TestHasEnv(t *testing.T) {
	cases := map[string]bool{
		"plain":                    false,
		"${{ env.A }}":             true,
		"${{env.A}}":               true,
		"x-${{  env.A_1  }}-y":     true,
		"${{ matrix.A }}":          false,
		"${{ env.1A }}":            false,
		"${ env.A }":               false,
		"${{ env.A }":              false,
		"${{ secrets.A }}":         false,
		"prefix ${{ env.A.B }} sf": false,
	}
	for value, want := range cases {
		if got := HasEnv(value); got != want {
			t.Errorf("HasEnv(%q) = %v; want %v", value, got, want)
		}
	}
}

func TestEnvNames(t *testing.T) {
	got := EnvNames("${{ env.B }}/${{ env.A }}:${{ env.B }}")
	if want := []string{"B", "A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnvNames() = %v; want %v", got, want)
	}
	if got := EnvNames("plain"); got != nil {
		t.Errorf("EnvNames(plain) = %v; want nil", got)
	}
}

func TestExpandEnv(t *testing.T) {
	env := map[string]string{"A": "one", "EMPTY": ""}
	cases := map[string]string{
		"plain":                           "plain",
		"${{ env.A }}":                    "one",
		"${{env.A}}-${{ env.A }}":         "one-one",
		"${{ env.MISSING }}":              "${{ env.MISSING }}",
		"${{ env.A }}/${{ env.MISSING }}": "one/${{ env.MISSING }}",
		"[${{ env.EMPTY }}]":              "[]",
	}
	for value, want := range cases {
		if got := ExpandEnv(value, env); got != want {
			t.Errorf("ExpandEnv(%q) = %q; want %q", value, got, want)
		}
	}
	// A value is never expanded twice.
	if got := ExpandEnv("${{ env.A }}", map[string]string{"A": "${{ env.B }}", "B": "x"}); got != "${{ env.B }}" {
		t.Errorf("ExpandEnv expanded a substituted value again: %q", got)
	}
}

func TestReplaceEnv(t *testing.T) {
	if got := ReplaceEnv("a-${{ env.X }}-${{ env.Y }}", "$1"); got != "a-$1-$1" {
		t.Errorf("ReplaceEnv() = %q; want the replacement used literally", got)
	}
}
