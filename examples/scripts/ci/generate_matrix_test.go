package main

import (
	"testing"
)

func TestGenerateMatrix(t *testing.T) {
	platforms := []Platform{
		{"linux", "amd64", "golang:1.26-alpine"},
		{"darwin", "arm64", "golang:1.26-alpine"},
	}

	steps := GenerateMatrix(platforms)

	if len(steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(steps))
	}

	if steps[0].ID != "build-linux-amd64" {
		t.Errorf("expected first step ID build-linux-amd64, got %s", steps[0].ID)
	}

	if steps[1].Env["GOARCH"] != "arm64" {
		t.Errorf("expected second step GOARCH arm64, got %s", steps[1].Env["GOARCH"])
	}
}
