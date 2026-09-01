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
| `Action TrentService.CreateKey` | ✓ `simulator-aws/kms.go:132::handleKMSCreateKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DescribeKey` | ✓ `simulator-aws/kms.go:133::handleKMSDescribeKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListKeys` | ✓ `simulator-aws/kms.go:134::handleKMSListKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ScheduleKeyDeletion` | ✓ `simulator-aws/kms.go:135::handleKMSScheduleKeyDeletion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Encrypt` | ✓ `simulator-aws/kms.go:136::handleKMSEncrypt` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Decrypt` | ✓ `simulator-aws/kms.go:137::handleKMSDecrypt` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKey` | ✓ `simulator-aws/kms.go:138::handleKMSGenerateDataKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateAlias` | ✓ `simulator-aws/kms.go:139::handleKMSCreateAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeleteAlias` | ✓ `simulator-aws/kms.go:140::handleKMSDeleteAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListAliases` | ✓ `simulator-aws/kms.go:141::handleKMSListAliases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetKeyPolicy` | ✓ `simulator-aws/kms.go:142::handleKMSGetKeyPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.PutKeyPolicy` | ✓ `simulator-aws/kms.go:143::handleKMSPutKeyPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListResourceTags` | ✓ `simulator-aws/kms.go:144::handleKMSListResourceTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.TagResource` | ✓ `simulator-aws/kms.go:145::handleKMSTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UntagResource` | ✓ `simulator-aws/kms.go:146::handleKMSUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetKeyRotationStatus` | ✓ `simulator-aws/kms.go:147::handleKMSGetKeyRotationStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.EnableKeyRotation` | ✓ `simulator-aws/kms.go:148::handleKMSEnableKeyRotation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DisableKeyRotation` | ✓ `simulator-aws/kms.go:149::handleKMSDisableKeyRotation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.EnableKey` | ✓ `simulator-aws/kms.go:151::handleKMSEnableKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DisableKey` | ✓ `simulator-aws/kms.go:152::handleKMSDisableKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CancelKeyDeletion` | ✓ `simulator-aws/kms.go:153::handleKMSCancelKeyDeletion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdateKeyDescription` | ✓ `simulator-aws/kms.go:154::handleKMSUpdateKeyDescription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdateAlias` | ✓ `simulator-aws/kms.go:155::handleKMSUpdateAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateRandom` | ○ `simulator-aws/kms.go:156::handleKMSGenerateRandom` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListKeyPolicies` | ✓ `simulator-aws/kms.go:157::handleKMSListKeyPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListKeyRotations` | ✓ `simulator-aws/kms.go:158::handleKMSListKeyRotations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.RotateKeyOnDemand` | ✓ `simulator-aws/kms.go:159::handleKMSRotateKeyOnDemand` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetParametersForImport` | ✓ `simulator-aws/kms.go:160::handleKMSGetParametersForImport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ImportKeyMaterial` | ✓ `simulator-aws/kms.go:161::handleKMSImportKeyMaterial` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeleteImportedKeyMaterial` | ✓ `simulator-aws/kms.go:162::handleKMSDeleteImportedKeyMaterial` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Sign` | ✓ `simulator-aws/kms_crypto.go:46::handleKMSSign` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.Verify` | ✓ `simulator-aws/kms_crypto.go:47::handleKMSVerify` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetPublicKey` | ✓ `simulator-aws/kms_crypto.go:48::handleKMSGetPublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateMac` | ✓ `simulator-aws/kms_crypto.go:49::handleKMSGenerateMac` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.VerifyMac` | ✓ `simulator-aws/kms_crypto.go:50::handleKMSVerifyMac` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKeyPair` | ✓ `simulator-aws/kms_crypto.go:51::handleKMSGenerateDataKeyPair` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKeyPairWithoutPlaintext` | ✓ `simulator-aws/kms_crypto.go:52::handleKMSGenerateDataKeyPairWithoutPlaintext` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeriveSharedSecret` | ✓ `simulator-aws/kms_crypto.go:53::handleKMSDeriveSharedSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:31::handleKMSCreateCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DescribeCustomKeyStores` | ✓ `simulator-aws/kms_custom_key_stores.go:32::handleKMSDescribeCustomKeyStores` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ConnectCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:33::handleKMSConnectCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DisconnectCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:34::handleKMSDisconnectCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdateCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:35::handleKMSUpdateCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.DeleteCustomKeyStore` | ✓ `simulator-aws/kms_custom_key_stores.go:36::handleKMSDeleteCustomKeyStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.CreateGrant` | ✓ `simulator-aws/kms_grants.go:35::handleKMSCreateGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListGrants` | ✓ `simulator-aws/kms_grants.go:36::handleKMSListGrants` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.RevokeGrant` | ✓ `simulator-aws/kms_grants.go:37::handleKMSRevokeGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GenerateDataKeyWithoutPlaintext` | ✓ `simulator-aws/kms_grants.go:38::handleKMSGenerateDataKeyWithoutPlaintext` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ReEncrypt` | ✓ `simulator-aws/kms_grants.go:39::handleKMSReEncrypt` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.RetireGrant` | ✓ `simulator-aws/kms_multiregion.go:17::handleKMSRetireGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ListRetirableGrants` | ✓ `simulator-aws/kms_multiregion.go:18::handleKMSListRetirableGrants` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.ReplicateKey` | ✓ `simulator-aws/kms_multiregion.go:19::handleKMSReplicateKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.UpdatePrimaryRegion` | ✓ `simulator-aws/kms_multiregion.go:20::handleKMSUpdatePrimaryRegion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TrentService.GetKeyLastUsage` | ✓ `simulator-aws/kms_multiregion.go:21::handleKMSGetKeyLastUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
