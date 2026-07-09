import { writable } from 'svelte/store';
import type { Run, RunDetail, Job, Artifact } from './api';

export const runs = writable<Run[]>([]);
export const activeRun = writable<RunDetail | null>(null);
export const selectedJob = writable<Job | null>(null);
export const artifacts = writable<Artifact[]>([]);
export const connStatus = writable<'idle' | 'connecting' | 'live' | 'reconnecting' | 'done' | 'error'>('idle');
export const authRequired = writable(false);

export type View = 'runs' | 'projects' | 'orgs' | 'policies' | 'tokens';
export const currentView = writable<View>('runs');
