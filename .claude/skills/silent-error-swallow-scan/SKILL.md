---
name: silent-error-swallow-scan
description: Specialist scan for the silent-error-swallow pattern that has produced BUGS 1016 / 1017 / 1018 / 1019 / 1025 / 1033 — code that ignores errors from json.Unmarshal, io.Copy, json.NewDecoder.Decode, or pktline encoder writes and continues with assumptions about success. Codifies the project's "no fallbacks / no silent shims" rule as an automated grep + classification pass. Use before merging any Go change and periodically across the codebase.
---

# Silent-error-swallow scan

The project rule is "no fakes / no fallbacks / no silent shims" — but six bug IDs in two phases (BUGS 1016, 1017, 1018, 1019, 1025, 1033) have come from the same shape: a Go call that returns an error, the error is dropped with `_ = ...` or `if err != nil { /* fallback */ }`, and downstream code continues as if the call succeeded. That recurrence is itself the signal that a per-PR specialist scan is worth running.

This skill is that scan.

## When this skill applies

- Before merging any Go change — final pre-PR scan.
- Periodically across the whole `simulator-` + `backends/` + `agent/` tree.
- When a bug surfaces with the shape "the sim accepted my request, returned 200, but didn't actually persist X" — this is almost always a silent decode.
- When auditing a service for the "no fallbacks" rule.

Skip for: pure-data fixtures, generated code (`*_gen.go`), vendor / `go.work.sum`, `_test.go` files where the swallow is deliberate (e.g., cleanup `defer cleanup()` after a test asserted on the main path).

## The patterns to scan

### Pattern A — silent unmarshal / decode

The most-common shape in the BUGS catalogue. The function returns an error; we drop it; downstream assumes the destination struct is fully populated.

```bash
# Bare _ assignment on json.Unmarshal / Decode
rg -nC1 '_\s*=\s*json\.(Unmarshal|NewDecoder)' \
  --type go \
  -g '!*_test.go' -g '!*_gen.go' -g '!vendor/'

# Also matches yaml.Unmarshal, toml.Unmarshal, xml.Unmarshal
rg -nC1 '_\s*=\s*(yaml|toml|xml)\.(Unmarshal|NewDecoder)' \
  --type go \
  -g '!*_test.go' -g '!*_gen.go' -g '!vendor/'

# Decoder constructed and immediately Decode-d with dropped error
rg -nC2 'NewDecoder\([^)]+\)\.Decode\(' --type go \
  -g '!*_test.go' \
  | rg -B1 -A1 '_\s*='
```

### Pattern A2 — silent two-value reads (`data, _ := io.ReadAll(...)`)

Same shape as A but in the **two-value-with-`_`-in-position-2** form: `data, _ := io.ReadAll(r.Body)` followed within a few lines by `_ = xml.Unmarshal(data, &req)` (or json / yaml / toml) is a two-stacked silent error chain. Pattern A's grep catches the second line but misses the first; this pattern catches the read side directly.

```bash
# Two-value reads with discarded error position-2 — io.ReadAll, io.Copy, ioutil.ReadAll
rg -nC1 ',\s*_\s*:?=\s*io\.(ReadAll|Copy)\(' --type go \
  -g '!*_test.go' -g '!*_gen.go' -g '!vendor/'

# Anywhere `io.ReadAll(r.Body)` lives in a Go handler — flag for review unless
# the body is documented as fixed-shape JSON serialised with Content-Length.
rg -n 'io\.ReadAll\(r\.Body\)' --type go -g '!*_test.go' -g '!vendor/'
```

If `r.Body` ReadAll is followed within ±5 lines by a `_ = json/xml/yaml/toml.Unmarshal(...)`, that's a stacked-swallow chain. Both errors must be checked, or the handler must justify why a malformed wire-byte stream is acceptable.

### Pattern B — silent base64 / hex / url-decode

Same shape, different codec. The historical bug-fix sweep for `SOCKERLESS_LABELS` (BUG-1019) was this.

```bash
rg -nC1 '_\s*=\s*(base64|hex)\.[A-Z]\w*Decode' --type go -g '!*_test.go'
rg -nC1 '_\s*=\s*url\.(Parse|QueryUnescape|PathUnescape)' --type go -g '!*_test.go'
```

### Pattern C — silent io.Copy

BUGS 1025 + 1033 hit this. `io.Copy(w, rc)` returning an error in the response stream typically means client disconnect (recoverable, log at Debug) or simulator-side I/O failure (log at Error). Either way, not silent.

```bash
rg -nC1 '_\s*,\s*_\s*=\s*io\.Copy' --type go -g '!*_test.go'
# Also the bare-stmt form:
rg -n 'io\.Copy\([^)]+\)$' --type go -g '!*_test.go' | rg -v '_, err :='
```

### Pattern D — explicit `_ = err` after a function returning error

```bash
# err assigned via :=, then dropped on the next line
rg -nC3 ':=\s*\w[\w.]*\(' --type go -g '!*_test.go' \
  | grep -A2 'err :=' | grep -B1 '_\s*=\s*err'
```

