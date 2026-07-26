<script lang="ts">
    import { 
        Plus, Trash2, Code, MoveUp, MoveDown, Settings, 
        Package, Lock, Globe, Layers, ChevronDown, ChevronRight,
        Download, Upload, Cpu, Play
    } from '@lucide/svelte';
    import { flip } from 'svelte/animate';
    import EditorDAG from './EditorDAG.svelte';

    interface Step {
        id: string;
        type: 'command' | 'generator' | 'pipeline' | 'release' | 'approval';
        image: string;
        command: string;
        depends_on: string[];
        artifact_uploads: { name: string, path: string }[];
        artifact_downloads: { name: string, dest: string }[];
        env: { key: string, value: string }[];
        secrets: string[];
        inputs: string[];
        release: { name: string, tag: string, body: string, artifacts: string[] };
        condition: string;
        docker_socket: boolean;
        timeout: string;
        workdir: string;
        always_run: boolean;
        pipeline_ref: { name: string, path: string };
        matrix: { key: string, values: string[] }[];
        with: { key: string, value: string }[];
        split: { enabled: boolean, shards: number, historyDays: number, minHistoryRuns: number, fallback: 'single' | 'round-robin' };
        testReport: string;
        expanded: boolean;
    }

    let pipelineName = "My Pipeline";
    let steps: Step[] = [
        { 
            id: 'lint', type: 'command', image: 'golangci/golangci-lint:v1.64', command: 'golangci-lint run', depends_on: [], 
            artifact_uploads: [], artifact_downloads: [], env: [], secrets: [], 
            inputs: [], release: { name: '', tag: '', body: '', artifacts: [] },
            condition: '', docker_socket: false, 
            timeout: '5m', workdir: '', always_run: false,
            pipeline_ref: { name: '', path: '' }, matrix: [], with: [],
            split: { enabled: false, shards: 3, historyDays: 14, minHistoryRuns: 3, fallback: 'single' },
            testReport: '', expanded: true
        },
        { 
            id: 'test', type: 'command', image: 'golang:1.26', command: 'go test ./...', depends_on: ['lint'], 
            artifact_uploads: [], artifact_downloads: [], env: [], secrets: [], 
            inputs: [], release: { name: '', tag: '', body: '', artifacts: [] },
            condition: '', docker_socket: false, 
            timeout: '10m', workdir: '', always_run: false,
            pipeline_ref: { name: '', path: '' }, matrix: [], with: [],
            split: { enabled: false, shards: 3, historyDays: 14, minHistoryRuns: 3, fallback: 'single' },
            testReport: '', expanded: false
        }
    ];

    function addStep() {
        const id = `step-${steps.length + 1}`;
        steps = [...steps, { 
            id, type: 'command', image: 'alpine:latest', command: 'echo hello', depends_on: [], 
            artifact_uploads: [], artifact_downloads: [], env: [], secrets: [], 
            inputs: [], release: { name: '', tag: '', body: '', artifacts: [] },
            condition: '', docker_socket: false, 
            timeout: '', workdir: '', always_run: false,
            pipeline_ref: { name: '', path: '' }, matrix: [], with: [],
            split: { enabled: false, shards: 3, historyDays: 14, minHistoryRuns: 3, fallback: 'single' },
            testReport: '', expanded: true
        }];
    }

    function removeStep(index: number) {
        const id = steps[index].id;
        steps = steps.filter((_, i) => i !== index);
        // Remove from dependencies
        steps = steps.map(s => ({
            ...s,
            depends_on: s.depends_on.filter(d => d !== id)
        }));
    }

    function moveStep(index: number, direction: number) {
        if (index + direction < 0 || index + direction >= steps.length) return;
        const newSteps = [...steps];
        const temp = newSteps[index];
        newSteps[index] = newSteps[index + direction];
        newSteps[index + direction] = temp;
        steps = newSteps;
    }

    function toggleDependency(stepIndex: number, depId: string) {
        const step = steps[stepIndex];
        if (step.depends_on.includes(depId)) {
            step.depends_on = step.depends_on.filter(d => d !== depId);
        } else {
            step.depends_on = [...step.depends_on, depId];
        }
        steps = [...steps];
    }

    function addEnv(stepIndex: number) {
        steps[stepIndex].env = [...steps[stepIndex].env, { key: '', value: '' }];
    }

    function removeEnv(stepIndex: number, envIndex: number) {
        steps[stepIndex].env = steps[stepIndex].env.filter((_, i) => i !== envIndex);
    }

    function addWith(stepIndex: number) {
        steps[stepIndex].with = [...steps[stepIndex].with, { key: '', value: '' }];
    }

    function removeWith(stepIndex: number, withIndex: number) {
        steps[stepIndex].with = steps[stepIndex].with.filter((_, i) => i !== withIndex);
    }

    function addUpload(stepIndex: number) {
        steps[stepIndex].artifact_uploads = [...steps[stepIndex].artifact_uploads, { name: '', path: '' }];
    }

    function removeUpload(stepIndex: number, artIndex: number) {
        steps[stepIndex].artifact_uploads = steps[stepIndex].artifact_uploads.filter((_, i) => i !== artIndex);
    }

    function addDownload(stepIndex: number) {
        steps[stepIndex].artifact_downloads = [...steps[stepIndex].artifact_downloads, { name: '', dest: '' }];
    }

    function removeDownload(stepIndex: number, artIndex: number) {
        steps[stepIndex].artifact_downloads = steps[stepIndex].artifact_downloads.filter((_, i) => i !== artIndex);
    }

    function removeSecret(stepIndex: number, secret: string) {
        steps[stepIndex].secrets = steps[stepIndex].secrets.filter(s => s !== secret);
    }

    function removeInput(stepIndex: number, input: string) {
        steps[stepIndex].inputs = steps[stepIndex].inputs.filter(i => i !== input);
    }

    function addMatrix(stepIndex: number) {
        steps[stepIndex].matrix = [...steps[stepIndex].matrix, { key: '', values: [] }];
        steps = [...steps];
    }

    function removeMatrix(stepIndex: number, matrixIndex: number) {
        steps[stepIndex].matrix = steps[stepIndex].matrix.filter((_, i) => i !== matrixIndex);
        steps = [...steps];
    }

    function removeMatrixValue(stepIndex: number, matrixIndex: number, valueIndex: number) {
        steps[stepIndex].matrix[matrixIndex].values = steps[stepIndex].matrix[matrixIndex].values.filter((_, i) => i !== valueIndex);
        steps = [...steps];
    }

    function removeReleaseArtifact(stepIndex: number, art: string) {
        steps[stepIndex].release.artifacts = steps[stepIndex].release.artifacts.filter(a => a !== art);
        steps = [...steps];
    }

    $: yaml = generateYAML(pipelineName, steps);

    function generateYAML(name: string, steps: Step[]) {
        let lines = [`name: ${name}`, `steps:`];
        for (const s of steps) {
            lines.push(`  - id: ${s.id}`);
            if (s.type !== 'command') {
                lines.push(`    type: ${s.type}`);
            }
            if (s.type === 'pipeline') {
                lines.push(`    pipeline_ref:`);
                lines.push(`      name: ${s.pipeline_ref.name}`);
                if (s.pipeline_ref.path) {
                    lines.push(`      path: ${s.pipeline_ref.path}`);
                }
            } else if (s.type !== 'approval' && s.type !== 'release') {
                lines.push(`    image: ${s.image}`);
                if (s.command.includes('\n')) {
                    lines.push(`    run: |`);
                    s.command.split('\n').forEach(l => lines.push(`      ${l}`));
                } else {
                    lines.push(`    run: ${s.command}`);
                }
            }

            if (s.depends_on.length > 0) {
                lines.push(`    depends_on: [${s.depends_on.join(', ')}]`);
            }
            if (s.condition) lines.push(`    condition: ${s.condition}`);
            if (s.timeout) lines.push(`    timeout: ${s.timeout}`);
            if (s.workdir) lines.push(`    workdir: ${s.workdir}`);
            if (s.always_run) lines.push(`    always_run: true`);
            if (s.docker_socket) lines.push(`    docker_socket: true`);
            if (s.secrets.length > 0) lines.push(`    secrets: [${s.secrets.join(', ')}]`);
            if (s.inputs.length > 0) lines.push(`    inputs: [${s.inputs.join(', ')}]`);
            
            if (s.type === 'release' && s.release.tag) {
                lines.push(`    release:`);
                lines.push(`      tag: ${s.release.tag}`);
                if (s.release.name) lines.push(`      name: ${s.release.name}`);
                if (s.release.body) lines.push(`      body: ${s.release.body}`);
                if (s.release.artifacts.length > 0) {
                    lines.push(`      artifacts: [${s.release.artifacts.join(', ')}]`);
                }
            }
            
            if (s.matrix.length > 0) {
                lines.push(`    matrix:`);
                s.matrix.forEach(m => {
                    if (m.key) {
                        lines.push(`      ${m.key}: [${m.values.join(', ')}]`);
                    }
                });
            }
            
            if (s.env.length > 0) {
                lines.push(`    env:`);
                s.env.forEach(e => {
                    if (e.key) lines.push(`      ${e.key}: ${e.value}`);
                });
            }

            if (s.with.length > 0) {
                lines.push(`    with:`);
                s.with.forEach(w => {
                    if (w.key) lines.push(`      ${JSON.stringify(w.key)}: ${JSON.stringify(w.value)}`);
                });
            }

            if (s.testReport) lines.push(`    test_report: ${JSON.stringify(s.testReport)}`);

            if (s.split.enabled && s.type !== 'generator') {
                const shards = Math.max(2, Math.floor(Number(s.split.shards)) || 2);
                lines.push(`    split:`);
                lines.push(`      shards: ${shards}`);
                if (s.split.historyDays > 0) lines.push(`      history_days: ${s.split.historyDays}`);
                if (s.split.minHistoryRuns > 0) lines.push(`      min_history_runs: ${s.split.minHistoryRuns}`);
                if (s.split.fallback !== 'single') lines.push(`      fallback: ${s.split.fallback}`);
            }

            if (s.artifact_uploads.length > 0 || s.artifact_downloads.length > 0) {
                lines.push(`    artifacts:`);
                if (s.artifact_uploads.length > 0) {
                    lines.push(`      upload:`);
                    s.artifact_uploads.forEach(a => {
                        if (a.name) {
                            lines.push(`        - name: ${a.name}`);
                            lines.push(`          path: ${a.path}`);
                        } else {
                            lines.push(`        - path: ${a.path}`);
                        }
                    });
                }
                if (s.artifact_downloads.length > 0) {
                    lines.push(`      download:`);
                    s.artifact_downloads.forEach(a => {
                        if (a.dest) {
                            lines.push(`        - name: ${a.name}`);
                            lines.push(`          dest: ${a.dest}`);
                        } else {
                            lines.push(`        - name: ${a.name}`);
                        }
                    });
                }
            }
        }
        return lines.join('\n');
    }

    function copyToClipboard() {
        navigator.clipboard.writeText(yaml);
        alert("Pipeline YAML copied to clipboard!");
    }

    function scrollToStep(id: string) {
        activeTab = 'edit';
        const index = steps.findIndex(s => s.id === id);
        if (index !== -1) {
            steps[index].expanded = true;
            steps = [...steps];
            setTimeout(() => {
                const el = document.getElementById(`step-card-${id}`);
                if (el) el.scrollIntoView({ behavior: 'smooth', block: 'center' });
            }, 100);
        }
    }

    let activeTab: 'edit' | 'graph' = 'edit';
