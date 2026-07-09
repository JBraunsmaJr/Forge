<script lang="ts">
    import { onMount, createEventDispatcher } from 'svelte';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import '@xterm/xterm/css/xterm.css';
    import { authUrl } from '../api';

    export let sessionID: string | null = null;
    export let expiresInS: number = 0;
    export let status: 'starting' | 'ready' | 'closed' = 'starting';

    const dispatch = createEventDispatcher();
    let termWrap: HTMLDivElement;
    let term: Terminal | null = null;
    let ws: WebSocket | null = null;
    let fitAddon: FitAddon | null = null;
    let ttlInterval: any;
    let ttlText = '';
    let lastSessionID: string | null = null;

    function cleanup() {
        if (ws) {
            ws.onclose = null;
            ws.onerror = null;
            ws.close();
            ws = null;
        }
        if (term) {
            term.dispose();
            term = null;
        }
        clearInterval(ttlInterval);
    }

    function startTTLTimer(seconds: number) {
        let remaining = seconds;
        clearInterval(ttlInterval);
        const update = () => {
            if (remaining <= 0) {
                clearInterval(ttlInterval);
                ttlText = 'expired';
                return;
            }
            const m = Math.floor(remaining / 60);
            const s = remaining % 60;
            ttlText = `TTL ${m}:${String(s).padStart(2, '0')}`;
            remaining--;
        };
        update();
        ttlInterval = setInterval(update, 1000);
    }

    function openTerminalWS(sid: string) {
        if (!termWrap) return;
        
        term = new Terminal({
            theme: {
                background: '#0d0d0d',
                foreground: '#e0e0e0',
                cursor:     '#7c6af7',
            },
            fontFamily: 'Menlo, Consolas, "Courier New", monospace',
            fontSize:   13,
            lineHeight: 1.3,
            cursorBlink: true,
            allowProposedApi: true,
        });

        fitAddon = new FitAddon();
        term.loadAddon(fitAddon);
        term.open(termWrap);
        fitAddon.fit();

        const scheme = location.protocol === "https:" ? "wss" : "ws";
        const wsURL  = authUrl(`${scheme}://${location.host}/api/v1/debug/${sid}/ws?cols=${term.cols}&rows=${term.rows}`);

        ws = new WebSocket(wsURL);
        ws.binaryType = 'arraybuffer';

        ws.onopen = () => {
            status = 'ready';
            term?.focus();
        };

        ws.onmessage = (e) => {
            if (e.data instanceof ArrayBuffer) {
                term?.write(new Uint8Array(e.data));
            } else {
                term?.write(e.data);
            }
        };

        ws.onerror = () => {
            status = 'closed';
        };

        ws.onclose = () => {
            status = 'closed';
        };

        const encoder = new TextEncoder();
        term.onData((data) => {
            if (ws?.readyState === WebSocket.OPEN) {
                ws.send(encoder.encode(data));
            }
        });

        term.onResize(({ cols, rows }) => {
            if (ws?.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({ type: 'resize', cols, rows }));
            }
        });

        const ro = new ResizeObserver(() => fitAddon?.fit());
        ro.observe(termWrap);
    }

    $: if (sessionID !== lastSessionID) {
        cleanup();
        lastSessionID = sessionID;
    }

    $: if (sessionID && status === 'ready' && !ws) {
        openTerminalWS(sessionID);
    }

    $: if (expiresInS > 0) {
        startTTLTimer(expiresInS);
    }

    onMount(() => {
        return () => {
            cleanup();
        };
    });

    export let hideHeader = false;

    const statusLabels = {
        starting: '● connecting…',
        ready: '● ready',
        closed: '○ closed'
    };
</script>

<div id="debug-panel" class:visible={sessionID !== null} class:no-header={hideHeader}>
    {#if !hideHeader}
    <div id="debug-header">
        <h2>Debug Terminal</h2>
        <span id="debug-status" class={status}>{statusLabels[status]}</span>
        <span id="debug-ttl">{ttlText}</span>
        <button id="debug-close-btn" on:click={() => dispatch('close')}>✕ Close</button>
    </div>
    {/if}
    <div id="debug-term-wrap" bind:this={termWrap}></div>
</div>

<style>
    #debug-panel { border-top: 1px solid var(--border); background: #0d0d0d; flex-shrink: 0;
        display: none; flex-direction: column; height: 340px; }
    #debug-panel.no-header { border-top: none; height: 100%; flex: 1; flex-shrink: 1; }
    #debug-panel.visible { display: flex; }
    #debug-header { display: flex; align-items: center; gap: 10px; padding: 6px 14px;
        border-bottom: 1px solid #222; flex-shrink: 0; background: #111; }
    #debug-header h2 { font-size: 11px; font-weight: 600; letter-spacing: 1px;
        color: #666; text-transform: uppercase; }
    #debug-status { font-size: 11px; margin-left: auto; }
    #debug-status.starting { color: var(--amber); }
    #debug-status.ready    { color: var(--green); }
    #debug-status.closed   { color: #555; }
    #debug-ttl { font-size: 11px; color: #555; font-family: var(--font-mono); }
    #debug-close-btn { background: none; border: 1px solid #333; color: #555;
        border-radius: 4px; padding: 2px 8px; cursor: pointer; font-size: 11px; }
    #debug-close-btn:hover { border-color: var(--red); color: var(--red); }
    #debug-term-wrap { flex: 1; overflow: hidden; padding: 4px; }
</style>
