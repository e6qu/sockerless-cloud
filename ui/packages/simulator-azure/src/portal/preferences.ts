import { useSyncExternalStore } from "react";
import { DEFAULT_FAVORITES } from "../catalog.js";

// The operator's portal preferences: favorite services, recently viewed
// resources and recently used services. The portal keeps these per user in
// its own settings store; the console keeps them per browser.

export interface RecentResource {
  id: string;
  name: string;
  type: string;
  viewedAt: number;
}

interface Preferences {
  favorites: string[];
  recentResources: RecentResource[];
  recentServices: string[];
}

const STORAGE_KEY = "azure-portal:preferences";
const RECENT_LIMIT = 20;

function read(): Preferences {
  const fallback: Preferences = { favorites: DEFAULT_FAVORITES, recentResources: [], recentServices: [] };
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as Partial<Preferences>;
    return {
      favorites: parsed.favorites ?? fallback.favorites,
      recentResources: parsed.recentResources ?? [],
      recentServices: parsed.recentServices ?? [],
    };
  } catch {
    return fallback;
  }
}

let current = read();
const listeners = new Set<() => void>();

function write(next: Preferences) {
  current = next;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // Storage blocked: the preference holds for this page.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function usePreferences(): Preferences {
  return useSyncExternalStore(subscribe, () => current);
}

export function toggleFavorite(serviceId: string) {
  const favorites = current.favorites.includes(serviceId)
    ? current.favorites.filter((id) => id !== serviceId)
    : [...current.favorites, serviceId];
  write({ ...current, favorites });
}

export function recordResourceView(resource: Omit<RecentResource, "viewedAt">) {
  if (!resource.id) return;
  const rest = current.recentResources.filter((entry) => entry.id.toLowerCase() !== resource.id.toLowerCase());
  write({ ...current, recentResources: [{ ...resource, viewedAt: Date.now() }, ...rest].slice(0, RECENT_LIMIT) });
}

export function recordServiceUse(serviceId: string) {
  if (current.recentServices[0] === serviceId) return;
  write({ ...current, recentServices: [serviceId, ...current.recentServices.filter((id) => id !== serviceId)].slice(0, RECENT_LIMIT) });
}
