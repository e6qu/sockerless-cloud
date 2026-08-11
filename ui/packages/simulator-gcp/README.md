# @sockerless/ui-simulator-gcp

Google Cloud console for the GCP simulator: the console shell (header, product navigation, account control) under `src/console/`, with per-service pages under `src/pages/`, routed in `src/main.tsx` (React Router 7).

## Pages

- `/ui/` — overview
- `/ui/cloudrun` — Cloud Run jobs
- `/ui/functions` — Cloud Run functions
- `/ui/ar` — Artifact Registry
- `/ui/gcs` — Cloud Storage buckets
- `/ui/serviceaccounts` — IAM service accounts: create/delete accounts, mint and revoke keys, with the real console's one-time key download and post-mint `gcloud` usage
- `/ui/logging` — Logs Explorer

Pages read the real Google Cloud APIs at the console's configured cloud coordinate via `src/api.ts`: every call is authenticated with the operator's Shauth assertion federated through the Security Token Service token exchange (`src/console/federation.ts`), differing from the real cloud only in coordinates.

## Embedding

`make embed` in `simulator-gcp/` copies this package's `dist/` to `simulator-gcp/dist/` (see `make/go-app.mk`), which the binary bundles via `//go:embed all:dist` (`simulator-gcp/ui_embed.go`) and serves at `/ui/`. A `-tags noui` build skips it.

## Development

- `bun run dev` — Vite dev server (`:5173`), proxying `/health` and `/sim` to a running simulator on `:4567`.
- `bun run build` — production bundle into `dist/`.
- `bun run preview` — serve the built bundle.
- `bun run test:e2e` — Playwright tests.
- `bun run typecheck` — `tsc --noEmit`.

The package `Makefile` wraps these as `make build` / `run` / `preview` / `test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — dev-stack targets, ports, design system, error UX.
- [`@sockerless/ui-core`](../core/README.md) — shared components, hooks, tokens.
