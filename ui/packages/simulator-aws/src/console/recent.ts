import { useSyncExternalStore } from "react";

// Console Home's "Recently visited" list: the services this operator opened
// last, newest first, remembered in this browser the way the real console
// remembers them per user.
const STORAGE_KEY = "aws-console:recently-visited";
const LIMIT = 8;

const listeners = new Set<() => void>();
let cache: string[] | null = null;

function read(): string[] {
  if (cache) return cache;
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? "[]");
    cache = Array.isArray(parsed) ? parsed.filter((value): value is string => typeof value === "string") : [];
  } catch {
    cache = [];
  }
  return cache;
}

export function recordVisit(serviceId: string): void {
  const current = read();
  if (current[0] === serviceId) return;
  cache = [serviceId, ...current.filter((id) => id !== serviceId)].slice(0, LIMIT);
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(cache));
  } catch {
    // Storage is unavailable (a private window); the list lasts for this page.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useRecentlyVisited(): string[] {
  return useSyncExternalStore(subscribe, read, read);
}
