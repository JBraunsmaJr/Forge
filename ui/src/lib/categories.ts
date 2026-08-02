// Shared display metadata for root-cause failure categories (issue #44).
// Single source of truth for RootCauseCard.svelte and
// FailureInsights.svelte — add a category here once, both places pick it
// up, and the two can't silently drift apart.

export const categoryLabels: Record<string, string> = {
    infrastructure: 'Infrastructure',
    dependency: 'Dependency',
    flaky_test: 'Flaky Test',
    code_defect: 'Code Defect',
    configuration: 'Configuration',
    network: 'Network',
    unknown: 'Unclassified',
};

export const categoryColors: Record<string, string> = {
    infrastructure: '#e4a390',
    dependency: '#a390e4',
    flaky_test: '#e4d490',
    code_defect: '#e49090',
    configuration: '#90c9e4',
    network: '#90e4a3',
    unknown: '#8a8a8a',
};

// Darker background tints for the badge in RootCauseCard, keyed the same
// way. Colors chosen to pair with categoryColors above (same hue, dark
// surface) rather than derived at runtime, so they stay crisp/legible.
export const categoryBadgeBg: Record<string, string> = {
    infrastructure: '#2e1d14',
    dependency: '#211a30',
    flaky_test: '#302c1a',
    code_defect: '#301a1a',
    configuration: '#1a262e',
    network: '#142e1d',
    unknown: '#26262a',
};

export function categoryLabel(category: string): string {
    return categoryLabels[category] || category;
}
