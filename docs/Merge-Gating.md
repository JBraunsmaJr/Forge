# Merge Gating

Forge reports the result of every webhook-triggered run back to GitLab or GitHub as a commit status named `forge/ci`. Requiring that status is what stops a merge request from merging before its pipeline has passed.

## What Forge reports

| When                                   | Status                         |
|----------------------------------------|--------------------------------|
| A push or merge request starts a run   | `pending`                      |
| Every step passed                      | `success`                      |
| A step failed                          | `failure` (`failed` in GitLab) |
| The run was canceled                   | `error` (`canceled` in GitLab) |

Each status links to the run in Forge. For a merge request, the status is filed under the request's source branch, which is where GitLab looks for it.

The final status matters most: if it never arrives, the merge request stays blocked. Forge therefore retries it for about fifteen minutes when the Git host is unreachable, restarting, or answers with a temporary error. A rejected request (bad token, missing permission, unknown project) is not retried; it is written to the scheduler log as `[scm] giving up reporting ...`.

## Requirements

- The project in Forge has an **SCM token**. Without one, runs still happen but nothing is reported.
- The token may write commit statuses:
    - **GitLab:** a project or group access token with the `api` scope and at least the Developer role.
    - **GitHub:** a fine-grained token with *Commit statuses: Read and write*, or a classic token with `repo:status`.
- The project's webhook is configured, so merge request and push events reach Forge.
- For a self-managed instance with an internal certificate authority, the scheduler trusts it. See [Internal certificate authorities](Configuration.md#internal-certificate-authorities).

## GitLab

1. In the GitLab project, open **Settings > Merge requests**.
2. Under **Merge checks**, turn on **Pipelines must succeed**.
3. Leave **Skipped pipelines are considered successful** off, unless you want merge requests that Forge did not run to be mergeable.

GitLab shows Forge's status as an *external* job in the merge request's pipeline widget. If the project also runs GitLab CI pipelines for the same commits, GitLab decides which pipeline the external job is attached to, and the result can be surprising; while moving a project from GitLab CI to Forge, disable the GitLab CI pipeline for merge requests or expect both to be required.

## GitHub

1. In the repository, open **Settings > Branches** (or **Rules > Rulesets**) and edit the rule for the protected branch.
2. Turn on **Require status checks to pass before merging**.
3. Add `forge/ci` to the required checks. GitHub only offers a check after it has been reported once, so run the pipeline once first.

## When no status is reported

A required status that never appears blocks the merge. Forge reports nothing in these cases:

- **Merge requests from forks**, which do not run unless `FORGE_ALLOW_FORK_RUNS` is set.
- **Branches excluded by the project's branch filter.**
- **Runs that are not for a webhook event:** manually triggered runs, runs submitted with the CLI, and child runs started by a pipeline step.

A **rerun** reports its final status when it finishes, but does not set the commit back to `pending` while it runs; the previous result stays visible until then.

## Limits

- A scheduler restart loses a final status that was still being retried. The status can be restored by rerunning the pipeline.
- The status reflects the commits of the merge request as pushed, not the result of merging them into the target branch. Two merge requests that each pass can still break the target branch together; preventing that needs a merge queue, which Forge does not have yet.
