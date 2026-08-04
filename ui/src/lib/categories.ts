/**
 * A collection of category labels used for classification purposes.
 * Each key represents a category identifier, and the corresponding value
 * is the human-readable label for that category.
 *
 * The available categories include:
 * - infrastructure: Represents categories related to infrastructure issues.
 * - dependency: Indicates issues caused by dependency-related problems.
 * - flaky_test: Refers to tests that are unstable and produce inconsistent results.
 * - code_defect: Denotes issues originating from defects or bugs in the code.
 * - configuration: Represents problems related to misconfigurations.
 * - network: Covers issues related to networking problems.
 * - unknown: Categorizes items that cannot be classified under any specific category.
 */
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

/**
 * A collection of background colors for category badges used in RootCauseCard.
 * Each key corresponds to a category identifier, and the value is the
 * hexadecimal color code for the badge background.
 */
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
