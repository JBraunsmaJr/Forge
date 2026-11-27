package scheduler

import (
	"encoding/json"
	"os"
	"strings"
)

// Fork changes.
//
// A pull or merge request opened from a fork carries a pipeline definition
// written by someone who cannot push to the repository. Running it as usual
// would let that person name the project's secrets in a step and read them.
// Until Forge can run such a change in a restricted mode, it does not run it
// at all unless the installation opts in.

// envAllowForkRuns names the environment variable that lets pull and merge
// requests from forks start runs. Those runs are not restricted in any way:
// they can use the project's secrets. Leave it unset unless every fork of
// every project is trusted.
const envAllowForkRuns = "FORGE_ALLOW_FORK_RUNS"

// forkRunsAllowed reports whether the installation has opted in to running
// changes from forks.
func forkRunsAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envAllowForkRuns))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// githubPRFromFork reports whether a GitHub pull_request webhook body
// describes a pull request whose head is in a different repository from its
// base. When the body cannot be read, or either repository is missing (the
// head repository is null once a fork has been deleted), the answer is true:
// a change whose origin is unknown is treated as untrusted.
func githubPRFromFork(body []byte) bool {
	var payload struct {
		PullRequest struct {
			Head struct {
				Repo *struct {
					ID int64 `json:"id"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Repo *struct {
					ID int64 `json:"id"`
				} `json:"repo"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return true
	}
	head, base := payload.PullRequest.Head.Repo, payload.PullRequest.Base.Repo
	if head == nil || base == nil || head.ID == 0 || base.ID == 0 {
		return true
	}
	return head.ID != base.ID
}

// gitlabMRFromFork reports whether a GitLab merge request webhook body
// describes a merge request whose source project differs from its target
// project. As with githubPRFromFork, an unreadable or incomplete body counts
// as a fork.
func gitlabMRFromFork(body []byte) bool {
	var payload struct {
		ObjectAttributes struct {
			SourceProjectID int64 `json:"source_project_id"`
			TargetProjectID int64 `json:"target_project_id"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return true
	}
	source, target := payload.ObjectAttributes.SourceProjectID, payload.ObjectAttributes.TargetProjectID
	if source == 0 || target == 0 {
		return true
	}
	return source != target
}
