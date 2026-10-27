# @sockerless/ui-simulator-azure

The Azure portal `simulator-azure` serves: a portal shell (`src/portal/`) with
pages under `src/pages/`, routed in `src/main.tsx` (React Router).

## Pages

`src/catalog.ts` lists the services the portal serves, as All services, the
portal menu and global search show them; a service with no page is not listed.
`src/services.ts` describes each service blade by the Azure Resource Manager
list operation behind it, and one list blade and one detail blade render them
all.

| Route | Page |
|---|---|
| `/ui/`, `/ui/home` | Home |
| `/ui/allservices` | All services |
| `/ui/browse/all`, `/ui/browse/resourcegroups`, `/ui/browse/subscriptions` | All resources, resource groups, subscriptions |
| `/ui/browse/:namespace/:type` (and `/kind/:kind`) | A service blade's resource list |
| `/ui/resource/*` | A resource's detail page, by resource ID |
| `/ui/create`, `/ui/create/:namespace/:type` (and `/kind/:kind`) | Create wizards |
| `/ui/deployment/:name` | A deployment's progress |
| `/ui/entra`, `/ui/entra/users`, `/ui/entra/groups`, `/ui/entra/app-registrations` | Microsoft Entra ID |
| `/ui/monitor`, `/ui/monitor/activitylog`, `/ui/monitor/logs` | Azure Monitor |

Pages read the real Azure Resource Manager, Log Analytics and Microsoft Graph
APIs at the portal's configured coordinates through `src/api.ts`. The
operator's credentials come from Microsoft Entra ID through the console's
server-side federation broker (`src/portal/federation.ts`), so the portal
differs from the real one only in coordinates.

## Embedding

`make embed` in `simulator-azure/` copies this package's `dist/` to
`simulator-azure/dist/` (see `make/go-app.mk`), which a `-tags console` build
embeds through `//go:embed all:dist` (`simulator-azure/ui_embed.go`) and serves
at `/ui/`. A plain build embeds none and serves the bundle `SIM_CONSOLE_DIR`
names.

## Development

- `bun run dev` — Vite dev server (`:5173`). It serves the bundle under `/ui/` and forwards every other path of the portal's origin — `/ui/config.json`, the auth layer and federation broker under `/auth/`, and Azure Resource Manager, Microsoft Graph and Log Analytics — to a running simulator on `:4568`, which leaves the cloud coordinates empty so the portal reads them at its own origin. The browser reaches a storage account's Blob service at the account's own endpoint.
- `bun run build` — production bundle into `dist/`.
- `bun run preview` — serve the built bundle.
- `bun run test` — Vitest.
- `bun run test:e2e` — the Playwright suite.
- `bun run typecheck` — `tsc --noEmit`.

The package `Makefile` wraps these as `make build` / `run` / `preview` /
`test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — workspace commands, embedding, and dependency versions.
- [`@sockerless/ui-core`](../core/README.md) — design tokens, theme hook, error boundary, browser-suite harness.
