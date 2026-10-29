# Sim surface — aws-kms

Surface registered in `simulator-aws/kms.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did. It does not follow that the answer is built from what it read: a handler that looks its parent up and then answers a fixed body reaches state and is marked ✓
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action TrentService.ListKeys` | ✓ `simulator-aws/kms.go:100::handleKMSListKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ScheduleKeyDeletion` | ✓ `simulator-aws/kms.go:101::handleKMSScheduleKeyDeletion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Encrypt` | ✓ `simulator-aws/kms.go:102::handleKMSEncrypt` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Decrypt` | ✓ `simulator-aws/kms.go:103::handleKMSDecrypt` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKey` | ✓ `simulator-aws/kms.go:104::handleKMSGenerateDataKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateAlias` | ✓ `simulator-aws/kms.go:105::handleKMSCreateAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeleteAlias` | ✓ `simulator-aws/kms.go:106::handleKMSDeleteAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListAliases` | ✓ `simulator-aws/kms.go:107::handleKMSListAliases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetKeyPolicy` | ✓ `simulator-aws/kms.go:108::handleKMSGetKeyPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.PutKeyPolicy` | ✓ `simulator-aws/kms.go:109::handleKMSPutKeyPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListResourceTags` | ✓ `simulator-aws/kms.go:110::handleKMSListResourceTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.TagResource` | ✓ `simulator-aws/kms.go:111::handleKMSTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UntagResource` | ✓ `simulator-aws/kms.go:112::handleKMSUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetKeyRotationStatus` | ✓ `simulator-aws/kms.go:113::handleKMSGetKeyRotationStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.EnableKeyRotation` | ✓ `simulator-aws/kms.go:114::handleKMSEnableKeyRotation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DisableKeyRotation` | ✓ `simulator-aws/kms.go:115::handleKMSDisableKeyRotation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.EnableKey` | ✓ `simulator-aws/kms.go:117::handleKMSEnableKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DisableKey` | ✓ `simulator-aws/kms.go:118::handleKMSDisableKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CancelKeyDeletion` | ✓ `simulator-aws/kms.go:119::handleKMSCancelKeyDeletion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdateKeyDescription` | ✓ `simulator-aws/kms.go:120::handleKMSUpdateKeyDescription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdateAlias` | ✓ `simulator-aws/kms.go:121::handleKMSUpdateAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateRandom` | ○ `simulator-aws/kms.go:122::handleKMSGenerateRandom` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListKeyPolicies` | ✓ `simulator-aws/kms.go:123::handleKMSListKeyPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListKeyRotations` | ✓ `simulator-aws/kms.go:124::handleKMSListKeyRotations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.RotateKeyOnDemand` | ✓ `simulator-aws/kms.go:125::handleKMSRotateKeyOnDemand` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetParametersForImport` | ✓ `simulator-aws/kms.go:126::handleKMSGetParametersForImport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ImportKeyMaterial` | ✓ `simulator-aws/kms.go:127::handleKMSImportKeyMaterial` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeleteImportedKeyMaterial` | ✓ `simulator-aws/kms.go:128::handleKMSDeleteImportedKeyMaterial` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateKey` | ✓ `simulator-aws/kms.go:98::handleKMSCreateKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DescribeKey` | ✓ `simulator-aws/kms.go:99::handleKMSDescribeKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Sign` | ✓ `simulator-aws/kms_crypto.go:36::handleKMSSign` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Verify` | ✓ `simulator-aws/kms_crypto.go:37::handleKMSVerify` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetPublicKey` | ✓ `simulator-aws/kms_crypto.go:38::handleKMSGetPublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateMac` | ✓ `simulator-aws/kms_crypto.go:39::handleKMSGenerateMac` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.VerifyMac` | ✓ `simulator-aws/kms_crypto.go:40::handleKMSVerifyMac` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKeyPair` | ✓ `simulator-aws/kms_crypto.go:41::handleKMSGenerateDataKeyPair` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKeyPairWithoutPlaintext` | ✓ `simulator-aws/kms_crypto.go:42::handleKMSGenerateDataKeyPairWithoutPlaintext` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeriveSharedSecret` | ✓ `simulator-aws/kms_crypto.go:43::handleKMSDeriveSharedSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:27::handleKMSCreateCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DescribeCustomKeyStores` | ✓ `simulator-aws/kms_custom_key_stores.go:28::handleKMSDescribeCustomKeyStores` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ConnectCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:29::handleKMSConnectCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DisconnectCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:30::handleKMSDisconnectCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdateCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:31::handleKMSUpdateCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeleteCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:32::handleKMSDeleteCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateGrant` | ✓ `simulator-aws/kms_grants.go:28::handleKMSCreateGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListGrants` | ✓ `simulator-aws/kms_grants.go:29::handleKMSListGrants` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.RevokeGrant` | ✓ `simulator-aws/kms_grants.go:30::handleKMSRevokeGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKeyWithoutPlaintext` | ✓ `simulator-aws/kms_grants.go:31::handleKMSGenerateDataKeyWithoutPlaintext` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ReEncrypt` | ✓ `simulator-aws/kms_grants.go:32::handleKMSReEncrypt` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Encapsulate` | ✓ `simulator-aws/kms_mlkem.go:20::handleKMSEncapsulate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Decapsulate` | ✓ `simulator-aws/kms_mlkem.go:21::handleKMSDecapsulate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.RetireGrant` | ✓ `simulator-aws/kms_multiregion.go:11::handleKMSRetireGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListRetirableGrants` | ✓ `simulator-aws/kms_multiregion.go:12::handleKMSListRetirableGrants` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ReplicateKey` | ✓ `simulator-aws/kms_multiregion.go:13::handleKMSReplicateKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdatePrimaryRegion` | ✓ `simulator-aws/kms_multiregion.go:14::handleKMSUpdatePrimaryRegion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetKeyLastUsage` | ✓ `simulator-aws/kms_multiregion.go:15::handleKMSGetKeyLastUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
