# @sockerless/ui-simulator-aws

The AWS Management Console this simulator serves: a Cloudscape shell
(`src/console/`) with per-service pages under `src/pages/`, routed in
`src/main.tsx` (React Router 7).

## How it reads AWS

Every page reads the **real AWS APIs** — the same operations, wire protocols,
and response shapes a console pointed at real AWS reads — over federated,
SigV4-signed requests. `src/console/federation.ts` exchanges the operator's
assertion for temporary credentials at the Security Token Service
(`AssumeRoleWithWebIdentity`) and `src/console/sigv4.ts` signs with them; only
the coordinates (the endpoint base URLs and the role to assume, read from
`/ui/config.json`) differ between the simulator and real AWS. There is no
simulator-only endpoint, no `/sim/*` dashboard route, and no
simulator-versus-cloud branch anywhere in the data plane.

`src/api.ts` holds one reader (and writer) per AWS operation the console
drives, named for the operation it calls.

## Shell

The shell mirrors the AWS Management Console:

- **Top navigation** (`src/console/AwsShell.tsx`) — the AWS logo linking to
  Console Home, the Services menu (the catalog by category, with Recently
  visited first), the unified search (services and their features; `Alt+S`
  focuses it), Settings (visual mode and density), the Region selector (the
  Regions EC2 `DescribeRegions` answers; the choice is the Region every request
  is signed for), and the account menu (the federated principal and account
  from STS `GetCallerIdentity`, with Sign out). It carries no control that does
  nothing: there is no notifications bell or CloudShell, because nothing here
  backs them.
- **Service side navigation**, **breadcrumbs**, and the **help panel**
  (`src/console/AwsApp.tsx`) — each service's own navigation sections, a trail
  rooted at the service, and the service's help with its documentation link.
- **Flash notifications** (`src/console/notifications.tsx`) report the outcome
  of create and delete operations.
- **Tables** (`src/console/AwsTable.tsx`) — Cloudscape `Table` with
  `PropertyFilter`, pagination, and collection preferences (page size, visible
  columns, wrap lines, striped rows, compact mode) remembered per table.
- **Delete confirmation** (`AwsDeleteModal`) — the real console's pattern of
  typing a confirmation word or the resource name before Delete enables.

## Pages

`src/console/serviceCatalog.ts` is the one table the shell reads: every AWS
service under the category AWS files it in, its Services-menu description, its
landing route, and its side navigation. A service this console has pages for
links to them; every other service still appears and links to a page that says
this console has no page for it — a statement about the console, true whichever
AWS endpoint it is pointed at. `src/main.tsx` holds the routes. Console Home
(`/ui/`) shows Recently visited, Welcome to AWS, and Build a solution.

Create flows the real console runs as full pages are full pages here: Create
bucket (`/ui/s3/create`), Create key pair (`/ui/ec2/key-pairs/create`), Create
policy (`/ui/iam/policies/create`), and the three-step Create role wizard
(`/ui/iam/roles/create`).

## Browser suite

`e2e/` signs the operator in exactly as a deployment does. Microsoft Entra ID,
as `simulator-azure` serves it, is the OpenID Connect issuer;
`e2e/global-setup.ts` registers it as an IAM OpenID Connect identity provider
and creates the role the console assumes with `AssumeRoleWithWebIdentity`. The
suite then drives real create, list, describe, and delete flows through the
console against the simulator, and audits every page with axe-core.

## Embedding

`make embed` in `simulator-aws/` copies this package's `dist/` to
`simulator-aws/dist/` (see `make/go-app.mk`), which the binary bundles via
`//go:embed all:dist` (`simulator-aws/ui_embed.go`) in a `-tags console` build
and serves at `/ui/`. A plain build embeds none and serves the bundle
`SIM_CONSOLE_DIR` names.

## Development

- `bun run dev` — Vite dev server (`:5173`), proxying to a running simulator on `:4566`.
- `bun run build` — production bundle into `dist/`.
- `bun run preview` — serve the built bundle.
- `bun run test` — Vitest unit suite (`src/__tests__/`).
- `bun run test:e2e` — Playwright suite, including axe-core audits in both themes.
- `bun run typecheck` — `tsc --noEmit`.

The package `Makefile` wraps these as `make build` / `run` / `preview` / `test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — workspace commands, embedding, and held dependency versions.
- [`@sockerless/ui-core`](../core/README.md) — shared components, hooks, tokens.
