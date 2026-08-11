import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Tear down rendered components between tests so DOM queries don't see
// leftovers from prior renders. (No `globals: true` in vitest config so
// this isn't auto-wired by testing-library.)
afterEach(() => {
  cleanup();
});

// Bun's runtime injects a `localStorage` object that lacks getItem /
// setItem methods (it expects a `--localstorage-file` flag). vitest's
// jsdom env is sat on top of this so anything that touches localStorage
// from a component throws. Replace with a real in-memory store for tests.
{
  const store = new Map<string, string>();
  const polyfill: Storage = {
    get length() {
      return store.size;
    },
    clear() {
      store.clear();
    },
    getItem(key) {
      return store.has(key) ? store.get(key)! : null;
    },
    key(index) {
      return Array.from(store.keys())[index] ?? null;
    },
    removeItem(key) {
      store.delete(key);
    },
    setItem(key, value) {
      store.set(key, String(value));
    },
  };
  Object.defineProperty(window, "localStorage", { value: polyfill, configurable: true });
  Object.defineProperty(window, "sessionStorage", { value: polyfill, configurable: true });
}

// Bun's matchMedia returns undefined; jsdom doesn't ship one either.
// useTheme reads `prefers-color-scheme` so give tests a stable false.
if (!window.matchMedia) {
  Object.defineProperty(window, "matchMedia", {
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
    configurable: true,
  });
}
