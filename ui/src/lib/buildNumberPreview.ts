// buildNumberPreview mirrors just enough of internal/buildnumber's
// token renderer (Go) to give the format editor an instant, offline
// preview as the user types — issue #57's "live preview of a sample
// rendered build number as the user types." The authoritative
// validation (unknown/malformed tokens rejected) still happens
// server-side in api.setBuildFormat; this is preview-only and never
// blocks saving.
//
// Recognized tokens: %counter%, %counter:N%, %year%, %month%, %day%,
// %major%, %minor%.

const TOKEN = /%[^%]*%/g;

export interface BuildNumberPreview {
    // text is the rendered sample, or '' if the format couldn't be
    // rendered at all (e.g. empty input).
    text: string;
    // error describes the first unrecognized/malformed token found, if
    // any — shown as a hint, not a hard block (the save button still
    // works; the server does the real validation).
    error?: string;
}

function pad(n: number, width: number): string {
    return String(n).padStart(width, '0');
}

// MAX_COUNTER_WIDTH mirrors internal/buildnumber's own bound (the Go
// side rejects any %counter:N% where N isn't in 1..18) — enforced here
// too so a malformed or absurd format can't make the browser build an
// enormous padded string on every keystroke.
const MAX_COUNTER_WIDTH = 18;

export function previewBuildNumber(format: string, major: number, minor: number, counter = 1): BuildNumberPreview {
    if (!format || !format.trim()) {
        return { text: '' };
    }

    const now = new Date();
    let sawCounter = false;
    let error: string | undefined;

    const text = format.replace(TOKEN, (tok) => {
        const inner = tok.slice(1, -1);
        if (inner === '' || inner.includes('%')) {
            error = error || `malformed token ${tok}`;
            return tok;
        }

        // Split on the first colon only, then require the width
        // portion (if any) to be nothing but digits — parseInt alone
        // would accept "8junk" as 8, and a naive 2-element split would
        // silently drop a second colon like the "9" in "8:9" rather
        // than flagging it as malformed.
        const colonIdx = inner.indexOf(':');
        const name = colonIdx >= 0 ? inner.slice(0, colonIdx) : inner;
        const widthStr = colonIdx >= 0 ? inner.slice(colonIdx + 1) : '';

        let width = 0;
        if (colonIdx >= 0) {
            if (!/^[0-9]+$/.test(widthStr)) {
                error = error || `malformed width in token ${tok}`;
                return tok;
            }
            width = parseInt(widthStr, 10);
            if (width < 1 || width > MAX_COUNTER_WIDTH) {
                error = error || `width in token ${tok} must be between 1 and ${MAX_COUNTER_WIDTH}`;
                return tok;
            }
        }

        switch (name) {
            case 'counter':
                sawCounter = true;
                return width > 0 ? pad(counter, width) : String(counter);
            case 'year':
                if (width > 0) {
                    error = error || '%year% does not accept a width';
                    return tok;
                }
                return String(now.getUTCFullYear());
            case 'month':
                if (width > 0) {
                    error = error || '%month% does not accept a width';
                    return tok;
                }
                return pad(now.getUTCMonth() + 1, 2);
            case 'day':
                if (width > 0) {
                    error = error || '%day% does not accept a width';
                    return tok;
                }
                return pad(now.getUTCDate(), 2);
            case 'major':
                if (width > 0) {
                    error = error || '%major% does not accept a width';
                    return tok;
                }
                return String(major);
            case 'minor':
                if (width > 0) {
                    error = error || '%minor% does not accept a width';
                    return tok;
                }
                return String(minor);
            default:
                error = error || `unknown token %${name}%`;
                return tok;
        }
    });

    if (!sawCounter) {
        error = error || 'format must contain a %counter% token';
    }

    return { text, error };
}
