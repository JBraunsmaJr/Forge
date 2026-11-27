package scheduler

import (
	"errors"
	"fmt"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/changes"
	"github.com/JBraunsmaJr/forge/internal/gitcache"
)

// skipUnchangedSteps marks the steps whose `changes:` patterns match none of
// the files this run's commits changed as skipped. When the changed files
// cannot be determined, every step runs.
func (s *Server) skipUnchangedSteps(steps []api.StepDef, repoURL string, meta api.WebhookRunMeta) []api.StepDef {
	usesChanges := false
	for _, step := range steps {
		if len(step.Changes) > 0 {
			usesChanges = true
			break
		}
	}
	if !usesChanges {
		return steps
	}

	set := s.changedFiles(repoURL, meta)
	if !set.Known() {
		return steps
	}

	steps, skipped := changes.Apply(steps, set)
	fmt.Printf("[webhook] %s: %d file(s) changed, %d step(s) skipped by changes: %v\n",
		meta.RepoName, set.Len(), len(skipped), skipped)
	return steps
}

// changedFiles computes the files changed by the commits a webhook run is
// for, from the scheduler's mirror of the repository.
func (s *Server) changedFiles(repoURL string, meta api.WebhookRunMeta) changes.Set {
	var (
		files []string
		err   error
	)
	switch {
	case meta.BaseBranch != "":
		files, err = s.gitCache.ChangedFromBranch(repoURL, meta.BaseBranch, meta.CommitSHA)
	case meta.BaseSHA != "":
		files, err = s.gitCache.ChangedSince(repoURL, meta.BaseSHA, meta.CommitSHA)
	default:
		return changes.Unknown()
	}

	if err != nil {
		if !errors.Is(err, gitcache.ErrNoBase) {
			fmt.Printf("[webhook] %s: cannot determine changed files, running every step: %v\n", meta.RepoName, err)
		}
		return changes.Unknown()
	}
	return changes.Of(files)
}
