# @sockerless/ui-simulator-azure

Azure Simulator dashboard. A `<SimulatorApp title="Azure Simulator" />` shell from `@sockerless/ui-core` with per-service pages under `src/pages/`, routed in `src/main.tsx` (React Router 7).

## Pages

- `/ui/` — overview
- `/ui/container-apps` — Container Apps
- `/ui/functions` — Azure Functions
- `/ui/acr` — ACR registries
- `/ui/storage` — Storage accounts
- `/ui/monitor` — Monitor

Pages fetch the simulator's `/sim/*` UI endpoints via `src/api.ts`; the shell polls `/health` through the core simulator hooks.

## Embedding

`make embed` in `simulator-azure/` copies this package's `dist/` to `simulator-azure/dist/` (see `make/go-app.mk`), which the binary bundles via `//go:embed all:dist` (`simulator-azure/ui_embed.go`) and serves at `/ui/`. A `-tags noui` build skips it.

## Development

- `bun run dev` — Vite dev server (`:5173`), proxying `/health` and `/sim` to a running simulator on `:4568`.
- `bun run build` — production bundle into `dist/`.
- `bun run preview` — serve the built bundle.
- `bun run test:e2e` — Playwright tests.
- `bun run typecheck` — `tsc --noEmit`.

The package `Makefile` wraps these as `make build` / `run` / `preview` / `test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — dev-stack targets, ports, design system, error UX.
- [`@sockerless/ui-core`](../core/README.md) — shared components, hooks, tokens.
