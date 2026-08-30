# Azure Key Vault — data plane

Surface: `simulator-azure/keyvault.go` (data plane). All operations dispatch via the `<vault>.vault.<sim-host>` subdomain wrapper. Every authenticated request goes through the `WWW-Authenticate: Bearer` challenge-then-retry handshake; the authorization URL emitted by the sim **must** have ≥ 4 path-split segments because the Azure SDK indexes `parts[3]` without a bounds check (issue #193 → BUG-1135 → BUG-1143).

Canonical reference: <https://learn.microsoft.com/en-us/rest/api/keyvault/>

## Status legend

- ✓ — implemented + tested
- ✗ — missing or missing real-client coverage; paired with an open BUG
- 501 — stubbed with `NotImplemented` envelope
- n/a — no meaningful client/provider surface exists

## Common (every data-plane request)

| Operation | Verb + path | sim handler | sdk-test | tf-test | notes |
|---|---|---|---|---|---|
| Challenge handshake | any `<vault>.vault.<host>/...` w/o `Authorization` | ✓ `keyvault.go::registerKeyVault` (WrapHandler) + `handleKeyVaultDataPlane` | ✓ `keyvault_sdk_test.go::TestKeyVault_SDK_Secrets_ChallengeRoundTrip` + Keys + Certificates | n/a | URL format must split to ≥ 4 segments (BUG-1143). |

## Secrets

| Operation | Verb + path | sim handler | sdk-test | tf-test | notes |
|---|---|---|---|---|---|
| SetSecret | `PUT /secrets/{name}` | ✓ `keyvault.go::handleKVSetSecret` | ✓ `TestKeyVault_SDK_Secrets_ChallengeRoundTrip` | ✓ `azurerm_key_vault_secret` | |
| GetSecret | `GET /secrets/{name}` | ✓ `handleKVGetSecret` | ✓ same | ✓ same | |
| GetSecret (specific version) | `GET /secrets/{name}/{version}` | ✓ same | ✓ `TestKeyVault_State_FullVersionChain` | ✓ same | |
| ListSecrets | `GET /secrets` | ✓ `handleKVListSecrets` | ✓ `TestKeyVault_SDK_Secrets_ChallengeRoundTrip` | ✓ `azurerm_key_vault_secret` refresh | SDK pager. |
| ListSecretVersions | `GET /secrets/{name}/versions` | ✓ same | ✓ `TestKeyVault_State_FullVersionChain` | n/a | SDK pager; azurerm secret resource does not expose a separate version-list flow. |
| DeleteSecret | `DELETE /secrets/{name}` | ✓ `handleKVDeleteSecret` | ✓ `TestKeyVault_SDK_Secrets_ChallengeRoundTrip` + `TestKeyVault_State_SoftDeleteRoundTrip` | ✓ `azurerm_key_vault_secret` destroy | |
| UpdateSecret | `PATCH /secrets/{name}/{version}` | ✓ `handleKVPatchSecret` | ✓ `TestKeyVault_SDK_Secrets_ChallengeRoundTrip` | ✓ `azurerm_key_vault_secret` updates | |
| BackupSecret / RestoreSecret | `POST /secrets/{name}/backup` / `/secrets/restore` | ✓ `handleKVBackupSecret` / `handleKVRestoreSecret` | ✓ `TestKeyVault_SDK_Secrets_ChallengeRoundTrip` | n/a | Opaque backup blob preserves the simulator's real stored secret versions. |
| RecoverDeletedSecret / PurgeDeletedSecret | `POST /deletedsecrets/{name}/recover` / `DELETE /deletedsecrets/{name}` | ✓ `handleKVRecoverDeletedSecret` / `handleKVPurgeDeletedSecret` | ✓ `TestKeyVault_State_SoftDeleteRoundTrip` | n/a | Soft-delete state machine; azurerm secret destroy does not require a separate recover route. |

## Keys

| Operation | Verb + path | sim handler | sdk-test | tf-test | notes |
|---|---|---|---|---|---|
| CreateKey | `POST /keys/{name}/create` | ✓ `keyvault.go::handleKVCreateKey` | ✓ `TestKeyVault_SDK_Keys_ChallengeRoundTrip` | ✓ `azurerm_key_vault_key` | |
| GetKey | `GET /keys/{name}` | ✓ `handleKVGetKey` | ✓ same | ✓ same refresh | |
| GetKey (version) | `GET /keys/{name}/{version}` | ✓ same | ✓ same | ✓ same | |
| ListKeys | `GET /keys` | ✓ `handleKVListKeys` | ✓ same | ✓ same refresh | SDK pager. |
| ListKeyVersions | `GET /keys/{name}/versions` | ✓ `handleKVListKeyVersions` | ✓ same | ✓ same refresh | SDK pager. |
| DeleteKey | `DELETE /keys/{name}` | ✓ `handleKVDeleteKey` | ✓ same | ✓ `azurerm_key_vault_key` destroy | Soft-delete state. |
| PurgeDeletedKey | `DELETE /deletedkeys/{name}` | ✓ `handleKVPurgeDeletedKey` | ✓ same | ✓ `azurerm_key_vault_key` purge-on-destroy | |
| UpdateKey | `PATCH /keys/{name}/{version}` | ✓ `handleKVUpdateKey` | ✓ same | ✓ same | |
| ImportKey | `PUT /keys/{name}` | ✓ `handleKVImportKey` | ✓ same | n/a | |
| Sign / Verify / Encrypt / Decrypt / WrapKey / UnwrapKey | `POST /keys/{name}/{version}/{op}` | ✓ `handleKVCryptoKey` | ✓ same | n/a | RSA operations use real generated/imported local key material behind the public Key Vault API. |
| BackupKey / RestoreKey | `POST /keys/{name}/backup` / `/keys/restore` | ✓ `handleKVBackupKey` / `handleKVRestoreKey` | ✓ same | n/a | |

## Certificates

| Operation | Verb + path | sim handler | sdk-test | tf-test | notes |
|---|---|---|---|---|---|
| CreateCertificate | `POST /certificates/{name}/create` | ✓ `keyvault.go::handleKVCreateCertificate` | ✓ `TestKeyVault_SDK_Certificates_ChallengeRoundTrip` | ✓ `azurerm_key_vault_certificate` | Returns 202 + `CertificateOperation`; the simulator completes locally and exposes `/pending`. |
| GetCertificate | `GET /certificates/{name}` | ✓ `handleKVGetCertificate` | ✓ same | ✓ same refresh | |
| ListCertificates | `GET /certificates` | ✓ `handleKVListCertificates` | ✓ same | ✓ same refresh | SDK pager. |
| ListCertificateVersions | `GET /certificates/{name}/versions` | ✓ `handleKVListCertificateVersions` | ✓ same | ✓ same refresh | SDK pager. |
| DeleteCertificate | `DELETE /certificates/{name}` | ✓ `handleKVDeleteCertificate` | ✓ same | ✓ `azurerm_key_vault_certificate` destroy | Soft-delete state. |
| PurgeDeletedCertificate | `DELETE /deletedcertificates/{name}` | ✓ `handleKVPurgeDeletedCertificate` | ✓ same | ✓ `azurerm_key_vault_certificate` purge-on-destroy | |
| GetCertificateOperation | `GET /certificates/{name}/pending` | ✓ `handleKVGetCertificateOperation` | ✓ same | ✓ `azurerm_key_vault_certificate` create polling | |
| UpdateCertificateOperation | `PATCH /certificates/{name}/pending` | ✓ `handleKVUpdateCertificateOperation` | ✓ same | n/a | |
| UpdateCertificate | `PATCH /certificates/{name}/{version}` | ✓ `handleKVUpdateCertificate` | ✓ same | ✓ same | |
| ImportCertificate | `POST /certificates/{name}/import` | ✓ `handleKVImportCertificate` | ✓ same | n/a | |
| MergeCertificate | `POST /certificates/{name}/pending/merge` | ✓ `handleKVMergeCertificate` | ✓ same | n/a | |
| BackupCertificate / RestoreCertificate | `POST /certificates/{name}/backup` / `/certificates/restore` | ✓ `handleKVBackupCertificate` / `handleKVRestoreCertificate` | ✓ same | n/a | |

## Known gaps

The issue #282 data-plane parity gaps were closed. Secrets, keys, and certificates now have SDK lifecycle coverage for pagers, updates, backup/restore, and soft-delete purge paths, and Terraform coverage covers `azurerm_key_vault_secret`, `azurerm_key_vault_key`, and `azurerm_key_vault_certificate`.

## Reopens that produced this table

- Issue #193 (predecessor repository) reopened — PR #200's `WWW-Authenticate` URL had 3 path segments; Azure SDKs panicked at `parts[3]`. PR #200's coverage test used raw `net/http` + `Authorization: Bearer fake-token`, which bypassed the challenge flow entirely and never exercised the SDK's parser. This table makes the gap visible: every KV op that has an SDK client (Secrets / Keys / Certificates) is held to having an SDK-driven sdk-test.
