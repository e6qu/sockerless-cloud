// Bun's runtime injects a `localStorage` object that lacks getItem /
// setItem methods (it expects a `--localstorage-file` flag). vitest's
// jsdom env is sat on top of this so anything that touches localStorage
// from a component — the project picker persists the selected project —
// throws. Replace with a real in-memory store for tests, the same shim
// @sockerless/ui-core's test setup applies.
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
