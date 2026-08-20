# Step Registry

A **step** is a reusable, versioned, parameterised pipeline fragment. It sits
between raw YAML you copy between repos and a full reusable pipeline: it takes
declared inputs, expands into one or more ordinary Forge steps, and is pinned by
content digest so it cannot change under you.

Steps are referenced with `uses:`:

```yaml
steps:
  - id: scan-source
    uses: forge-community/trivy@v1.0.0
    with:
      severity: "HIGH,CRITICAL"
```

> `uses:` also accepts a **local file path** (`./templates/docker-build.yml`).
> That form is documented under
> [Step Templates](Pipeline-Reference.md#step-templates-uses) and needs no
> registry. This page covers the registry form.

---

## Reference syntax

```
uses: <registry>/<step>@<version>
```

| Part       | Meaning                                                                 |
|------------|-------------------------------------------------------------------------|
| `registry` | `forge-community` (or its alias `forge-steps`) for the public registry; `internal` for your org's private registry. |
| `step`     | The step name, matching a key under `steps:` in the registry's `registry.yml`. |
| `version`  | An exact version (`v1.0.0`), a major alias (`v1` → highest published `v1.x`), or `latest`. |

The version is **required**. `uses: forge-community/trivy` is rejected — an
unpinned step is a supply-chain risk, so there is no implicit `latest`.

### Choosing a version

| Form      | Resolves to                          | Use when |
|-----------|--------------------------------------|----------|
| `v1.0.0`  | Exactly that version                 | Production pipelines. Reproducible. |
| `v1`      | Highest published `v1.x`             | You want patches automatically but not breaking changes. |
| `latest`  | Whatever `latest:` says in the index | Experimenting only. |

---

## How resolution works

Resolution happens in the **compiler**, before a run is submitted — not on an
agent at runtime. So `forge validate` catches a bad version, a missing required
input, or an out-of-range enum value locally.

```
uses: forge-community/trivy@v1.0.0
        │
        ├─► GET <registry>/registry.yml
        │     └─► steps.trivy.versions["v1.0.0"] → sha256:fefad7…
        │
        ├─► GET <registry-at-tag-v1.0.0>/steps/trivy/step.yml
        │
        ├─► Verify SHA-256 of the fetched step.yml == the pinned digest
        │     └─► mismatch → compilation fails
        │
        ├─► Validate `with:` against the step's declared inputs
        │     (required, enum, defaults applied)
        │
        └─► Expand into ordinary steps
```

The digest check is the security property. `registry.yml` records the SHA-256 of
each version's `step.yml` at publish time, so a moved tag or an edited file is
rejected at compile time rather than silently injected into your pipeline.

!!! note "Steps are fetched at their git tag"
    `registry.yml` is read from the registry's default branch, but `step.yml` is
    read at the **tag matching the version**. A version listed in `registry.yml`
    without a corresponding git tag in the registry repo will fail to resolve
    with an HTTP 404. Registry maintainers must push a tag per published
    version.

---

## Inputs

Inputs are declared in the step's `step.yml` and supplied by the caller under
`with:`.

```yaml
- id: compile
  uses: forge-community/go-build@v1.0.0
  with:
    package: ./cmd/forge
    output: forge
```

* **Required inputs** must be supplied. Omitting one fails compilation with
  `step forge-community/docker-build requires input "tag"`.
* **Defaults** are applied for anything you leave out.
* **Enums** are checked against the declared values.
* **Values are strings.** Quote numbers and booleans (`exit_code: "0"`,
  `ignore_unfixed: "true"`) so intent stays obvious.
* `${{ env.VAR }}` interpolation works inside `with:` values.

---

## Expanded step IDs and `depends_on`

A step expands into one or more real steps, each **namespaced under the call
site's id**. `id: compile` using a step whose internal step is `build` produces
`compile.build`.

You do not need to know those internal names. Depend on the **call-site id** and
Forge remaps it to the expanded step(s):

```yaml
- id: compile
  uses: forge-community/go-build@v1.0.0

- id: package
  image: alpine:3.20
  depends_on: [compile]     # → resolves to compile.build
```

The call site's own `depends_on` is inherited by the fragment's entry steps, so
the whole fragment starts only once your dependencies are satisfied. Ordering
*inside* the fragment is preserved as its author wrote it.

---

## Policy and the `requires:` block

Every step declares what it needs:

```yaml
requires:
  docker_socket: true
  internet_access: false
  privileged: false
```

This feeds the policy system. If your org forbids the docker socket, any step
declaring `docker_socket: true` is rejected at submission time rather than
failing part-way through a run. See [Policies](Policies.md).

---

## Configuring the registry

Forge resolves public steps from
[`JBraunsmaJr/forge-community`](https://github.com/JBraunsmaJr/forge-community)
by default. Override it with `FORGE_STEP_REGISTRY_URL`:

```bash
# Internal mirror — avoids GitHub rate limits and keeps CI working when
# github.com is unreachable
FORGE_STEP_REGISTRY_URL=https://mirror.internal.example.com/forge-community
```

The URL is the **base of the registry checkout**: Forge appends `/registry.yml`
and `/steps/<name>/step.yml`.

For a `raw.githubusercontent.com` base, Forge swaps the trailing branch segment
for the version tag when fetching `step.yml`. Any other base (an internal mirror,
an artifact store) is used **as-is** — so a mirror should serve the content of
the versions it advertises, and advertise only what it actually serves. Digest
verification is unchanged either way, so a mirror cannot serve modified steps
without failing the check.

---

## Available steps

Current contents of the community registry:

| Step           | Purpose                                              | Required inputs |
|----------------|------------------------------------------------------|-----------------|
| `trivy`        | Scan a container image or the workspace filesystem   | —               |
| `go-build`     | Compile a Go module, upload the binary as an artifact| —               |
| `node-test`    | Install locked dependencies and run the test script  | —               |
| `docker-build` | Build a Docker image (does not push)                 | `tag`           |

`forge-community/trivy` scans the **workspace filesystem** when `image:` is
omitted, which needs neither a docker socket nor registry credentials — the
cheapest way to get dependency scanning into an existing pipeline.

A complete example using all four lives in
[`examples/.forge/community-steps.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/examples/.forge/community-steps.yml).

---

## Forge's own pipeline

Forge dogfoods the registry. [`.forge/pipeline.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/.forge/pipeline.yml)
uses `forge-community/trivy@v1.0.0` to scan its own source tree:

```yaml
- id: scan-source
  name: Scan Source (community step)
  uses: forge-community/trivy@v1.0.0
  with:
    severity: "HIGH,CRITICAL"
    ignore_unfixed: "true"
    exit_code: "0"
```

`exit_code: "0"` keeps it report-only, so a newly-disclosed CVE in a dependency
cannot break the build before anyone has triaged it. Change it to `"1"` to make
the scan gate the pipeline.

Note that `build-image` deliberately still uses the local
`.forge/templates/docker-build.yml`: it logs in and **pushes**, which the
community `docker-build` step does not do, and the promotion step downstream
needs that pushed image.

---

## Contributing a step

Steps live in the community registry repo, one directory per step:

```
steps/<name>/
├── step.yml        # definition: inputs, outputs, steps, requires
├── README.md       # documentation
└── tests/
    └── basic.yml   # a pipeline exercising the step
```

Two conventions worth knowing, because both are silent when you get them wrong:

* **Give internal steps literal ids** (`build`, `scan`) rather than
  `${{ step_id }}`. Forge already namespaces them under the caller's id, so
  `${{ step_id }}` produces a doubled id like `compile.compile`.
* **Give every optional input a default**, `default: ""` included. An input with
  no default and no supplied value leaves `${{ inputs.name }}` unsubstituted in
  the rendered script.

Publishing requires updating `registry.yml` with the new version and the
SHA-256 of its `step.yml`, then tagging the repo at that version. See
[CONTRIBUTING.md](https://github.com/JBraunsmaJr/forge-community/blob/main/CONTRIBUTING.md).
