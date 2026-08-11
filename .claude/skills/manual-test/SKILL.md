---
name: manual-test
description: Run sockerless's canonical manual smoke per component — real adaptor, real binary, real output. Use when verifying a fix, before claiming "this works", or when sample-capturing for docs. Pairs with adaptor-fidelity-check.
---

# Manual test

Manual tests in sockerless drive the real reference adaptor against a real running binary and assert specific output. They are the ground truth — `go test ./...` proves compile + unit correctness; manual tests prove the **wire contract**.

## When this skill applies

- Before claiming a fix works ("I changed X" → "I ran Y and got Z").
- Before capturing sample output for a README per Phase 157.
- After context compaction, before continuing a multi-turn change.
- When CI surfaces a wire-level bug (the manual flow reproduces it locally).

## Discipline

- **No mocks.** Real binary, real adaptor.
- **Real captured output.** Paste exactly what the terminal showed; never paraphrase.
- **Round-trip per change.** If you edited the response handler, you must re-run the adaptor and see the new response.
- **Cleanup at the end.** `pkill`, `docker rm`, `gh repo delete`, etc.

## Per-component recipes

### `backends/docker`

```bash
cd backends/docker && make build
./sockerless-backend-docker -addr :13375 --log-level error > /tmp/db.log 2>&1 &
sleep 1

# Round-trip
DOCKER_HOST=tcp://localhost:13375 docker version --format '{{.Client.Version}} client / {{.Server.Version}} server'
DOCKER_HOST=tcp://localhost:13375 docker run --rm alpine:3.20 echo "hello"
curl -sS http://localhost:13375/_ping

# Cleanup
pkill -f sockerless-backend-docker
```

### `backends/{ecs,lambda,cloudrun,cloudrun-functions,aca,azure-functions}`

```bash
# 1. Bring up sim + backend + admin together via the canonical stack target.
#    Pick the cell that matches the backend under test:
make stack-aws-ecs           # sim-aws + backend-ecs + admin
make stack-aws-lambda        # sim-aws + backend-lambda + admin
make stack-gcp-cloudrun      # sim-gcp + backend-cloudrun + admin
make stack-gcp-gcf           # sim-gcp + backend-gcf + admin
make stack-azure-aca         # sim-azure + backend-aca + admin
make stack-azure-azf         # sim-azure + backend-azf + admin

# 2. Drive via docker (the frontend adaptor)
DOCKER_HOST=tcp://localhost:3375 docker run --rm alpine echo hi

# 3. (also) drive via the cloud adaptor against the SIMULATOR to confirm the
#    cloud side of the action was reproduced fully
aws --endpoint-url http://localhost:4566 ecs list-tasks --cluster <cluster>
gcloud --api-endpoint-overrides http://localhost:4567 run jobs list
az --endpoint http://localhost:4568 containerapp list

# 4. Cleanup
make stack-down
```

### `simulator-{aws,gcp,azure}`

```bash
# 1. Start
cd simulator-<cloud> && make build && ./sockerless-simulator-<cloud> --addr :<port> &

# 2. Hit it with all three adaptor types
aws --endpoint-url http://localhost:<port> <service> <verb>
# (or gcloud / az equivalents)
# Then via SDK in a one-off Go program; then via Terraform provider with endpoint override.

# 3. Cleanup
pkill -f sockerless-simulator-<cloud>
```

### Bleephub consumer integration

Run Bleephub's documented real-client harness from its standalone repository. Its
`runner-sockerless-test` target builds the same Sockerless simulators and backend
binaries a consumer uses in production-style integration testing.

### Full e2e (sim + backend + runner)

```bash
make stack-aws-ecs           # sim + backend + admin (add bleephub: make stack-bleephub-up)
make e2e-github-ecs          # official actions/runner end-to-end against the sim-mode ECS backend
make stack-down
```

## What "real captured output" looks like

```
$ DOCKER_HOST=tcp://localhost:13375 docker run --rm alpine:3.20 echo hello
hello

$ gh repo create demo --public
✓ Created repository admin/demo on GitHub
```

vs. paraphrased / made-up:

```
✗ "docker run completes successfully and prints the message"
✗ "gh repo create returns the created repo URL"
```

If you cannot show the literal terminal output, you have not actually tested it.

## When to file a bug

If any manual test fails, before fixing:

1. Capture the failing command + output verbatim.
2. Add a one-liner to `BUGS.md` under Open (next sequential BUG-NNN, severity P0–P3).
3. Include the fix shape (one sentence on where + how) once understood.
4. Then fix it in the same session (per `memory/feedback_manual_test_cycle.md`).

## Output

When this skill fires, name the component + adaptor you're about to drive, run the recipe, and paste the actual terminal output (or note what failed). End with cleanup status ("backend killed", "sim down", "repo deleted").
