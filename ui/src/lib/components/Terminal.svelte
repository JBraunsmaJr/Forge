<script lang="ts">
    import { onMount, createEventDispatcher } from 'svelte';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import '@xterm/xterm/css/xterm.css';
    import { authUrl } from '../api';

    export let sessionID: string | null = null;
    export let status: 'starting' | 'ready' | 'closed' = 'starting';

    const dispatch = createEventDispatcher();
    let termWrap: HTMLDivElement;
    let term: Terminal | null = null;
    let ws: WebSocket | null = null;
    let fitAddon: FitAddon | null = null;
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

    onMount(() => {
        return () => {
            cleanup();
        };
    });
</script>

<div id="debug-panel" class:visible={sessionID !== null}>
    <div id="debug-term-wrap" bind:this={termWrap}></div>
</div>

<style>
    #debug-panel { background: #0d0d0d; height: 100%; flex: 1; display: none; flex-direction: column; overflow: hidden; }
    #debug-panel.visible { display: flex; }
    #debug-term-wrap { flex: 1; overflow: hidden; padding: 4px; }
</style>
