# BUGS

Open: 2. Resolved: 0.

## Open

- **BUG-2 (skip-if-absent, Cosmos DB differential):** The Azure Cosmos DB
  differential tests in `simulator-azure/sdk-tests/` carry two grandfathered
  tool-absent skips (`t.Skipf` when the emulator's advertised host port is
  occupied, and when the emulator container fails to start). Both predate the
  no-tool-absent-skips hook and violate the no-skip rule: the emulator is
  either required (provision it in TestMain and fail loud, resolving the port
  conflict for real) or the differential should be restructured. Fix shape:
  make the harness own emulator startup end-to-end and `log.Fatalf` on
  failure.

- **BUG-1 (deadcode coverage gap, shared/):** The per-cloud `shared/`
  framework packages joined their simulator modules during the extraction
  from the sockerless monorepo, which put them inside `deadcode`'s
  same-module reporting set for the first time. Each diverged copy retains
  cross-cloud helpers its own cloud never calls (`GCPError` in
  `simulator-aws/shared`, `AWSError` in `simulator-gcp/shared`, ~24-31
  findings per cloud under `deadcode -tags noui -test`).
  `scripts/simulators-deadcode.sh` currently excludes `shared/` findings to
  preserve the gate's historical scope. Fix shape: on a Linux host (GOOS and
  build tags such as `realexec_host` change reachability), delete the
  genuinely dead helpers per cloud — or re-converge the three copies — then
  drop the `shared/` exclusion from the script.

## Resolved history

(none yet)
