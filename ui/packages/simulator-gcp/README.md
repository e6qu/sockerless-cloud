# @sockerless/ui-simulator-gcp

Google Cloud console for the GCP simulator, built on the real Google Cloud console's patterns: the top app bar (navigation menu, project picker, search, notifications, help, settings and account), a product navigation for each product, resource lists with the filter bar, column display options and selection actions, full-page create forms with Equivalent code, detail pages with tabs, and a snackbar plus a notifications panel for the operations the console runs.

The console shell lives in `src/console/`, the product pages in `src/pages/`, and the routes in `src/main.tsx` (React Router). `src/console/catalog.ts` lists the products, their categories, side navigation and documentation, and drives the navigation menu, the search box and View all products.

## Pages

Paths follow the real console's URL structure under `/ui/`, and every URL carries the selected project as `?project=`:

- `/ui/welcome`, `/ui/home/dashboard`, `/ui/home/activity`, `/ui/products` — Cloud overview: project info, resource counts, the Admin Activity audit log, every product
- `/ui/compute/instances`, `/ui/compute/instancesAdd`, `/ui/compute/disks` — Compute Engine
- `/ui/networking/networks/list`, `/ui/networking/networks/add`, `/ui/networking/connectors/list` — VPC network
- `/ui/net-services/loadbalancing/list/loadBalancers`, `/ui/net-services/dns/zones` — Network services
- `/ui/run`, `/ui/run/create`, `/ui/run/jobs`, `/ui/functions/list` — Cloud Run services and jobs, Cloud Run functions
- `/ui/storage/browser`, `/ui/storage/create-bucket` — Cloud Storage buckets, objects (upload, folders, download, delete), configuration and permissions
- `/ui/sql/instances`, `/ui/firestore/databases`, `/ui/spanner/instances`, `/ui/bigtable/instances`, `/ui/memorystore/redis/instances` — databases
- `/ui/logs/query` — Logs Explorer
- `/ui/bigquery`, `/ui/cloudpubsub/topic/list`, `/ui/cloudpubsub/subscription/list`, `/ui/dataflow/jobs` — analytics, with Pub/Sub publish and pull
- `/ui/artifacts`, `/ui/cloud-build/builds`, `/ui/eventarc/triggers`, `/ui/api-gateway` — CI/CD and integration
- `/ui/security/secret-manager`, `/ui/security/kms/keyrings` — Secret Manager and Key Management
- `/ui/iam-admin/iam`, `/ui/iam-admin/roles`, `/ui/iam-admin/serviceaccounts`, `/ui/iam-admin/workforce-identity-pools`, `/ui/cloud-resource-manager`, `/ui/projectcreate` — IAM & Admin
- `/ui/apis/dashboard` — Enabled APIs & services

Pages read the real Google Cloud APIs at the console's configured cloud coordinate through `src/api.ts`. Every call carries an access token the console obtains by exchanging the operator's OpenID Connect assertion at the Security Token Service through a Workforce Identity Federation pool provider (`src/console/federation.ts`), so the console differs from the real cloud only in coordinates. Long-running operations are driven through the services' own wait or get methods.

## Embedding

`make embed` in `simulator-gcp/` copies this package's `dist/` to `simulator-gcp/dist/` (see `make/go-app.mk`), which the binary bundles via `//go:embed all:dist` (`simulator-gcp/ui_embed.go`) in a `-tags console` build and serves at `/ui/`. A plain build embeds none and serves the bundle `SIM_CONSOLE_DIR` names.

## Development

- `bun run dev` — Vite dev server (`:5173`). It serves the bundle under `/ui/` and forwards every other path of the console's origin — `/ui/config.json`, the auth layer under `/auth/`, the Security Token Service and the Google Cloud APIs — to a running simulator on `:4567`, which leaves the cloud coordinates empty so the console reads them at its own origin.
- `bun run build` — production bundle into `dist/`.
- `bun run typecheck` — `tsc --noEmit`.
- `bun run test` — vitest.
- `bun run test:e2e` — the Playwright suite. It builds and starts `simulator-gcp` with the console and `simulator-azure`, whose Microsoft Entra ID is the OpenID Connect issuer the operator signs in with; `e2e/global-setup.ts` creates the workforce pool, its provider and the project grant through the real IAM and Resource Manager APIs. The suite then drives create, list, describe and delete flows through the console and audits the main pages with axe-core in both themes. `SERVER_BIN` and `SIMULATOR_BIN` name prebuilt binaries instead.

The package `Makefile` wraps these as `make build` / `run` / `preview` / `test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — workspace commands, embedding, and dependency versions.
- [`@sockerless/ui-core`](../core/README.md) — design tokens, theme hook, error boundary, browser-suite harness.
