# @sockerless/ui-simulator-azure

The Azure portal this simulator serves: a portal shell (`src/portal/`) with per-service pages under `src/pages/`, routed in `src/main.tsx` (React Router 7).

## Pages

`src/main.tsx` declares the routes. Among them:

- `/ui/` — overview
- `/ui/subscriptions` — subscriptions
- `/ui/container-apps` — Azure Container Apps
- `/ui/functions` — Azure Functions
- `/ui/acr` — Azure Container Registry
- `/ui/storage` — storage accounts
- `/ui/monitor` — Azure Monitor
- `/ui/entra/app-registrations` — Microsoft Entra app registrations
- `/ui/<service>` — the generic resource list for each service blade in `src/services.ts`
- `/ui/not-supported/:slug` — the not-supported page

Pages read the real Azure Resource Manager and Azure Monitor APIs at the portal's configured coordinate through `src/api.ts`, with operator credentials federated from Microsoft Entra (`src/portal/federation.ts`), differing from the real portal only in coordinates.

## Embedding

`make embed` in `simulator-azure/` copies this package's `dist/` to `simulator-azure/dist/` (see `make/go-app.mk`), which the binary bundles via `//go:embed all:dist` (`simulator-azure/ui_embed.go`) and serves at `/ui/`. A `-tags noui` build skips it.

## Development

- `bun run dev` — Vite dev server (`:5173`), proxying to a running simulator on `:4568`.
- `bun run build` — production bundle into `dist/`.
- `bun run preview` — serve the built bundle.
- `bun run test:e2e` — Playwright tests.
- `bun run typecheck` — `tsc --noEmit`.

The package `Makefile` wraps these as `make build` / `run` / `preview` / `test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — workspace commands, embedding, and held dependency versions.
- [`@sockerless/ui-core`](../core/README.md) — shared components, hooks, tokens.
