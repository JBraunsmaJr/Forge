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
        const [name, widthStr] = inner.split(':', 2);
        const width = widthStr ? parseInt(widthStr, 10) : 0;

        switch (name) {
            case 'counter':
                sawCounter = true;
                return width > 0 ? pad(counter, width) : String(counter);
            case 'year':
                return String(now.getUTCFullYear());
            case 'month':
                return pad(now.getUTCMonth() + 1, 2);
            case 'day':
                return pad(now.getUTCDate(), 2);
            case 'major':
                return String(major);
            case 'minor':
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
