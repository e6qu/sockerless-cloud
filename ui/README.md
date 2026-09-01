# Sockerless Cloud web interfaces

Bun + Turborepo workspace holding the simulator console SPAs. All packages
share a single design system + API client from `core`; the per-simulator apps
are thin shells over it.

| Package | Purpose |
|---|---|
| `core` | Shared design system, API client, table/query plumbing, e2e harness (`core/e2e/run-tests.sh`). |
| `simulator-aws` | AWS console-style UI served by `simulator-aws` at `/ui/` (Cloudscape design system). |
| `simulator-gcp` | Google Cloud console-style UI served by `simulator-gcp` at `/ui/`. |
| `simulator-azure` | Azure portal-style UI served by `simulator-azure` at `/ui/`. |

The consoles follow the coordinates-only rule (see
[`AGENTS.md`](../AGENTS.md#a-simulator-console-ui-differs-from-a-real-cloud-console-only-in-coordinates)):
they read only real cloud APIs at a configured base URL and federate operator
credentials through the cloud's own federation primitive, so the same build
runs against the simulator and the real cloud.

## Commands

From this directory:

- `bun install` — install workspace deps.
- `bunx turbo run build` — build every package; outputs land in each package's
  `dist/`.
- `bunx turbo run typecheck` / `test` — TypeScript + vitest across the
  workspace.
- `cd packages/simulator-aws && bun run test:e2e` — Playwright browser suite
  (builds the backing simulator binary via the `core/e2e` harness).

To embed a rebuilt console into its simulator binary, from the repo root:

```
make simulator-aws/embed && make simulator-aws/build
```

and commit the regenerated `simulator-aws/dist/` (see
[`AGENTS.md § Committed console dist`](../AGENTS.md#committed-console-dist)).

## Held dependency versions

Three pins are deliberately behind their newest release. Each is held because
the newer version breaks something here, not because nobody looked — so before
running `bun update --latest`, read this and put back whatever it moves.

- **`@fluentui/react-components` — pinned exactly at 9.74.5.** 9.74.6 and
  9.74.7 fail every Azure console test with "The requested module 'tabster'
  does not provide an export named 'createTabster'". tabster 8.8.0 is the
  newest release and its ESM entry does export that symbol, so this is an
  interop break in Fluent's own build rather than a dependency it is missing.
  Bisected: 9.74.4 clean, 9.74.5 clean, 9.74.6 broken. Lift the pin when a
  Fluent release resolves it.
- **`@tanstack/react-table` — held on 8.x.** v9 is an API redesign, not a
  version bump: `createColumnHelper` takes feature generics, and the table is
  constructed through a feature set. Adopting it is a migration of every
  console's tables, which is work to schedule rather than a dependency to take.
- **`typescript` — held on 5.x.** TypeScript 7 rejects the side-effect CSS
  imports every console entry point makes (`TS2882`, "Cannot find module or
  type declarations for side-effect import of './index.css'"). Adopting it
  needs module declarations for those imports first.

Everything else tracks its newest release published more than 24 hours ago,
which is the same adoption quarantine `scripts/check-latest-deps.sh` applies to
the Go, Terraform and GitHub Actions dependencies.
