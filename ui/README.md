# Sockerless Cloud web interfaces

Bun + Turborepo workspace holding the simulator console SPAs. The consoles
share design tokens, the theme hook, the error boundary and the browser-suite
harness from `core`; each reads its cloud's real APIs through its own client.

| Package | Purpose |
|---|---|
| `core` | Design tokens, `useTheme`, `ErrorBoundary`, e2e harness (`core/e2e/run-tests.sh`). |
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
  workspace. The AWS and Google Cloud console suites need Go: their
  `vitest.global-setup.ts` builds the console's simulator and `simulator-azure`
  from this checkout, starts them API-only on free ports serving the console's
  `dist/` with its operator federated as the Playwright suite does, and the
  tests sign in and drive the console against them.
- `cd packages/simulator-aws && bun run test:e2e` (or `simulator-gcp`) —
  Playwright browser suite (builds the backing simulator binary via the
  `core/e2e` harness, plus `simulator-azure`, whose Microsoft Entra ID signs the
  console's operator in; the AWS console federates it with
  AssumeRoleWithWebIdentity, the Google Cloud console with Workforce Identity
  Federation). `SERVER_BIN` and `SIMULATOR_BIN` name prebuilt binaries instead.
  On Linux the harness runs Chromium in a network namespace of its own
  (`core/e2e/isolated-chromium.sh`), because Chromium aborts in-flight requests
  with `ERR_NETWORK_CHANGED` on every address change of the host's interfaces,
  and the simulators' workloads add and remove engine bridges throughout the
  run. That needs `pasta` (`scripts/install-passt.sh` installs the pinned
  package) and unprivileged user namespaces; on Ubuntu 24.04 run
  `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`.

To embed a rebuilt console into its simulator binary, from the repo root:

```
make simulator-aws/embed && make simulator-aws/build
```

The bundle is a build output and stays out of git (see
[`AGENTS.md § No built artifacts in git`](../AGENTS.md#no-built-artifacts-in-git)).

## Dependency versions

Every dependency tracks its newest release published more than 24 hours ago,
which is the same adoption quarantine `scripts/check-latest-deps.sh` applies to
the Go, Terraform and GitHub Actions dependencies.
