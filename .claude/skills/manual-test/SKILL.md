---
name: manual-test
description: Run a simulator's canonical manual smoke — real adaptor, real binary, real output. Use when verifying a fix, before claiming "this works", or when capturing sample output for docs. Pairs with adaptor-fidelity-check.
---

# Manual test

A manual test drives the real reference adaptor against a real running
simulator binary and asserts specific output. `go test ./...` proves compile
and unit correctness; a manual test proves the **wire contract**.

## When this skill applies

- Before claiming a fix works ("I changed X" → "I ran Y and got Z").
- Before capturing sample output for a README.
- After context compaction, before continuing a multi-turn change.
- When CI surfaces a wire-level bug (the manual flow reproduces it locally).

## Discipline

- **No mocks.** Real binary, real adaptor.
- **Real captured output.** Paste exactly what the terminal showed; never paraphrase.
- **Round-trip per change.** If you edited the response handler, re-run the adaptor and see the new response.
- **Clean up at the end.** Stop the simulator; remove what the run created.

## Recipes

### `simulator-{aws,gcp,azure}`

```bash
# 1. Build and start (default ports: aws :4566, gcp :4567 plus gRPC :4569, azure :4568).
#    SIM_RUNTIME=process serves the API without a container engine; omit it
#    when the test starts workloads.
make simulator-aws/build
SIM_LISTEN_ADDR=:4566 ./simulator-aws/simulator-aws &

# 2. Drive it with each adaptor type.
AWS_ENDPOINT_URL=http://localhost:4566 AWS_ACCESS_KEY_ID=test \
  AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 \
  aws ecs list-clusters
# gcloud: CLOUDSDK_API_ENDPOINT_OVERRIDES_<API>=http://localhost:4567/
# az:     az rest --url "http://localhost:4568/subscriptions/...?api-version=..."
# SDK:    a one-off Go program with the endpoint as its only coordinate.
# Terraform: the provider block from simulator-<cloud>/docs/terraform.md.

# 3. Clean up.
pkill -f 'simulator-aws/simulator-aws'
```

Each simulator's README carries its full wiring, and its `docs/cli.md`,
`docs/terraform.md` and `docs/python-sdk.md` the per-adaptor setup.

### Through the local HTTPS gateway

For clients that insist on HTTPS (the azurerm provider among them), put Caddy
in front with the repository's configuration:

```bash
caddy run --config make/https-gateway/Caddyfile --adapter caddyfile &
# then address https://aws.sockerless.localhost:8443 (or gcp / azure),
# trusting Caddy's local root CA through SSL_CERT_FILE.
```

### Inside the shared Linux harness

On macOS, or whenever the host lacks the kernel capabilities a real-execution
path needs, run a simulator's suites inside `Dockerfile.test`:

```bash
make simulator-azure/docker-test
```

## What "real captured output" looks like

```
$ AWS_ENDPOINT_URL=http://localhost:4566 aws sts get-caller-identity --query Account --output text
000000000000
```

versus paraphrased or made up:

```
✗ "get-caller-identity returns the account"
```

If you cannot show the literal terminal output, you have not tested it.

## When to file a bug

If a manual test fails:

1. Capture the failing command and its output verbatim.
2. Fix it in the same change, or add a row to `BUGS.md` under Open (the next
   free BUG number, severity P0–P3) with the area, the symptom and cause, and
   the fix shape.

## Output

When this skill fires, name the simulator and adaptor you're about to drive,
run the recipe, and paste the actual terminal output (or note what failed).
End with the cleanup status ("simulator stopped").
