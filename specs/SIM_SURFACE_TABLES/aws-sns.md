# Sim surface — aws-sns

Surface registered in `simulator-aws/sns.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action CreateTopic` | ✓ `simulator-aws/sns.go:76::handleSNSCreateTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteTopic` | ✓ `simulator-aws/sns.go:77::handleSNSDeleteTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTopics` | ✓ `simulator-aws/sns.go:78::handleSNSListTopics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetTopicAttributes` | ✓ `simulator-aws/sns.go:79::handleSNSGetTopicAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetTopicAttributes` | ✓ `simulator-aws/sns.go:80::handleSNSSetTopicAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Subscribe` | ✓ `simulator-aws/sns.go:81::handleSNSSubscribe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Unsubscribe` | ✓ `simulator-aws/sns.go:82::handleSNSUnsubscribe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ConfirmSubscription` | ✓ `simulator-aws/sns.go:83::handleSNSConfirmSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetSubscriptionAttributes` | ✓ `simulator-aws/sns.go:84::handleSNSGetSubscriptionAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetSubscriptionAttributes` | ✓ `simulator-aws/sns.go:85::handleSNSSetSubscriptionAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListSubscriptions` | ✓ `simulator-aws/sns.go:86::handleSNSListSubscriptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListSubscriptionsByTopic` | ✓ `simulator-aws/sns.go:87::handleSNSListSubscriptionsByTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddPermission` | ✓ `simulator-aws/sns.go:88::handleSNSAddPermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemovePermission` | ✓ `simulator-aws/sns.go:89::handleSNSRemovePermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Publish` | ✓ `simulator-aws/sns.go:90::handleSNSPublish` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PublishBatch` | ✓ `simulator-aws/sns.go:91::handleSNSPublishBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TagResource` | ✓ `simulator-aws/sns.go:92::handleSNSTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UntagResource` | ✓ `simulator-aws/sns.go:93::handleSNSUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTagsForResource` | ✓ `simulator-aws/sns.go:94::handleSNSListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetEndpointAttributes` | ✓ `simulator-aws/sns_mobile_sms.go:100::handleSNSSetEndpointAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListEndpointsByPlatformApplication` | ✓ `simulator-aws/sns_mobile_sms.go:101::handleSNSListEndpointsByPlatformApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateSMSSandboxPhoneNumber` | ○ `simulator-aws/sns_mobile_sms.go:103::handleSNSCreateSMSSandboxPhoneNumber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteSMSSandboxPhoneNumber` | ✓ `simulator-aws/sns_mobile_sms.go:104::handleSNSDeleteSMSSandboxPhoneNumber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action VerifySMSSandboxPhoneNumber` | ✓ `simulator-aws/sns_mobile_sms.go:105::handleSNSVerifySMSSandboxPhoneNumber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListSMSSandboxPhoneNumbers` | ✓ `simulator-aws/sns_mobile_sms.go:106::handleSNSListSMSSandboxPhoneNumbers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetSMSSandboxAccountStatus` | ✓ `simulator-aws/sns_mobile_sms.go:107::handleSNSGetSMSSandboxAccountStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetSMSAttributes` | ✓ `simulator-aws/sns_mobile_sms.go:109::handleSNSGetSMSAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetSMSAttributes` | ✓ `simulator-aws/sns_mobile_sms.go:110::handleSNSSetSMSAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CheckIfPhoneNumberIsOptedOut` | ✓ `simulator-aws/sns_mobile_sms.go:111::handleSNSCheckIfPhoneNumberIsOptedOut` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListPhoneNumbersOptedOut` | ✓ `simulator-aws/sns_mobile_sms.go:112::handleSNSListPhoneNumbersOptedOut` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action OptInPhoneNumber` | ✓ `simulator-aws/sns_mobile_sms.go:113::handleSNSOptInPhoneNumber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListOriginationNumbers` | ✓ `simulator-aws/sns_mobile_sms.go:114::handleSNSListOriginationNumbers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutDataProtectionPolicy` | ✓ `simulator-aws/sns_mobile_sms.go:116::handleSNSPutDataProtectionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetDataProtectionPolicy` | ✓ `simulator-aws/sns_mobile_sms.go:117::handleSNSGetDataProtectionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreatePlatformApplication` | ✓ `simulator-aws/sns_mobile_sms.go:91::handleSNSCreatePlatformApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeletePlatformApplication` | ✓ `simulator-aws/sns_mobile_sms.go:92::handleSNSDeletePlatformApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetPlatformApplicationAttributes` | ✓ `simulator-aws/sns_mobile_sms.go:93::handleSNSGetPlatformApplicationAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetPlatformApplicationAttributes` | ✓ `simulator-aws/sns_mobile_sms.go:94::handleSNSSetPlatformApplicationAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListPlatformApplications` | ✓ `simulator-aws/sns_mobile_sms.go:95::handleSNSListPlatformApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreatePlatformEndpoint` | ✓ `simulator-aws/sns_mobile_sms.go:97::handleSNSCreatePlatformEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteEndpoint` | ✓ `simulator-aws/sns_mobile_sms.go:98::handleSNSDeleteEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetEndpointAttributes` | ✓ `simulator-aws/sns_mobile_sms.go:99::handleSNSGetEndpointAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
