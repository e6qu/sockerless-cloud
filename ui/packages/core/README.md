# @sockerless/ui-core

Design tokens, the theme hook, the error boundary and the browser-suite harness
the simulator consoles share. Each console reads its cloud's real APIs through
its own client; this package holds no API client.

## Exports

- `@sockerless/ui-core/components` — `ErrorBoundary`, the catastrophic-failure
  boundary each console wraps its routes in.
- `@sockerless/ui-core/hooks` — `useTheme`, which resolves the theme from
  localStorage, then `prefers-color-scheme`, then the caller's default, and
  flips the `dark` class on `<html>`.
- `@sockerless/ui-core/styles` — `src/styles/tokens.css`, the design tokens and
  base styles every console's `index.css` imports after `@import "tailwindcss";`,
  including the `.sl-skip-link` skip link.

## Shauth product-UI contract

Authenticated console shells expose `data-shauth-user="<exact username>"` on
the visible account control and `data-shauth-sign-out` on the real sign-out
control. These stable hooks let the post-deployment browser qualification
exercise the same identity and logout controls that a user sees; they do not
replace accessible names, semantic elements, or application-local authorization
checks.

## Browser-suite harness

`e2e/` holds what each console's Playwright suite runs on:

- `run-tests.sh` — the `test:e2e` entry point.
- `start-backend.sh` — Playwright's `webServer`: builds and starts the
  simulator under test and the identity simulator, and provisions
  `SIMULATOR_SETUP` through `setup-cloud.sh`.
- `setup-cloud.sh` — provisions cloud resources through the cloud's own APIs.
- `chromium.ts` and `isolated-chromium.sh` — launch Chromium in a network
  namespace of its own on Linux.

## Development

- `bun run test` — vitest run.
- `bun run typecheck` — `tsc --noEmit`.

Run a console package's dev server to see these in a page; this package has
none.

## Test environment notes

- `vitest.config.ts` pins jsdom to `url: "http://localhost/"` so localStorage works (jsdom gates webstorage on origin).
- `src/test-setup.ts` polyfills bun's broken `localStorage` (object without methods) and missing `matchMedia`, plus wires `@testing-library/react`'s `cleanup()` between tests.
