import { useSyncExternalStore } from "react";
import { DEFAULT_PINNED, PRODUCTS } from "./catalog.js";

// The navigation menu's pinned products, a per-browser preference as in the
// real console.

const STORAGE_KEY = "gcp-console-pinned-products";
const listeners = new Set<() => void>();
let cached: string[] | null = null;

function read(): string[] {
  if (cached) return cached;
  let pinned = DEFAULT_PINNED;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (Array.isArray(parsed)) {
        pinned = parsed.filter((id): id is string => typeof id === "string" && PRODUCTS.some((p) => p.id === id));
      }
    }
  } catch {
    // Unreadable storage leaves the default pins.
  }
  cached = pinned;
  return pinned;
}

function write(pinned: string[]) {
  cached = pinned;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(pinned));
  } catch {
    // A browser that refuses storage keeps the pins for this page only.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function usePinnedProducts(): { pinned: string[]; toggle: (id: string) => void } {
  const pinned = useSyncExternalStore(subscribe, read);
  return {
    pinned,
    toggle: (id: string) => write(pinned.includes(id) ? pinned.filter((entry) => entry !== id) : [...pinned, id]),
  };
}
