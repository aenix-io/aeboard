// What this browser remembers for its visitor: favorite documents and the
// ones opened lately. Both stay in the browser (localStorage) — the notes
// themselves are shared, these are one person's.
import { useSyncExternalStore } from 'react';

const FAVORITES = 'aenotes.favorites';
const RECENT = 'aenotes.recent';
const CHANGED = 'aenotes:prefs';
const RECENT_MAX = 8;

function read<T>(key: string, fallback: T): T {
  try {
    const v = JSON.parse(localStorage.getItem(key) ?? 'null') as T | null;
    return v ?? fallback;
  } catch {
    return fallback;
  }
}

function write(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* storage blocked */
  }
  window.dispatchEvent(new Event(CHANGED));
}

// Snapshots are cached by their stored text, so React sees the same value
// until it changes.
const cache = new Map<string, { raw: string | null; value: unknown }>();
function snapshot<T>(key: string, fallback: T): T {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(key);
  } catch {
    /* storage blocked */
  }
  const c = cache.get(key);
  if (c && c.raw === raw) return c.value as T;
  const value = read(key, fallback);
  cache.set(key, { raw, value });
  return value;
}

function subscribe(fn: () => void) {
  window.addEventListener(CHANGED, fn);
  window.addEventListener('storage', fn); // other tabs
  return () => {
    window.removeEventListener(CHANGED, fn);
    window.removeEventListener('storage', fn);
  };
}

const NONE: string[] = [];

/** useFavorites is the favorite documents, in the order they were added. */
export function useFavorites(): string[] {
  return useSyncExternalStore(subscribe, () => snapshot(FAVORITES, NONE));
}

export function toggleFavorite(id: string) {
  const f = read<string[]>(FAVORITES, []);
  write(FAVORITES, f.includes(id) ? f.filter(x => x !== id) : [...f, id]);
}

/** Recent is a document opened lately and when. */
export interface Recent {
  id: string;
  at: string;
}

const NO_RECENT: Recent[] = [];

/** useRecent is the documents opened lately, the latest first. */
export function useRecent(): Recent[] {
  return useSyncExternalStore(subscribe, () => snapshot(RECENT, NO_RECENT));
}

export function noteOpened(id: string) {
  const r = read<Recent[]>(RECENT, []).filter(x => x.id !== id);
  write(RECENT, [{ id, at: new Date().toISOString() }, ...r].slice(0, RECENT_MAX));
}

const SEARCH = 'aenotes:search';

/** openSearch asks the app to show the search dialog. */
export function openSearch() {
  window.dispatchEvent(new Event(SEARCH));
}

export function onOpenSearch(fn: () => void): () => void {
  window.addEventListener(SEARCH, fn);
  return () => window.removeEventListener(SEARCH, fn);
}
