# Sim surface — aws-acm_acme

Surface registered in `simulator-aws/acm_acme.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action CertificateManager.CreateAcmeEndpoint` | ✓ `simulator-aws/acm_acme.go:176::handleACMCreateAcmeEndpoint` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DeleteAcmeEndpoint` | ✓ `simulator-aws/acm_acme.go:177::handleACMDeleteAcmeEndpoint` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DescribeAcmeEndpoint` | ✓ `simulator-aws/acm_acme.go:178::handleACMDescribeAcmeEndpoint` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.ListAcmeEndpoints` | ✓ `simulator-aws/acm_acme.go:179::handleACMListAcmeEndpoints` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.UpdateAcmeEndpoint` | ✓ `simulator-aws/acm_acme.go:180::handleACMUpdateAcmeEndpoint` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.CreateAcmeDomainValidation` | ✓ `simulator-aws/acm_acme.go:181::handleACMCreateAcmeDomainValidation` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DeleteAcmeDomainValidation` | ✓ `simulator-aws/acm_acme.go:182::handleACMDeleteAcmeDomainValidation` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DescribeAcmeDomainValidation` | ✓ `simulator-aws/acm_acme.go:183::handleACMDescribeAcmeDomainValidation` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.ListAcmeDomainValidations` | ✓ `simulator-aws/acm_acme.go:184::handleACMListAcmeDomainValidations` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.UpdateAcmeDomainValidation` | ✓ `simulator-aws/acm_acme.go:185::handleACMUpdateAcmeDomainValidation` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.CreateAcmeExternalAccountBinding` | ✓ `simulator-aws/acm_acme.go:186::handleACMCreateAcmeExternalAccountBinding` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DeleteAcmeExternalAccountBinding` | ✓ `simulator-aws/acm_acme.go:187::handleACMDeleteAcmeExternalAccountBinding` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DescribeAcmeExternalAccountBinding` | ✓ `simulator-aws/acm_acme.go:188::handleACMDescribeAcmeExternalAccountBinding` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.GetAcmeExternalAccountBindingCredentials` | ✓ `simulator-aws/acm_acme.go:189::handleACMGetAcmeExternalAccountBindingCredentials` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.ListAcmeExternalAccountBindings` | ✓ `simulator-aws/acm_acme.go:190::handleACMListAcmeExternalAccountBindings` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.RevokeAcmeExternalAccountBinding` | ✓ `simulator-aws/acm_acme.go:191::handleACMRevokeAcmeExternalAccountBinding` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.DescribeAcmeAccount` | ✓ `simulator-aws/acm_acme.go:192::handleACMDescribeAcmeAccount` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.ListAcmeAccounts` | ✓ `simulator-aws/acm_acme.go:193::handleACMListAcmeAccounts` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action CertificateManager.RevokeAcmeAccount` | ✓ `simulator-aws/acm_acme.go:194::handleACMRevokeAcmeAccount` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /acme/{endpoint}/directory` | ✓ `simulator-aws/acm_acme.go:196::handleACMEDataPlane` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `HEAD /acme/{endpoint}/new-nonce` | ✓ `simulator-aws/acm_acme.go:197::handleACMEDataPlane` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /acme/{endpoint}/new-nonce` | ✓ `simulator-aws/acm_acme.go:198::handleACMEDataPlane` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /acme/{endpoint}/{resource}` | ✓ `simulator-aws/acm_acme.go:199::handleACMEDataPlane` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /acme/{endpoint}/{resource}/{id}` | ✓ `simulator-aws/acm_acme.go:200::handleACMEDataPlane` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
