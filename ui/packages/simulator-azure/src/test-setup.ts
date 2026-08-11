// Fluent v9's focus management (via tabster, the library its Popover, Menu,
// Dialog, and Nav components all use to move and trap focus) walks the DOM
// with `document.createTreeWalker`, filtering nodes against the real
// `NodeFilter` global's constants (`SHOW_ELEMENT`, `FILTER_ACCEPT`, …). jsdom
// implements `createTreeWalker` itself but does not define the `NodeFilter`
// global those constants live on — a real browser does — so the first Fluent
// component that mounts under vitest throws `ReferenceError: NodeFilter is
// not defined` the moment a MutationObserver callback touches it, even though
// the same component renders and behaves correctly in a real browser.
const NODE_FILTER_CONSTANTS = {
  FILTER_ACCEPT: 1,
  FILTER_REJECT: 2,
  FILTER_SKIP: 3,
  SHOW_ALL: 0xffffffff,
  SHOW_ELEMENT: 0x1,
  SHOW_ATTRIBUTE: 0x2,
  SHOW_TEXT: 0x4,
  SHOW_CDATA_SECTION: 0x8,
  SHOW_ENTITY_REFERENCE: 0x10,
  SHOW_ENTITY: 0x20,
  SHOW_PROCESSING_INSTRUCTION: 0x40,
  SHOW_COMMENT: 0x80,
  SHOW_DOCUMENT: 0x100,
  SHOW_DOCUMENT_TYPE: 0x200,
  SHOW_DOCUMENT_FRAGMENT: 0x400,
  SHOW_NOTATION: 0x800,
} as typeof NodeFilter;

// vitest's jsdom environment copies `window`'s properties onto the Node
// process's `globalThis` once, at startup — a property added to `globalThis`
// afterwards (here) does not automatically appear on `window` itself, and
// tabster's bundled output reads `NodeFilter` as a bare identifier while
// operating on `window`'s document, resolving it against `window`, not
// against the Node-side `globalThis`. Both objects need the constants.
if (typeof globalThis.NodeFilter === "undefined") {
  (globalThis as unknown as { NodeFilter: typeof NodeFilter }).NodeFilter = NODE_FILTER_CONSTANTS;
}
if (typeof window !== "undefined" && typeof window.NodeFilter === "undefined") {
  (window as unknown as { NodeFilter: typeof NodeFilter }).NodeFilter = NODE_FILTER_CONSTANTS;
}

// jsdom does not implement ResizeObserver. Fluent's `MessageBar` (used by
// this portal's error/warning banners) measures itself with one on mount to
// decide whether to reflow onto multiple lines — real behaviour that has no
// visual meaning in a headless DOM with no layout engine, so a no-op stand-in
// is enough for it to mount without throwing.
class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = NoopResizeObserver as unknown as typeof ResizeObserver;
}
if (typeof window !== "undefined" && typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = NoopResizeObserver as unknown as typeof ResizeObserver;
}
