package stepretry

import (
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	for _, stepType := range []string{"", "task", "generator", "docker_build"} {
		for _, retries := range []int{0, 1, MaxRetries} {
			if err := Validate(retries, stepType); err != nil {
				t.Errorf("Validate(%d, %q) = %v; want valid", retries, stepType, err)
			}
		}
	}
	for _, retries := range []int{-1, MaxRetries + 1, 100} {
		if err := Validate(retries, "task"); err == nil || !strings.Contains(err.Error(), "between 0 and") {
			t.Errorf("Validate(%d) = %v; want a range error", retries, err)
		}
	}
	for _, stepType := range []string{"pipeline", "approval", "release", "docker_publish"} {
		if err := Validate(1, stepType); err == nil || !strings.Contains(err.Error(), stepType) {
			t.Errorf("Validate(1, %q) = %v; want the step type refused", stepType, err)
		}
		if err := Validate(0, stepType); err != nil {
			t.Errorf("Validate(0, %q) = %v; no retry is always valid", stepType, err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	own := map[string]string{"unrelated": "kept"}
	with := AddTo(own, 3)
	if len(own) != 1 {
		t.Error("AddTo modified the map it was given")
	}
	if with["unrelated"] != "kept" || FromWith(with) != 3 {
		t.Errorf("round trip = %v; want the parameter kept and 3 retries", with)
	}
	if got := AddTo(own, 0); len(got) != 1 {
		t.Errorf("AddTo(0) = %v; want the map unchanged", got)
	}
	if FromWith(AddTo(nil, 2)) != 2 {
		t.Error("AddTo(nil, 2) did not round-trip")
	}
}

func TestFromWith_IsSafeAgainstBadValues(t *testing.T) {
	cases := map[string]int{"": 0, "abc": 0, "-3": 0, "2": 2, "5": 5, "6": MaxRetries, "1000000": MaxRetries, "1.5": 0}
	for value, want := range cases {
		if got := FromWith(map[string]string{withKey: value}); got != want {
			t.Errorf("FromWith(%q) = %d; want %d", value, got, want)
		}
	}
	if FromWith(nil) != 0 {
		t.Error("FromWith(nil) != 0")
	}
}

func TestDelay(t *testing.T) {
	previous := time.Duration(0)
	for retry := 1; retry <= MaxRetries; retry++ {
		delay := Delay(retry)
		if delay < previous {
			t.Errorf("Delay(%d) = %v is shorter than Delay(%d) = %v", retry, delay, retry-1, previous)
		}
		if delay <= 0 || delay > time.Minute {
			t.Errorf("Delay(%d) = %v; want a positive wait of at most a minute", retry, delay)
		}
		previous = delay
	}
}
