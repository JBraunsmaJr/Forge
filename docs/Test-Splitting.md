# Test Splitting

A step with a `split:` block is fanned out into N shards, each running a subset
of the suite. Assignment is driven by recorded per-test durations, so shards
finish at roughly the same time instead of being split evenly by count.

```yaml
- id: integration-tests
  split:
    strategy: duration
    shards: 3
    history_days: 14
    min_history_runs: 2
    fallback: single
  test_report: .forge/test-report.json
  script: .forge/scripts/integration-tests.sh
```

## The warm-up

Duration-based assignment needs history, so a fresh pipeline takes a few runs to
reach steady state. This is expected, not a fault:

| Run | State | Behaviour |
|-----|-------|-----------|
| 1 | No rows | Cold start. With `fallback: single`, shard 1 runs the whole suite; the rest get `FORGE_TEST_SHARD_EMPTY=1` and no-op. `fallback: round-robin` runs everything on every shard instead. |
| 2 | Rows exist, `runs < min_history_runs` | Round-robin over known entries. |
| 3+ | `runs >= min_history_runs` | Greedy bin-packing by average duration. |

If you are past run 3 and still seeing shard 1 do all the work, no durations are
being recorded — see [Diagnosing](#diagnosing).

## Duration history and key kinds

Durations live in `test_file_durations`, keyed by
`(project_id, pipeline_name, step_id, file_path)`.

`file_path` is **not** always a path. It depends on the framework, because
`go test -json` cannot report which file a test is defined in:

| Framework | `key_kind` | `file_path` holds | Selected with |
|-----------|-----------|-------------------|---------------|
| `go-test` | `go-test-name` | Top-level test function name (`TestAuth_Login`) | `go test -run` |
| `pytest`, `jest`, `rspec` | `file-path` | Repo-relative test file path | Path argument |

The `key_kind` column records which scheme a row uses. Shard planning only reads
rows matching the **most recent** kind for that step.

This matters more than it looks. Selectors flow back into the runner, so history
keyed one way and consumed as though it were the other selects nothing. And
because `go test -run` **exits 0 when its regex matches nothing**, the shard
passes, runs no tests, and reports no durations — which leaves the bad history in
place. The suite stops running while every build stays green.

Scoping to the newest `key_kind` turns a scheme change into "no usable history"
(a cold start that self-heals in two runs) rather than a silent, permanent
outage. Rows written before the column existed have `key_kind = ''` and are
ignored outright: their scheme is unknown, and guessing is the failure being
prevented.

!!! warning "Changing how a report keys its entries"
    If you change what a converter emits in `TestFileResult.Path`, give it a new
    `KeyKind`. Do not reuse an existing one for a different scheme — that is
    indistinguishable from valid history and reintroduces the trap above.

## Shard environment

| Variable | Meaning |
|----------|---------|
| `FORGE_TEST_FILES` | This shard's assigned entries, comma-separated. |
| `FORGE_TEST_FILES_ALL` | Union of every shard's assignment. |
| `FORGE_SHARD_INDEX` | 0-based shard number. |
| `FORGE_SHARD_TOTAL` | Total shards. |
| `FORGE_SHARD_ESTIMATED_MS` | Predicted runtime for this shard. |
| `FORGE_TEST_SHARD_EMPTY` | `1` when this shard should no-op (cold start with `fallback: single`). |

An empty `FORGE_TEST_FILES` is **not** a skip signal on its own — it is ambiguous
between "run everything" (shard 1, cold start) and "assigned nothing".
`FORGE_TEST_SHARD_EMPTY` is the only authoritative signal for the latter.

### What a shard script should do

The planner only knows entries that already appear in history, so a test added
since the last run is invisible to it and would never be assigned to anyone. A
correct shard script reconciles the assignment against the suite's real contents:

1. List the suite's actual tests (`go test -list`).
2. **Fail** if that list is empty — the package did not build, and every later
   check would read as "nothing to do" and pass.
3. Subtract `FORGE_TEST_FILES_ALL` from the real list to find unassigned tests,
   and claim a deterministic slice by name hash (`cksum % FORGE_SHARD_TOTAL`), so
   shards agree without coordinating.
4. Drop assigned names that no longer exist (renamed or deleted tests still in
   history).
5. If nothing remains, exit 0 — leftovers are distributed across all shards, so
   the tests are provably covered elsewhere.

`.forge/scripts/integration-tests.sh` is the reference implementation.

## Diagnosing

Start here:

```sql
SELECT project_id, pipeline_name, step_id, key_kind,
       COUNT(DISTINCT run_id) AS runs,
       COUNT(DISTINCT file_path) AS entries,
       MAX(created_at) AS latest
FROM   test_file_durations
GROUP  BY 1,2,3,4
ORDER  BY latest DESC;
```

| Symptom | Cause |
|---------|-------|
| `latest` is old while runs keep passing | Reports are empty. The scheduler logs `WARNING: empty test report`. Usually shards select nothing. |
| Entries don't look like the current `key_kind` | Legacy or mis-keyed rows. They are ignored; the step cold-starts. |
| `project_id` is NULL for some rows | History is partitioned by project. Runs submitted without project context (direct API, CLI) build a separate bucket that project-triggered runs cannot see. |
| `pipeline_name` varies per run | `PipelineName` fell through to the decorated run name. Every run then looks like a new pipeline and history never accumulates. |
| `entries` < your real test count | Tests exist that have never been recorded. Handled by leftover pickup above. |

To reset a step's history deliberately:

```sql
DELETE FROM test_file_durations
WHERE  step_id LIKE 'integration-tests%';
```

The next three runs rebuild it through the warm-up.
