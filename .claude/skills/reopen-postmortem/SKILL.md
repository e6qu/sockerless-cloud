---
name: reopen-postmortem
description: Every reopened community-filed issue triggers a structured postmortem that goes into the BUG entry. The postmortem captures (a) what test passed but should have failed, (b) what SDK / CLI / provider code path the test missed, (c) what new canonical-client test catches the regression. Distilled from BUG-1146 — the pattern across #190 (reopened once) and #193 (reopened once) where "fixed" was claimed against a partial test that bypassed the real client contract, and the user found the gap the moment they ran a real SDK against the merged build.
---

# Reopen postmortem invariant

## The class of bug

When a community-filed issue is reopened against sockerless, the cause is almost always one of:

1. **The fix matched the symptom, not the contract.** The user reported what they saw (a 409 / a panic / a wrong-shape response); the fix made the symptom go away in a narrow test, but the real-cloud client exercises a broader contract that still breaks.
2. **The test that verified the fix bypassed the part of the client that bites.** Raw `net/http` + pre-set `Authorization: Bearer fake-token` skipped the SDK's challenge-then-retry parser (BUG-1135 → BUG-1143). Constructing the resource via ARM before exercising the data plane masked the "no prior registration" path (BUG-1130 → BUG-1134).
3. **Partial coverage was declared total.** The user named operation X of a closed table; the fix landed X; the table's other 14 rows kept the bug shape and got re-filed (BUG-1138 → BUG-1142 via issue #201). The `surface-table-completeness` skill catches this *before* declaring done; this skill catches what `surface-table-completeness` itself missed.

Every reopen erodes the "every gap is a real bug with a real fix" invariant. Saying "fixed" twice for the same shape costs trust. The remediation is to make sure the *next* fix doesn't repeat the previous fix's blind spot — which requires writing the blind spot down.

## When this skill applies

Apply it when ANY of these are true:

1. A community-filed GitHub issue is reopened after a previous "fixed" claim. The reopen comment usually quotes a previous merge commit or PR.
2. A new GitHub issue is filed against a surface that an earlier issue claimed to fix (adjacent reopen — different issue ID, same shape).
3. A BUG entry in BUGS.md is converted from `~~CLOSED~~` back to Open after the user reports the fix doesn't hold.
4. The current PR includes a "BUG-NNNN reopened" line in the commit messages.

Do **not** apply this skill to first-time issues, scope-expansion issues (different surface than the original), or process / skill / continuity-doc improvements.

## The rule

**Every reopen BUG entry in BUGS.md must contain three additional fields beyond the standard one-liner:**

1. **What test passed but should have failed?** Quote the test name + file:line. Explain why it didn't catch the regression — what was it actually asserting?
2. **What client code path did the test miss?** Reference the specific function / parser / policy in the SDK / CLI / provider that the original test bypassed.
3. **What new test (using the canonical client) catches the regression?** This is the load-bearing requirement — the fix isn't complete until this test exists, fails on the pre-fix build, and passes on the post-fix build.

These three fields go in the BUG one-liner column itself, prefixed with `Postmortem:`. They are NOT footnotes or optional context — without them, the reopen is unsupported and the next reopen is foreseeable.

## How to apply

### When filing a reopen BUG

1. Identify the previous BUG that claimed to fix the issue (e.g., BUG-1135 → BUG-1143 chain).
2. Read the previous BUG's commit + the test file it added.
3. Run the test under the *new* canonical client (the one the user's reopen used). If it doesn't fail, the test is not the regression guard — find one that does.
4. Write the three postmortem fields into the new BUG one-liner.
5. **Don't start coding the fix yet.** Confirm the new test fails on the pre-fix build; *that* is the regression guard. Then start the fix; the test passes when the fix is right.

### When closing a reopen BUG

The strikethrough form mirrors the original BUG style but explicitly references the original BUG and the postmortem fields:

```markdown
| ~~1143~~ | ~~P0~~ | ~~Azure KV WWW-Authenticate authorization URL breaks Azure SDK parser (issue #193 reopened — postmortem of BUG-1135)~~ — **FIXED** in Phase 177 ... | 36 |
```

The "postmortem of BUG-NNNN" suffix preserves the chain so future readers can find the prior fix that didn't hold.

### When auditing a PR that touches a reopen

For each commit that says "BUG-NNNN reopened" or references a strikethrough BUG:

1. Find the new canonical-client test added in the PR. If none, the fix is incomplete.
2. Confirm the new test would have failed on the pre-fix build. The simplest verification: `git stash` the fix, run the test, see it fail; `git stash pop` to restore the fix.
3. Verify the BUG entry carries the three postmortem fields. If not, file a sub-finding before merge.

## Refused shortcuts

- *"The fix is small, the test from the previous fix is enough."* No — the previous test is what missed the regression. If you don't write a new test that catches the new gap, the next reopen is just a matter of time.
- *"The reopen is just user confusion / a different shape."* If it's a different shape, file as a new BUG, not a reopen. If it IS the same shape, the postmortem is required.
- *"The pre-fix build is gone, we can't verify the test fails on it."* You can. Either `git checkout <prev-commit>` or temporarily revert the fix in your working tree. The verification is cheap and the alternative (shipping another half-fix) is expensive.

## Companion skills

- `surface-table-completeness` — applies when the reopen surfaces a closed operation table that was partially covered. The postmortem references which row(s) of which table were missing.
- `sim-canonical-config-test` — most reopens stem from non-canonical test config. The postmortem cites the specific bypass (raw `net/http`, `Bearer fake-token`, `BaseEndpoint = baseURL + "/<svc>"`).
- `backpedal-pattern-audit` — meta-tracker for recurring reopens. If a reopen chain reaches 3 hops (1135 → 1143 → 1153?), file a class-of-bug rule.

## Worked example — BUG-1143 (KV WWW-Authenticate URL)

**Postmortem fields filled out:**

1. *What test passed but should have failed?* `simulator-azure/sdk-tests/blob_keys_certs_test.go::TestKeyVault_AuthChallenge` exercised the WWW-Authenticate response shape with raw `net/http`. The test asserted the response carried `Bearer authorization=` and `resource=` keys, but did NOT exercise the SDK parser that consumes those values. It passed against PR #200's 3-segment URL because nothing read it.
2. *What client code path did the test miss?* `github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal.parseTenant` at `challenge_policy.go:104` — `parts := strings.Split(url, "/"); tenant := parts[3]`. The function panics on a URL with < 4 path segments. None of PR #200's tests called any code that walked through this function.
3. *What new test catches the regression?* `simulator-azure/sdk-tests/keyvault_sdk_test.go::TestKeyVault_SDK_Secrets_ChallengeRoundTrip` (plus the Keys and Certificates siblings). Each test constructs an `azsecrets.NewClient` with a canonical `azcore.TokenCredential` and exercises SetSecret → GetSecret → DeleteSecret. The challenge policy fires on the first call; `parseTenant` runs on the response; pre-fix the test panics with `index out of range [3] with length 3`; post-fix the test extracts the zero-UUID tenant and passes.

The BUG-1143 row in BUGS.md carries this postmortem inline. The next reopen — should one happen — must do the same.