</script>

<div class="editor-container">
    <div class="header">
        <div class="title-group">
            <Layers size={24} class="title-icon" />
            <h1>Pipeline Designer</h1>
        </div>
        <div class="actions">
            <div class="tabs-nav">
                <button class:active={activeTab === 'edit'} on:click={() => activeTab = 'edit'}>Editor</button>
                <button class:active={activeTab === 'graph'} on:click={() => activeTab = 'graph'}>Graph View</button>
            </div>
            <button class="btn primary" on:click={addStep}>
                <Plus size={16} /> Add Step
            </button>
            <button class="btn secondary" on:click={copyToClipboard}>
                <Code size={16} /> Copy YAML
            </button>
        </div>
    </div>

    <div class="layout">
        <div class="steps-list">
            <div class="pipeline-meta">
                <div class="field">
                    <label>Pipeline Name</label>
                    <input type="text" bind:value={pipelineName} placeholder="Enter pipeline name..." />
                </div>
            </div>

            <div class="steps-container">
                {#each steps as step, i (step.id)}
                    <div id="step-card-{step.id}" class="step-card" animate:flip={{ duration: 200 }}>
                        <div 
                            class="step-header" 
                            on:click={() => step.expanded = !step.expanded}
                            on:keydown={(e) => e.key === 'Enter' && (step.expanded = !step.expanded)}
                            aria-expanded={step.expanded}
                            role="button"
                            tabindex="0"
                        >
                            <div class="step-header-left">
                                <span class="expand-toggle">
                                    {#if step.expanded}
                                        <ChevronDown size={18} />
                                    {:else}
                                        <ChevronRight size={18} />
                                    {/if}
                                </span>
                                <span class="step-type-badge {step.type}">{step.type}</span>
                                <input 
                                    type="text" 
                                    bind:value={step.id} 
                                    class="id-input" 
                                    on:click|stopPropagation 
                                    placeholder="step-id"
                                    aria-label="Step ID"
                                />
                            </div>
                            <div class="step-header-actions" on:click|stopPropagation role="none">
                                <button class="btn-icon" on:click={() => moveStep(i, -1)} disabled={i === 0} title="Move Up" type="button">
                                    <MoveUp size={14} />
                                </button>
                                <button class="btn-icon" on:click={() => moveStep(i, 1)} disabled={i === steps.length - 1} title="Move Down" type="button">
                                    <MoveDown size={14} />
                                </button>
                                <button class="btn-icon danger" on:click={() => removeStep(i)} title="Remove Step" type="button">
                                    <Trash2 size={14} />
                                </button>
                            </div>
                        </div>
                        
                        {#if step.expanded}
                            <div class="step-body">
                                <div class="grid-2">
                                    <div class="field">
                                        <label for="step-type-{step.id}">Step Type</label>
                                        <select id="step-type-{step.id}" bind:value={step.type}>
                                            <option value="command">Standard Command</option>
                                            <option value="generator">Generator (Dynamic)</option>
                                            <option value="pipeline">Trigger Pipeline</option>
                                            <option value="release">SCM Release</option>
                                            <option value="approval">Manual Approval</option>
                                        </select>
                                    </div>
                                    {#if step.type !== 'pipeline' && step.type !== 'approval' && step.type !== 'release'}
                                        <div class="field">
                                            <label for="step-image-{step.id}">Docker Image</label>
                                            <input id="step-image-{step.id}" type="text" bind:value={step.image} placeholder="e.g. alpine:latest" />
                                        </div>
                                    {/if}
                                </div>
                                
                                {#if step.type === 'pipeline'}
                                    <div class="grid-2">
                                        <div class="field">
                                            <label for="step-pipe-name-{step.id}">Target Pipeline Name</label>
                                            <input id="step-pipe-name-{step.id}" type="text" bind:value={step.pipeline_ref.name} placeholder="e.g. deploy-prod" />
                                        </div>
                                        <div class="field">
                                            <label for="step-pipe-path-{step.id}">Path (optional)</label>
                                            <input id="step-pipe-path-{step.id}" type="text" bind:value={step.pipeline_ref.path} placeholder="e.g. .forge/deploy.yml" />
                                        </div>
                                    </div>
                                {:else if step.type === 'release'}
                                    <div class="grid-2">
                                        <div class="field">
                                            <label for="step-rel-tag-{step.id}">Git Tag</label>
                                            <input id="step-rel-tag-{step.id}" type="text" bind:value={step.release.tag} placeholder="e.g. v1.0.0" />
                                        </div>
                                        <div class="field">
                                            <label for="step-rel-name-{step.id}">Release Title</label>
                                            <input id="step-rel-name-{step.id}" type="text" bind:value={step.release.name} placeholder="e.g. Version 1.0.0" />
                                        </div>
                                    </div>
                                    <div class="field">
                                        <label for="step-rel-body-{step.id}">Release Description</label>
                                        <textarea id="step-rel-body-{step.id}" bind:value={step.release.body} rows="2" placeholder="Changelog..."></textarea>
                                    </div>
                                    <div class="sub-section" style="margin-bottom: 12px;">
                                        <span class="label">Release Artifacts (Attached to GitHub Release)</span>
                                        <div class="dep-chips">
                                            {#each step.release.artifacts as art}
                                                <span class="chip">
                                                    {art}
                                                    <button class="chip-remove" on:click={() => removeReleaseArtifact(i, art)} title="Remove Artifact">&times;</button>
                                                </span>
                                            {/each}
                                            <input 
                                                type="text" 
                                                class="chip-input" 
                                                placeholder="+ Add Artifact..."
                                                on:keydown={(e) => {
                                                    if (e.key === 'Enter') {
                                                        e.preventDefault();
                                                        const val = e.currentTarget.value.trim();
                                                        if (val) {
                                                            step.release.artifacts = [...step.release.artifacts, val];
                                                            e.currentTarget.value = '';
                                                            steps = [...steps];
                                                        }
                                                    }
                                                }}
                                            />
                                        </div>
                                    </div>
                                {:else if step.type !== 'approval' && step.type !== 'release'}
                                    <div class="field">
                                        <label for="step-command-{step.id}">Command / Script</label>
                                        <textarea id="step-command-{step.id}" bind:value={step.command} rows="3" placeholder="Enter shell commands..."></textarea>
                                    </div>
                                {/if}

                                <div class="section">
                                    <div class="section-header">
                                        <Layers size={14} />
                                        <span>Dependencies</span>
                                    </div>
                                    <div class="dep-chips">
                                        {#each steps as other}
                                            {#if other.id !== step.id}
                                                <button 
                                                    class="chip" 
                                                    class:selected={step.depends_on.includes(other.id)}
                                                    on:click={() => toggleDependency(i, other.id)}
                                                >
                                                    {other.id}
                                                </button>
                                            {/if}
                                        {/each}
                                        {#if steps.length <= 1}
                                            <span class="empty-text">No other steps to depend on.</span>
                                        {/if}
                                    </div>
                                </div>

                                <div class="section">
                                    <div class="section-header">
                                        <Cpu size={14} />
                                        <span>Matrix Strategy</span>
                                        <button class="btn-add-small" on:click={() => addMatrix(i)}><Plus size={12} /></button>
                                    </div>
                                    {#each step.matrix as m, mi}
                                        <div class="matrix-row">
                                            <input type="text" bind:value={m.key} placeholder="Dimension (e.g. os)" style="width: 150px;" />
                                            <div class="dep-chips" style="flex: 1; margin: 0;">
                                                {#each m.values as val, vi}
                                                    <span class="chip">
                                                        {val}
                                                        <button class="chip-remove" on:click={() => removeMatrixValue(i, mi, vi)}>&times;</button>
                                                    </span>
                                                {/each}
                                                <input 
                                                    type="text" 
                                                    class="chip-input" 
                                                    placeholder="+ Add Value..."
                                                    on:keydown={(e) => {
                                                        if (e.key === 'Enter') {
                                                            e.preventDefault();
                                                            const val = e.currentTarget.value.trim();
                                                            if (val) {
                                                                m.values = [...m.values, val];
                                                                e.currentTarget.value = '';
                                                                steps = [...steps];
                                                            }
                                                        }
                                                    }}
                                                />
                                            </div>
                                            <button class="btn-icon danger" on:click={() => removeMatrix(i, mi)}><Trash2 size={12} /></button>
                                        </div>
                                    {/each}
                                    {#if step.matrix.length === 0}
                                        <span class="empty-text">No matrix dimensions defined.</span>
                                    {/if}
                                </div>

                                {#if step.type === 'command'}
                                    <div class="section">
                                        <div class="section-header">
                                            <Layers size={14} />
                                            <span>Test Splitting</span>
                                        </div>
                                        <label class="checkbox-label" style="margin-bottom: 8px;">
                                            <input type="checkbox" bind:checked={step.split.enabled} />
                                            Split this step across parallel shards, balanced by historical test runtime
                                        </label>
                                        {#if step.split.enabled}
                                            <div class="advanced-grid">
                                                <div class="field">
                                                    <label for="step-report-{step.id}">Test Report Path</label>
                                                    <input id="step-report-{step.id}" type="text" bind:value={step.testReport} placeholder="e.g. .forge/test-report.json" />
                                                    <span class="field-hint">Required — this is the file your command must write (see `forge report` in the CLI reference) for shard timing history to exist at all. Without it, splitting has nothing to balance and falls back to running everything on one shard.</span>
                                                </div>
                                                <div class="field">
                                                    <label for="step-shards-{step.id}">Shards</label>
                                                    <input
                                                        id="step-shards-{step.id}"
                                                        type="number"
                                                        min="2"
                                                        bind:value={step.split.shards}
                                                        on:blur={() => {
                                                            const n = Math.floor(Number(step.split.shards));
                                                            step.split.shards = Number.isFinite(n) && n >= 2 ? n : 2;
                                                        }}
                                                    />
                                                    <span class="field-hint">Minimum 2 — the compiler rejects anything lower.</span>
                                                </div>
                                            </div>
                                            <div class="advanced-grid">
                                                <div class="field">
                                                    <label for="step-hist-days-{step.id}">History Window (days)</label>
                                                    <input id="step-hist-days-{step.id}" type="number" min="1" bind:value={step.split.historyDays} />
                                                    <span class="field-hint">Default 14. How far back to look for per-file timing.</span>
                                                </div>
                                                <div class="field">
                                                    <label for="step-min-runs-{step.id}">Min History Runs</label>
                                                    <input id="step-min-runs-{step.id}" type="number" min="1" bind:value={step.split.minHistoryRuns} />
                                                    <span class="field-hint">Default 3. A file needs this many recorded runs before its timing is trusted for balancing.</span>
                                                </div>
                                            </div>
                                            <div class="field">
                                                <label for="step-fallback-{step.id}">Cold-Start Fallback</label>
                                                <select id="step-fallback-{step.id}" bind:value={step.split.fallback}>
                                                    <option value="single">Single — one shard runs everything, others no-op (default)</option>
                                                    <option value="round-robin">Round-robin — every shard runs everything until history exists</option>
                                                </select>
                                                <span class="field-hint">Applies only when no shard has any timing history yet. "Single" avoids every shard redundantly running the full suite on the first run.</span>
                                            </div>
                                        {/if}
                                    </div>
                                {/if}

                                <div class="grid-2">
                                    <div class="section">
                                        <div class="section-header">
                                            <Upload size={14} />
                                            <span>Artifacts (Upload)</span>
                                            <button class="btn-add-small" on:click={() => addUpload(i)}><Plus size={12} /></button>
                                        </div>
                                        {#each step.artifact_uploads as art, ai}
                                            <div class="inline-fields">
                                                <input type="text" bind:value={art.name} placeholder="Name" />
                                                <input type="text" bind:value={art.path} placeholder="Path" />
                                                <button class="btn-icon danger" on:click={() => removeUpload(i, ai)}><Trash2 size={12} /></button>
                                            </div>
                                        {/each}
                                    </div>
                                    <div class="section">
                                        <div class="section-header">
                                            <Download size={14} />
                                            <span>Artifacts (Download)</span>
                                            <button class="btn-add-small" on:click={() => addDownload(i)}><Plus size={12} /></button>
                                        </div>
                                        {#each step.artifact_downloads as art, ai}
                                            <div class="inline-fields">
                                                <input type="text" bind:value={art.name} placeholder="Name" />
                                                <input type="text" bind:value={art.dest} placeholder="Dest" />
                                                <button class="btn-icon danger" on:click={() => removeDownload(i, ai)}><Trash2 size={12} /></button>
                                            </div>
                                        {/each}
                                    </div>
                                </div>

                                <div class="section">
                                    <div class="section-header">
                                        <Settings size={14} />
                                        <span>Advanced Configuration</span>
                                    </div>
                                    <div class="advanced-grid">
                                        <div class="field">
                                            <label for="step-cond-{step.id}">Condition (CEL expression)</label>
                                            <input id="step-cond-{step.id}" type="text" bind:value={step.condition} placeholder="e.g. event == 'push'" />
                                        </div>
                                        <div class="field">
                                            <label for="step-workdir-{step.id}">Working Directory</label>
                                            <input id="step-workdir-{step.id}" type="text" bind:value={step.workdir} placeholder="e.g. /workspace/src" />
                                        </div>
                                    </div>

                                    <div class="advanced-grid">
                                        <div class="field">
                                            <label for="step-timeout-{step.id}">Timeout</label>
                                            <input id="step-timeout-{step.id}" type="text" bind:value={step.timeout} placeholder="e.g. 10m, 1h" />
                                        </div>
                                        <div class="field-row">
                                            <div class="checkbox-group">
                                                <label class="checkbox-label">
                                                    <input type="checkbox" bind:checked={step.docker_socket} />
                                                    Mount Docker Socket
                                                </label>
                                                <label class="checkbox-label">
                                                    <input type="checkbox" bind:checked={step.always_run} />
                                                    Always Run
                                                </label>
                                            </div>
                                        </div>
                                    </div>
                                    
                                    <div class="grid-2">
                                        <div class="sub-section">
                                            <span class="label">Environment Variables</span>
                                            {#each step.env as e, ei}
                                                <div class="inline-fields">
                                                    <input type="text" bind:value={e.key} placeholder="KEY" aria-label="Env Key" />
                                                    <input type="text" bind:value={e.value} placeholder="VALUE" aria-label="Env Value" />
                                                    <button class="btn-icon danger" on:click={() => removeEnv(i, ei)} title="Remove Env Var"><Trash2 size={12} /></button>
                                                </div>
                                            {/each}
                                            <button class="btn-text" on:click={() => addEnv(i)}>+ Add Variable</button>
                                        </div>
                                        <div class="sub-section">
                                            <span class="label">Secrets</span>
                                            <div class="dep-chips">
                                                {#each step.secrets as s}
                                                    <span class="chip secret">
                                                        <Lock size={10} /> {s}
                                                        <button class="chip-remove" on:click={() => removeSecret(i, s)} title="Remove Secret">&times;</button>
                                                    </span>
                                                {/each}
                                                <input 
                                                    type="text" 
                                                    class="chip-input" 
                                                    placeholder="+ Add Secret..."
                                                    on:keydown={(e) => {
                                                        if (e.key === 'Enter') {
                                                            e.preventDefault();
                                                            const val = e.currentTarget.value.trim();
                                                            if (val) {
                                                                step.secrets = [...step.secrets, val];
                                                                e.currentTarget.value = '';
                                                                steps = [...steps];
                                                            }
                                                        }
                                                    }}
                                                />
                                            </div>
                                        </div>
                                    </div>

                                    <div class="sub-section" style="margin-top: 12px;">
                                        <span class="label">
                                            With Parameters
                                            {#if step.type === 'generator'}
                                                <span class="label-hint">— passed to the generator's stdin as structured input</span>
                                            {:else}
                                                <span class="label-hint">— used for ${'{{'} inputs.NAME {'}}'} substitution in step templates, or by generator steps</span>
                                            {/if}
                                        </span>
                                        {#each step.with as w, wi}
                                            <div class="inline-fields">
                                                <input type="text" bind:value={w.key} placeholder="KEY" aria-label="With Key" />
                                                <input type="text" bind:value={w.value} placeholder="VALUE" aria-label="With Value" />
                                                <button class="btn-icon danger" on:click={() => removeWith(i, wi)} title="Remove Parameter"><Trash2 size={12} /></button>
                                            </div>
                                        {/each}
                                        <button class="btn-text" on:click={() => addWith(i)}>+ Add Parameter</button>
                                    </div>

                                    <div class="sub-section" style="margin-top: 12px;">
                                        <span class="label">Cache Inputs (Glob patterns)</span>
                                        <div class="dep-chips">
                                            {#each step.inputs as inp}
                                                <span class="chip">
                                                    {inp}
                                                    <button class="chip-remove" on:click={() => removeInput(i, inp)} title="Remove Input">&times;</button>
                                                </span>
                                            {/each}
                                            <input 
                                                type="text" 
                                                class="chip-input" 
                                                placeholder="+ Add Input Glob..."
                                                on:keydown={(e) => {
                                                    if (e.key === 'Enter') {
                                                        e.preventDefault();
                                                        const val = e.currentTarget.value.trim();
                                                        if (val) {
                                                            step.inputs = [...step.inputs, val];
                                                            e.currentTarget.value = '';
                                                            steps = [...steps];
                                                        }
                                                    }
                                                }}
                                            />
                                        </div>
                                    </div>
                                </div>
                            </div>
                        {/if}
                    </div>
                {/each}
            </div>
        </div>

        <div class="preview-panel">
            {#if activeTab === 'edit'}
                <div class="preview-header">
                    <h2>YAML Output</h2>
                    <span class="preview-badge">Auto-updated</span>
                </div>
                <div class="preview-content">
                    <pre><code>{yaml}</code></pre>
                </div>
            {:else}
                <div class="preview-header">
                    <h2>Dependency Graph</h2>
                    <span class="preview-badge">Visual Preview</span>
                </div>
                <div class="graph-preview">
                    <EditorDAG {steps} onSelectStep={scrollToStep} />
                    <div class="graph-hint">
                        💡 Click a node to jump to its configuration. Reorder steps or use "Depends On" to build your pipeline's DAG.
                    </div>
                </div>
            {/if}
        </div>
    </div>
</div>

<style>
    .editor-container {
        padding: 24px;
        height: 100%;
        display: flex;
        flex-direction: column;
        background: #0d1117;
        color: #e6edf3;
    }

    .header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 24px;
        background: #161b22;
        padding: 16px;
        border-radius: 8px;
        border: 1px solid #30363d;
    }

    .title-group {
        display: flex;
        align-items: center;
        gap: 12px;
    }

    .title-icon {
        color: #58a6ff;
    }

    .header h1 {
        margin: 0;
        font-size: 1.25rem;
        font-weight: 600;
    }

    .tabs-nav {
        display: flex;
        background: #0d1117;
        padding: 4px;
        border-radius: 6px;
        margin-right: 16px;
        border: 1px solid #30363d;
    }

    .tabs-nav button {
        padding: 4px 12px;
        border: none;
        background: transparent;
        color: #8b949e;
        border-radius: 4px;
        cursor: pointer;
        font-size: 0.85rem;
        font-weight: 500;
    }

    .tabs-nav button.active {
        background: #21262d;
        color: #58a6ff;
    }

    .layout {
        display: grid;
        grid-template-columns: 1fr 450px;
        gap: 24px;
        flex: 1;
        overflow: hidden;
    }

    .steps-list {
        overflow-y: auto;
        padding-right: 8px;
    }

    .pipeline-meta {
        margin-bottom: 24px;
    }

    .steps-container {
        display: flex;
        flex-direction: column;
        gap: 16px;
    }

    .step-card {
        background: #161b22;
        border: 1px solid #30363d;
        border-radius: 8px;
        overflow: hidden;
        transition: border-color 0.2s;
    }

    .step-card:hover {
        border-color: #484f58;
    }

    .step-header {
        padding: 12px 16px;
        display: flex;
        justify-content: space-between;
        align-items: center;
        cursor: pointer;
        background: #21262d;
        width: 100%;
        border: none;
        color: inherit;
        text-align: left;
        font-family: inherit;
    }

    .step-header:focus-visible {
        outline: 2px solid #58a6ff;
        outline-offset: -2px;
    }

    .step-header-left {
        display: flex;
        align-items: center;
        gap: 12px;
        flex: 1;
    }

    .expand-toggle {
        background: transparent;
        border: none;
        color: #8b949e;
        cursor: pointer;
        display: flex;
        padding: 0;
    }

    .step-type-badge {
        font-size: 0.7rem;
        text-transform: uppercase;
        font-weight: 800;
        padding: 2px 6px;
        border-radius: 4px;
        letter-spacing: 0.5px;
    }

    .step-type-badge.command { background: #23863622; color: #3fb950; border: 1px solid #23863644; }
    .step-type-badge.generator { background: #9e6a0322; color: #d29922; border: 1px solid #9e6a0344; }
    .step-type-badge.pipeline { background: #1f6feb22; color: #58a6ff; border: 1px solid #1f6feb44; }
    .step-type-badge.release { background: #8957e522; color: #a371f7; border: 1px solid #8957e544; }
    .step-type-badge.approval { background: #d2992222; color: #f0883e; border: 1px solid #d2992244; }

    .id-input {
        background: transparent;
        border: none;
        color: #e6edf3;
        font-weight: 600;
        font-size: 0.95rem;
        width: 200px;
        padding: 2px 4px;
        border-radius: 4px;
    }

    .id-input:focus {
        background: #0d1117;
        outline: 1px solid #58a6ff;
    }

    .step-header-actions {
        display: flex;
        gap: 4px;
    }

    .step-body {
        padding: 16px;
        border-top: 1px solid #30363d;
        display: flex;
        flex-direction: column;
        gap: 16px;
    }

    .grid-2 {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 16px;
    }

    .field {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .field label {
        font-size: 0.75rem;
        font-weight: 600;
        color: #8b949e;
    }

    .field-hint {
        font-size: 0.7rem;
        color: #6e7681;
        line-height: 1.4;
    }

    input[type="text"], input[type="number"], textarea, select {
        background: #0d1117;
        border: 1px solid #30363d;
        border-radius: 6px;
        color: #e6edf3;
        padding: 8px 12px;
        font-size: 0.9rem;
        width: 100%;
    }

    input:focus, textarea:focus, select:focus {
        outline: none;
        border-color: #58a6ff;
        box-shadow: 0 0 0 3px rgba(88, 166, 255, 0.1);
    }

    .section {
        background: #0d1117;
        padding: 12px;
        border-radius: 6px;
        border: 1px solid #30363d;
    }

    .section-header {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 12px;
        font-size: 0.75rem;
        font-weight: 600;
        color: #8b949e;
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }

    .btn-add-small {
        background: #23863622;
        color: #3fb950;
        border: 1px solid #23863644;
        border-radius: 4px;
        padding: 0 4px;
        cursor: pointer;
        margin-left: auto;
    }

    .inline-fields {
        display: grid;
        grid-template-columns: 1fr 1fr 32px;
        gap: 8px;
        margin-bottom: 8px;
    }

    .dep-chips {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
    }

    .chip {
        background: #21262d;
        border: 1px solid #30363d;
        color: #c9d1d9;
        border-radius: 16px;
        padding: 4px 12px;
        font-size: 0.75rem;
        cursor: pointer;
        transition: all 0.2s;
    }

    .chip.selected {
        background: #23863622;
        border-color: #238636;
        color: #3fb950;
    }

    .chip.secret {
        background: #1f6feb11;
        border-color: #1f6feb44;
        color: #58a6ff;
        display: flex;
        align-items: center;
        gap: 6px;
    }

    .chip-remove {
        background: transparent;
        border: none;
        color: currentColor;
        opacity: 0.5;
        cursor: pointer;
        font-size: 1rem;
        line-height: 1;
        padding: 0;
    }

    .chip-remove:hover { opacity: 1; }

    .advanced-grid {
        display: grid;
        grid-template-columns: 1fr 200px;
        gap: 16px;
        margin-bottom: 16px;
    }

    .field-row {
        display: flex;
        align-items: center;
        padding-top: 24px;
    }

    .checkbox-group {
        display: flex;
        gap: 20px;
    }

    .checkbox-label {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 0.85rem;
        cursor: pointer;
    }

    .sub-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .sub-section .label {
        font-size: 0.75rem;
        color: #8b949e;
        font-weight: 600;
    }

    .label-hint {
        font-weight: 400;
        font-style: italic;
        color: #6e7681;
    }

    .btn-text {
        background: transparent;
        border: none;
        color: #58a6ff;
        font-size: 0.8rem;
        cursor: pointer;
        padding: 4px 0;
        text-align: left;
        width: fit-content;
    }

    .btn-text:hover { text-decoration: underline; }

    .preview-panel {
        background: #161b22;
        border: 1px solid #30363d;
        border-radius: 8px;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    .preview-header {
        padding: 12px 16px;
        border-bottom: 1px solid #30363d;
        display: flex;
        justify-content: space-between;
        align-items: center;
        background: #21262d;
    }

    .preview-header h2 {
        font-size: 0.85rem;
        margin: 0;
        color: #8b949e;
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }

    .preview-badge {
        font-size: 0.65rem;
        background: #23863622;
        color: #3fb950;
        padding: 2px 6px;
        border-radius: 10px;
    }

    .preview-content {
        padding: 16px;
        overflow: auto;
        flex: 1;
    }

    pre {
        margin: 0;
        font-family: 'ui-monospace', 'SFMono-Regular', 'SF Mono', Menlo, Monaco, Consolas, monospace;
        font-size: 0.85rem;
        line-height: 1.5;
        color: #c9d1d9;
    }

    .graph-preview {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    .graph-hint {
        margin-top: auto;
        background: #1f6feb11;
        border: 1px solid #1f6feb44;
        padding: 12px;
        border-radius: 6px;
        font-size: 0.8rem;
        color: #8b949e;
        line-height: 1.4;
    }

    .btn {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 16px;
        border-radius: 6px;
        font-size: 0.85rem;
        font-weight: 500;
        cursor: pointer;
        transition: all 0.2s;
    }

    .btn.primary {
        background: #238636;
        border: 1px solid #2ea44f;
        color: white;
    }

    .btn.primary:hover { background: #2ea44f; }

    .btn.secondary {
        background: #21262d;
        border: 1px solid #30363d;
        color: #c9d1d9;
    }

    .btn.secondary:hover { background: #30363d; }

    .btn-icon {
        background: transparent;
        border: none;
        color: #8b949e;
        cursor: pointer;
        padding: 4px;
        border-radius: 4px;
        display: flex;
    }

    .btn-icon:hover:not(:disabled) {
        background: #30363d;
        color: #e6edf3;
    }

    .btn-icon.danger:hover {
        background: #da363322;
        color: #f85149;
    }

    .btn-icon:disabled {
        opacity: 0.3;
        cursor: not-allowed;
    }

    .matrix-row {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 8px;
        background: #161b22;
        padding: 8px;
        border-radius: 4px;
    }

    .empty-text {
        font-size: 0.75rem;
        color: #484f58;
        font-style: italic;
    }

    .chip-input {
        background: transparent !important;
        border: 1px dashed #30363d !important;
        border-radius: 16px !important;
        padding: 4px 12px !important;
        font-size: 0.75rem !important;
        width: 120px !important;
        color: #8b949e !important;
    }

    .chip-input:focus {
        border-style: solid !important;
        border-color: #58a6ff !important;
        color: #e6edf3 !important;
        width: 200px !important;
    }
</style>