### Pattern E — explicit `if err != nil { /* fallback */ }`

The "fallback" shape that has produced multiple `feedback_no_fallbacks` corrections.

```bash
# if err != nil block whose body is a comment + nothing (or fallback assignment)
rg -nC2 'if err != nil \{' --type go -g '!*_test.go' | rg -B1 -A1 'fallback|best.effort|fire.and.forget|silently|degrade'
```

### Pattern F — pktline / git encoder writes

BUG-1025 hit this specifically; bleephub's smart-HTTP advertise path silently swallowed `pktline.Encoder.Encodef` + `Flush` errors.

```bash
rg -nC1 '_\s*=\s*\w*pktline\w*\.' --type go -g '!*_test.go'
rg -nC1 'pktline\.[A-Z]\w*\([^)]+\)$' --type go -g '!*_test.go'
```

## How to classify each finding

For every hit the scan produces, classify as one of:

1. **Bug (file it).** The swallow assumes downstream success without checking. Examples:
   - JSON request-body decode where the empty struct produces a misleading downstream error.
   - `io.Copy` where mid-stream failure means partial corrupt data on the wire.
   - Base64 decode of a backend-↔-sim contract value (per BUG-1019, malformed input must surface, not silently produce empty).
2. **Legitimate (annotate it).** The error is genuinely safe to swallow. Examples:
   - Connection-closed write after response headers committed (`io.Copy` to a hijacked conn).
   - Empty-body `Decode` where `io.EOF` means "no body, use defaults" — accept `errors.Is(err, io.EOF)` only.
   - Cleanup paths inside a deferred function on an already-failed test.
   For these, the fix is a 1-line comment explaining why the swallow is OK, not an `_ = err`.
3. **Test code.** Skip unless the test asserts on a downstream effect that depends on the swallowed call succeeding.

## How to fix

The default fix shape, applied to all three of BUG-1018 / 1019 / 1033:

```go
// Before
_ = json.Unmarshal(body, &req)

// After
if err := json.Unmarshal(body, &req); err != nil {
    sim.<ServiceError>(w, "InvalidRequest", "Malformed body: " + err.Error(), http.StatusBadRequest)
    return
}
```

For `io.Copy` after a hijacked connection or after response headers committed (BUG-1033 shape):

```go
// Before
io.Copy(w, rc)

// After
if _, err := io.Copy(w, rc); err != nil {
    s.Logger.Debug().Err(err).Msg("<op> stream copy failed — client likely disconnected")
}
```

`Debug` (not `Error`) because the failure isn't recoverable but is also not catastrophic; the client is gone.

## Known prior occurrences this skill replays

- **BUG-1016** — bleephub write handlers (`handleOIDCCustomSubPut`, `handlePagesCreate`, `handleBranchProtectionPut`, `handleLockIssue`) swallowed malformed JSON, returned 201/200/204.
- **BUG-1017** — Five `_ = json.Unmarshal` sites across AWS + GCP sims.
- **BUG-1018** — `handleExecStart` + `handleLibpodContainerCreate` swallowed request-decode errors before hijacking.
- **BUG-1019** — Cloud Functions backend decoded `SOCKERLESS_LABELS` env var with silent base64 + JSON errors, falling through to a legacy fallback that produced ghost containers.
- **BUG-1025** — bleephub smart-HTTP advertise path swallowed pktline encoder errors at three sites.
- **BUG-1033** — Five `io.Copy` calls in image-streaming + build response paths swallowed mid-stream copy errors.
- **BUG-1105** (Phase 174 round 1) — 23 silent `_ = sim.ReadJSON(r, &req)` sites across Phase 173 handlers (apim.go, servicebus.go, postgres_flexible.go, pubsub.go, memorystore_redis.go, apigateway.go, sqladmin.go, sqs.go).
- **BUG-1106** — silent `_ = json.NewDecoder(r.Body).Decode(&body)` in `handleKVCreateCertificate`.
- **(Phase 175)** — silent stacked `data, _ := io.ReadAll(r.Body)` + `_ = xml.Unmarshal(data, &req)` in `simulator-azure/storage_dataplane.go:505-507` — exact Pattern A2 shape introduced in Phase 174 round 2 (same PR that added the xml handler).
- **(Phase 175)** — silent `_ = json.Unmarshal(decoded, &entrypoint)` in `simulator-azure/functions.go:666` + `:670` after base64 decode of `SOCKERLESS_ENTRYPOINT`/`SOCKERLESS_CMD` — BUG-1019 replay on a different env-var pair.

Eight+ bugs across four phases — the recurrence rate justifies the dedicated scan. **Re-run the scan after every PR's own changes**, not just before — the BUG-1104 meta-shape is that helpers written in a PR get bypassed elsewhere in the same PR.

## Related skills

- `avoid-vibe-slop` — broader project-local checklist; this skill is one specialist of its "no fakes / no fallbacks" rule.
- `dead-code-silencer-scan` — sister skill for the unused-import silencer pattern.
- `backpedal-pattern-audit` — meta-skill that surfaces patterns like this one from BUGS.md.
