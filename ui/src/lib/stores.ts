import { writable } from 'svelte/store';
import type { Run, RunDetail, Job, Artifact, User } from './api';

export const runs = writable<Run[]>([]);
export const activeRun = writable<RunDetail | null>(null);
export const selectedJob = writable<Job | null>(null);
export const artifacts = writable<Artifact[]>([]);
export const connStatus = writable<'idle' | 'connecting' | 'live' | 'reconnecting' | 'done' | 'error'>('idle');
export const authRequired = writable(false);
export const currentUser = writable<User | null>(null);

export type View = 'runs' | 'projects' | 'orgs' | 'policies' | 'tokens' | 'agents' | 'editor' | 'search';
export const currentView = writable<View>('runs');
export const sidebarOpen = writable(false);
export const navigateToRunID = writable<string | null>(null);
