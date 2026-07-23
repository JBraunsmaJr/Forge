// Minimal, XSS-safe ANSI SGR -> HTML converter for the log viewer.
//
// Renders the color/style escape codes tools like go test, npm, and docker
// emit, instead of showing them as garbage glyphs. Only SGR ("...m") codes
// are interpreted; every other escape sequence (cursor movement, screen
// clears, OSC titles) is stripped. Input is HTML-escaped BEFORE any markup
// is generated, so log content can never inject HTML.

const FG: Record<number, string> = {
    30: 'a-black', 31: 'a-red', 32: 'a-green', 33: 'a-yellow',
    34: 'a-blue', 35: 'a-magenta', 36: 'a-cyan', 37: 'a-white',
    90: 'a-bblack', 91: 'a-bred', 92: 'a-bgreen', 93: 'a-byellow',
    94: 'a-bblue', 95: 'a-bmagenta', 96: 'a-bcyan', 97: 'a-bwhite',
};

function escapeHtml(s: string): string {
    return s
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}

// Matches ANSI escape sequences: CSI (ESC[ ... final byte), OSC (ESC] ... BEL/ST),
// and single-char escapes. SGR is the CSI variant ending in "m".
// eslint-disable-next-line no-control-regex
const ANSI_RE = /\x1b\[([0-9;]*)m|\x1b\[[0-9;?]*[a-lnzA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-_]/g;

export function ansiToHtml(raw: string): string {
    if (!raw.includes('\x1b')) return escapeHtml(raw);

    let out = '';
    let open = 0; // spans currently open
    let classes: string[] = [];
    let last = 0;

    const flushSpan = () => {
        while (open > 0) { out += '</span>'; open--; }
    };
    const openSpan = () => {
        if (classes.length > 0) {
            out += `<span class="${classes.join(' ')}">`;
            open++;
        }
    };

    for (const m of raw.matchAll(ANSI_RE)) {
        out += escapeHtml(raw.slice(last, m.index));
        last = (m.index ?? 0) + m[0].length;
        if (m[1] === undefined) continue; // non-SGR sequence: strip

        for (const codeStr of (m[1] === '' ? ['0'] : m[1].split(';'))) {
            const code = parseInt(codeStr, 10);
            if (code === 0) {
                classes = [];
            } else if (code === 22) {
                classes = classes.filter((c) => c !== 'a-bold' && c !== 'a-dim');
            } else if (code === 23) {
                classes = classes.filter((c) => c !== 'a-italic');
            } else if (code === 24) {
                classes = classes.filter((c) => c !== 'a-underline');
            } else if (code === 39) {
                classes = classes.filter((c) => !Object.values(FG).includes(c));
            } else if (code === 1) {
                if (!classes.includes('a-bold')) classes.push('a-bold');
            } else if (code === 2 || code === 3 || code === 4) {
                const c = code === 2 ? 'a-dim' : code === 3 ? 'a-italic' : 'a-underline';
                if (!classes.includes(c)) classes.push(c);
            } else if (FG[code]) {
                // a new foreground color replaces any prior one
                classes = classes.filter((c) => !Object.values(FG).includes(c));
                classes.push(FG[code]);
            }
            // background colors / 256-color / truecolor: ignored (stripped)
        }
        flushSpan();
        openSpan();
    }
    out += escapeHtml(raw.slice(last));
    flushSpan();
    // consecutive SGR codes produce zero-width spans; drop them
    return out.replace(/<span class="[^"]*"><\/span>/g, '');
}
