package scheduler

import (
	"encoding/json"
	"strings"
)

// Change-detection bases, read from webhook payloads. A base is what a run's
// changed files are computed against: for a push, the commit the branch
// pointed at before; for a pull or merge request, the branch it targets.
//
// The payload has already been authenticated and parsed once by the handler.
// A payload that cannot be read here simply yields no base, and a run with
// no base runs every step.

// githubChangeBase returns the base of a GitHub push or pull_request event.
func githubChangeBase(event string, body []byte) (baseSHA, baseBranch string) {
	switch event {
	case "push":
		var payload struct {
			Before string `json:"before"`
		}
		if json.Unmarshal(body, &payload) == nil {
			baseSHA = payload.Before
		}
	case "pull_request":
		var payload githubPRPayload
		if json.Unmarshal(body, &payload) == nil {
			baseBranch = payload.PullRequest.Base.Ref
		}
	}
	return baseSHA, baseBranch
}

// gitlabChangeBase returns the base of a GitLab push or merge_request event.
func gitlabChangeBase(event string, body []byte) (baseSHA, baseBranch string) {
	switch event {
	case "push":
		var payload struct {
			Before string `json:"before"`
		}
		if json.Unmarshal(body, &payload) == nil {
			baseSHA = payload.Before
		}
	case "merge_request":
		var payload struct {
			ObjectAttributes struct {
				TargetBranch string `json:"target_branch"`
			} `json:"object_attributes"`
		}
		if json.Unmarshal(body, &payload) == nil {
			baseBranch = strings.TrimPrefix(payload.ObjectAttributes.TargetBranch, "refs/heads/")
		}
	}
	return baseSHA, baseBranch
}
