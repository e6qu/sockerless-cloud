# Identity and access

Each simulator authenticates every request with its cloud's own credentials
and authorizes it with its cloud's own policy language. The credentials a
client starts with are seeded, the way a new account's first credential is:

| Cloud | First credential | Signs requests with | Authorization |
|---|---|---|---|
| AWS | IAM user `sockerless-sim-admin`, access key `test` / `test` | SigV4 | IAM policies: identity, resource and trust policies, permissions boundaries for users |
| Google Cloud | Service account `sockerless-bootstrap@sockerless.iam.gserviceaccount.com` and its published key | OAuth 2.0 access tokens from the JWT-bearer grant | IAM policies on the resource hierarchy and resources, in the places listed under Enforcement |
| Azure | Application `test-client-id` with secret `test-client-secret` in tenant `11111111-1111-1111-1111-111111111111`, Owner on the tenant root management group | Microsoft Entra ID access tokens | Azure RBAC on every Azure Resource Manager operation and on the Storage, Key Vault and Event Grid data actions; Shared Key and SAS for Storage, Service Bus and Event Hubs |

How far each simulator goes differs, and each section ends with its limits,
the open bugs among them. Workloads get their credentials through instance
metadata, which [Instance metadata](instance-metadata.md) documents.

- [AWS](#aws)
- [Google Cloud](#google-cloud)
- [Azure](#azure)

## Running the examples

Each cloud's section ends with examples you can run. `TestDocs` in
`testutil/doctest` runs them as written, one part per cloud, and checks the
output they say to expect. They need Go, Docker Engine, curl and jq; the AWS
examples also need AWS CLI version 2, and the Google Cloud examples gcloud and
openssl.

Clone the repository. Each cloud's examples install that cloud's simulator
from the clone, and the Google Cloud examples also run a command from it:

<!-- doctest: run -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
export SOCKERLESS_SRC="$PWD/sockerless-cloud"
export PATH="$(go env GOPATH)/bin:$PATH"
```

Each cloud's examples start its simulator in a terminal of its own and stop it
at the end. A command written `! command` is one the example expects to fail:
the shell reports success when the command fails, and the error it prints is
the point of the example.

## AWS

The AWS simulator (`simulator-aws/`) serves AWS Identity and Access Management (IAM) and AWS Security Token Service (AWS STS) as ordinary service slices. It also runs every signed request through two gates: SigV4 authentication, then call-time IAM authorization. A client signs with real credentials the simulator issued. IAM then evaluates that principal's policies the way AWS does, within the limits listed at the end of this section.

### Principals and credentials

#### The seeded credential

On start, `seedRootAdminCredential` (`iam_users.go`) provisions one IAM user and one access key. It skips this when the key already exists in a persistent store.

| Item | Value |
|---|---|
| Account | `123456789012`, or `SOCKERLESS_AWS_ACCOUNT_ID` (`awsAccountID`, `aws_identity.go`) |
| Region | `us-east-1`, or `SOCKERLESS_AWS_REGION` (`awsRegion`) |
| Access key ID / secret | `test` / `test` |
| Principal | IAM user `sockerless-sim-admin`, `arn:aws:iam::123456789012:user/sockerless-sim-admin`, user ID `AIDAROOTSIMADMIN00000` |
| Permissions | inline policy `AdministratorAccess`: `{"Effect":"Allow","Action":"*","Resource":"*"}` |

The `test` key belongs to an ordinary IAM user, not the account root user. Its access comes from its policy alone, so a trust policy, a resource policy or an explicit `Deny` still constrains it. No principal bypasses evaluation. The simulator has no root user who signs requests. `AssumeRoot` mints a session whose principal is `arn:aws:iam::<account>:root`, but that session carries no policies, so the gate denies it every action (see the limits).

#### IAM principals

| Principal | Created by | Where it is stored / resolved |
|---|---|---|
| IAM user | `CreateUser`; long-term keys from `CreateAccessKey` (`AKIA…`, `iam_users.go`, `iam_users_creds.go`) | `iamUsers`, `iamAccessKeys`. A key must be `Active` to sign (`UpdateAccessKey` toggles it). |
| Group | `CreateGroup`, `AddUserToGroup` (`iam_groups.go`) | A user inherits its groups' inline and attached policies (`iamEffectivePolicyDocsForUser`). |
| Role | `CreateRole` with `AssumeRolePolicyDocument` (`iam.go`, `iam_roles_policies.go`) | Inline and attached policies come from `iamPolicyDocsForRole`. |
| Instance profile | `CreateInstanceProfile`, `AddRoleToInstanceProfile` (`iam.go`) | Read by the instance metadata service (`ec2InstanceProfileRole`). |
| Service-linked role | `CreateServiceLinkedRole` (`iam_slr_oidc.go`) | Accepts any service principal. Deletion completes at once. |
| OIDC provider | `CreateOpenIDConnectProvider` (`iam_slr_oidc.go`) | Trusted by `AssumeRoleWithWebIdentity` for the matching issuer. |
| SAML provider | `CreateSAMLProvider` (`iam_providers_certs.go`) | Its metadata supplies the entity ID and signing certificates (`samlProviderSigning`). |

`iamPrincipalForAccessKey` (`sts.go`) turns an access key ID into a principal ARN and the policy documents that govern it. It checks temporary credentials first and long-term user keys second.

#### AWS STS

`registerSTS` (`sts.go`) serves these operations:

| Operation | Trust check | Session principal | Notes |
|---|---|---|---|
| `GetCallerIdentity` | none (permissionless) | the signer | Unknown key → `InvalidClientTokenId` |
| `AssumeRole` | role trust policy (`stsTrustAllows`) with `sts:RoleSessionName`, `sts:ExternalId`, `sts:SourceIdentity` in context | `assumed-role/<role>/<session>` | Tags need `sts:TagSession` in the trust policy. Transitive tags carry into chained sessions (`sts_session_tags.go`). |
| `AssumeRoleWithWebIdentity` | token verified against the issuer's discovery document and JSON Web Key Set (`verifyWebIdentityToken`, `sts_webidentity.go`); audience in the provider's client ID list; trust policy with `<provider>:sub`, `:aud`, `:amr`, `:email` and similar | `assumed-role/<role>/<session>` | Unsigned by design. |
| `AssumeRoleWithSAML` | assertion signed by one of the provider's certificates, issuer and audience match, within its validity window (`verifySAMLAssertion`, `sts_saml.go`); role attribute names this role and provider; trust policy | `assumed-role/<role>/<RoleSessionName attribute>` | Unsigned by design. An expired assertion → `ExpiredTokenException`. |
| `GetSessionToken` | none | the calling user | `SerialNumber` plus `TokenCode` mark the session as MFA. |
| `GetFederationToken` | none | `federated-user/<name>` | Accepts tags. |
| `GetWebIdentityToken` | outbound federation enabled (`sts_outbound_federation.go`) | n/a | Issues a JWT from `https://<id>.tokens.sts.global.api.aws`, which serves discovery and JSON Web Key Set documents. |
| `AssumeRoot`, `GetDelegatedAccessToken` | none | `…:root` / the caller | See the limits. |
| `DecodeAuthorizationMessage`, `GetAccessKeyInfo` | none | n/a | See the limits. |

Web-identity and SAML sessions keep the provider's claims (`stsChainedFederationKeys`, `sts_role_chaining.go`). A role that such a session assumes next can still condition on them.

#### Temporary credentials and expiry

`stsMintTempCred` (`sts.go`) mints an `ASIA…` access key ID, a random secret and a session token. The token seals the expiration and is MACed with a key stored in `sts_token_keys`. Tokens therefore survive a restart with a persistent store. Each session is recorded in `iamTempCreds` with its principal, its tags and its MFA flag.

- `DurationSeconds` defaults to 3600 and is clamped to 900–43200 (`stsDurationSeconds`).
- `sigv4SecretFor` refuses a session past its expiration with `ExpiredToken` / `ExpiredTokenException`. It refuses a session token that does not match with `InvalidClientTokenId`.
- `startTempCredSweeper` deletes expired sessions. Once a session is gone, `stsTokenExpiration` still reads the sealed expiration from the token, so the caller gets "expired" rather than "invalid".

### Authentication: SigV4

`sigv4.go` recomputes the signature of every request it gates.

1. **Form.** An `X-Amz-Algorithm` or `X-Amz-Signature` query parameter selects the presigned form (`sigv4VerifyPresigned`). An `Authorization: AWS4-HMAC-SHA256 …` header selects the header form (`sigv4VerifyHeader`). If neither is present, the request carries no credential.
2. **Secret.** `sigv4SecretFor` resolves the access key ID to the simulator's in-process service credential (`awsServiceCredential`, `aws_service_account.go`), an `Active` IAM key, or a live temporary credential whose token matches. For Amazon S3 Express One Zone, the token comes from `x-amz-s3session-token` (`sigv4PresentedSessionToken`).
3. **Canonical request.** The simulator rebuilds it from the method, the URI, the sorted query, the signed headers, and the payload hash. The URI is encoded twice for every service except Amazon S3, as the SDK signers do (`sigv4CanonicalURI`). The payload hash is `X-Amz-Content-Sha256` when present and the SHA-256 of the buffered body otherwise. A body that does not hash to a declared digest fails.
4. **Compare.** The simulator derives the signing key, computes the signature and compares it in constant time.
5. **Expiry.** An S3 presigned URL past `X-Amz-Expires` fails (`sigv4PresignedExpired`).

The streaming forms (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]`, `STREAMING-UNSIGNED-PAYLOAD-TRAILER`) authenticate on the seed signature in the header. `awsChunkedReader` (`awschunked.go`) unwraps the chunk framing but does not verify `chunk-signature` values or trailing checksums.

| Failure | awsJson | awsQuery (IAM, STS, …) / EC2 | Amazon S3 |
|---|---|---|---|
| Signature mismatch | `SignatureDoesNotMatch` | `SignatureDoesNotMatch` | `SignatureDoesNotMatch` |
| Unknown or inactive key, bad token | `UnrecognizedClientException` | `InvalidClientTokenId` | `InvalidAccessKeyId` |
| Expired session | `ExpiredTokenException` | `ExpiredToken` | `ExpiredToken` |
| Presigned URL expired | n/a | n/a | `AccessDenied` ("Request has expired") |
| No credential | `MissingAuthenticationTokenException` | `MissingAuthenticationToken` | the anonymous principal (below) |

All of these answer HTTP 403 (`sigv4WriteControlPlaneError`, `sigv4WriteControlPlaneMissingAuth`, `sigv4WriteS3Error`).

Where the gate runs:

| Surface | Authentication | Unsigned request |
|---|---|---|
| `POST /` (every awsJson and awsQuery service) — `main.go` → `sigv4EnforceControlPlane` | verified | refused, except `AssumeRoleWithWebIdentity`, `AssumeRoleWithSAML` and in-process service calls |
| Amazon S3 REST — `s3Enforced` (`s3.go`) | verified | the anonymous principal: `CreateBucket`, `ListBuckets` and `ListDirectoryBuckets` are refused, and any other operation needs a bucket-policy statement for Principal `"*"` or an AllUsers ACL grant, which Block Public Access (`RestrictPublicBuckets`, `IgnorePublicAcls`) and `BucketOwnerEnforced` withdraw (`s3RefuseAnonymous`, `s3_anonymous.go`). A request to a missing bucket still answers `NoSuchBucket`. |
| AWS Lambda REST — `lambdaEnforcedDynamic` (`lambda_enforcement.go`) | verified | refused, except in-process service principals |
| Lambda function URLs with `AWS_IAM` (`lambda_function_urls.go`), Amazon CloudWatch CBOR (`cloudwatch_alarm_ops.go`), Amazon RDS IAM auth tokens (`rds_dataplane.go`) | verified | refused |
| AWS Amplify, Amazon API Gateway, AWS Batch, Amazon EFS, EventBridge Scheduler, Amazon CloudFront, Amazon Route 53 REST — `awsRESTAuthenticate`, then `iamEnforceRESTRoute` (`rest_enforcement.go`) | verified, header and presigned forms | refused, except in-process service principals. REST-JSON services answer `MissingAuthenticationTokenException`, `InvalidSignatureException`, `UnrecognizedClientException` or `ExpiredTokenException`; CloudFront and Route 53 answer `MissingAuthenticationToken`, `SignatureDoesNotMatch`, `InvalidClientTokenId` or `ExpiredToken`, in an `<ErrorResponse>`. |

### Authorization: the IAM gate

`iamEnforce` (`iam_enforcement.go`) runs after authentication on `POST /`. `s3Enforced`, `lambdaEnforcedDynamic` and `iamEnforceRESTRoute` run the same core, `iamAuthorizeWithContext`, for REST.

#### Deriving the action and resources

- **Action.** `iamActionForRequest` builds `<prefix>:<operation>` from the request's AWS CloudTrail event source and its `X-Amz-Target` or `Action` (`monitoring` becomes `cloudwatch`). REST services take the operation whose Smithy `http` trait matches the route (`awsRESTOperationFor`). `iamOperationActions` (`iam_resource_types_gen.go`) renames operations whose IAM action differs. `iamOperationTargets` (`iam_operation_actions.go`) splits multi-action operations: `kms:ReEncrypt` becomes `ReEncryptFrom` plus `ReEncryptTo`, DynamoDB transactions and PartiQL are authorized per item, and S3 operations map through `s3AuthorizationTargets`. A create that carries tags also needs the service's tagging action (`iamTagOnCreateTargets`).
- **Resources.** `iamResourceARNsForRequest` reads the target from the request. The code checks DynamoDB tables and Lambda functions directly. Otherwise `iamDerivedResourceARNs` (`iam_resource_arns.go`) looks the action up in `iamActionResourceTypes` and fills that type's ARN template from `iamResourceARNFormats`. Both tables are generated by `scripts/gen-aws-iam-resource-types.sh` from the AWS Service Reference vendored in `specs/cloud-api/aws/service-reference/`. An action with no resource type, or a target the request does not name, authorizes against `*`. A `*` matches only a policy whose `Resource` is `*`.
- **Context.** `iamRequestConditionContext` sets `aws:username`, `aws:userid`, `aws:SourceIp`, `aws:RequestedRegion` and `aws:PrincipalOrgID`. It adds global keys (`aws:CurrentTime`, `aws:EpochTime`, `aws:SecureTransport`, `aws:UserAgent`, `aws:PrincipalArn`, `aws:PrincipalTag/*`, `aws:ResourceAccount`, `aws:MultiFactorAuth*`) and service keys (`aws:ResourceTag/*`, `aws:RequestTag/*`, `aws:TagKeys`, and each service's own keys in `iam_<service>_*condition_keys.go`). `testdata/iam_condition_key_gaps.tsv` lists the (action, key) pairs the gate does not build.

#### Evaluation order

For each target, `iamAuthorizeWithContext` takes these steps:

1. It resolves the principal. An unregistered key returns `registered=false`, and the gate allows the request. Only an in-process service principal reaches this branch: SigV4 refuses unknown keys, and `s3RefuseAnonymous` decides anonymous S3 requests first.
2. It evaluates the identity policies: the user's inline, attached and group policies, or the assumed role's inline and attached policies (`iamEvalDecision`). An explicit `Deny` ends evaluation.
3. It evaluates the resource policy stored under the target ARN (`iamResourcePolicyDocsForARN`). For `sts:AssumeRole` and `sts:TagSession`, it adds the role's trust policy. A `Deny` there ends evaluation. An `Allow` that names the caller (by ARN, by `*`, or by a role ARN for that role's sessions) grants on its own. An `Allow` that names only the caller's account delegates to IAM and grants nothing (`iamPrincipalDelegated`), which is how the default AWS KMS key policy behaves.
4. It applies the user's permissions boundary (`iamPermissionBoundaryDocs`). Anything the boundary does not allow is denied.
5. `iamEnforcePassRole` then checks `iam:PassRole` on every role ARN in the body, with `iam:PassedToService` taken from `iamPassRoleOperations`. A body too large to read is denied.

A denial answers HTTP 403: `AccessDeniedException` (awsJson, with the message member each model spells, `iam_deny_wire.go`), `UnauthorizedOperation` (EC2) or `AccessDenied` (other query services and S3).

These resource policies reach step 3 through `iamPutResourcePolicy`: Amazon S3 bucket policies (looked up at the bucket ARN for object requests), Lambda function policies, Amazon SNS topic and Amazon SQS queue policies, AWS KMS key policies, AWS Secrets Manager, Amazon Kinesis, DynamoDB, AWS Glue Data Catalog, AWS CodeBuild, AWS CloudTrail Lake and AWS Organizations resource policies.

#### Policy language

`iam_policy_sim.go` implements the language:

| Element | Support |
|---|---|
| `Action` / `NotAction`, `Resource` / `NotResource` | yes. Actions match case-insensitively. `*` and `?` match through `sim.Wildcard{AnyOne: true}` (`iamGlobMatch`). A pattern ending `:*` also matches the bare log-group ARN (`iamLogGroupPatternMatches`). |
| `Principal` / `NotPrincipal` | `*`, `AWS` (ARN, account ID, `:root`), `Service` (global or regional name, matched against `aws:CalledVia`), `Federated` |
| Operators | `String*` (including `IgnoreCase`, `Like`), `Arn*`, `Numeric*`, `Date*`, `Bool`, `IpAddress` / `NotIpAddress`, `BinaryEquals`, `Null`, the `…IfExists` suffix, and the `ForAllValues:` / `ForAnyValue:` prefixes. An unknown operator never matches. |
| Condition key names | case-insensitive (`iamCtxLookup`) |
| Policy variables | `${key}` in `Resource` / `NotResource`, plus `${*}`, `${?}`, `${$}` (`iamSubstituteVars`) |
| Explicit deny | wins over every allow |

### Workload identity

A workload holds a session of its role. `roleSessionCredential` (`role_credentials.go`) mints a one-hour session per holder and keeps serving it while more than half its lifetime remains. The session is revoked when the holder stops (`roleHolderLive`).

| Workload | Channel | Environment the simulator sets | Session name |
|---|---|---|---|
| Amazon EC2 instance with a profile | IMDSv2: `PUT /latest/api/token` (TTL 1–21600 s, refused with `X-Forwarded-For`), then `GET /latest/meta-data/iam/security-credentials/<role>` with `X-aws-ec2-metadata-token` (`ec2_imds.go`). IMDSv1 works unless `HttpTokens=required`. | `AWS_EC2_METADATA_SERVICE_ENDPOINT`, `AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE=IPv4` (`ec2MetadataEnv`, `metadata.go`) | the instance ID |
| Amazon ECS task role | container credentials endpoint `GET /v2/credentials/{id}` | `AWS_CONTAINER_CREDENTIALS_FULL_URI`, `ECS_CONTAINER_METADATA_URI_V4` (`ecsTaskMetadataEnv`) | the task ID |
| AWS Batch, AWS CodeBuild, AWS Glue, AWS Amplify compute | the same endpoint (`grantContainerCredentials`) | `AWS_CONTAINER_CREDENTIALS_FULL_URI` | per service, e.g. `GlueJobRunnerSession` |
| AWS Lambda execution role | environment (`roleEnvironmentCredentialsExpiring`, `lambda_environment.go`) | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | the function name |

The SDK's default credential chain finds each of these unchanged. The metadata endpoints name the workload network's sidecar. The SDK accepts a plain-HTTP container credentials URI only on loopback or the link-local address. On backends that forward a workload's loopback, the URI is therefore `http://127.0.0.1:51679`, which the sidecar relays to the simulator.

A service accepts a role only if the role's trust policy admits it. `iamServiceTrustedRole` (`iam_service_context.go`) evaluates `sts:AssumeRole` for `service:<principal>` with `aws:SourceArn` and `aws:SourceAccount`. Callers include Lambda (`lambda.amazonaws.com`, which refuses with `InvalidParameterValueException` "The role defined for the function cannot be assumed by Lambda."), Amazon ECS RunTask (`ecs-tasks.amazonaws.com`), ECS services (`ecs.amazonaws.com`), Batch, CodeBuild, Glue, Amplify, EventBridge Scheduler, AWS Step Functions and AWS Systems Manager. `iamValidateServiceRole` also checks that the role's policies allow each action the service will take.

### Service-to-service calls

One service reaches another through its API. `sim.Server.Call` (`sim/call.go`) serves an in-process request through the full chain: authentication, the IAM gate, CloudTrail and specification validation. `awsCall` (`aws_call.go`) wraps it with an `awsActor` and acts in one of two ways:

- **As a service principal** (`Role` empty). The request is unsigned and carries a `sim.Caller` (`events.amazonaws.com`, plus the source ARN). `sigv4EnforceControlPlane` admits it only when `sim.InProcess` holds. `iamEnforceServicePrincipal` then requires the target's resource policy to admit the service, with `aws:ViaAWSService=true`, `aws:CalledVia`, `aws:SourceArn` and `aws:SourceAccount` (`iamServiceInitiatedConditionContext`). A target with no resource policy denies. An outside client cannot set `sim.Caller`.
- **As a role** (`Role` set). `awsAssumeServiceRole` checks the trust policy with `iamServiceTrustedRole`. It holds a session under the holder `service:<principal>|<role>` and signs the request with `sigv4Sign`. The target then evaluates the role's identity policies like any client's.

### IAM's own surfaces

- **Operations.** About 180 IAM operations are registered across `iam*.go`: users, groups, roles, policies and versions, instance profiles, access keys, login profiles, MFA devices, SSH and signing keys, server certificates, OIDC and SAML providers, service-linked roles, account settings and reports, tags, delegation requests, outbound web identity federation and organizations root-credential features. `specs/SIM_SURFACE_TABLES/aws-iam.md` lists 78 of them, because its extractor misses map-registered handlers. `specs/SIM_SURFACE_TABLES/aws-sts.md` lists all 11 STS operations.
- **Policy simulator.** `SimulateCustomPolicy` and `SimulatePrincipalPolicy` (`iam_policy_sim.go`) run the same `iamEvalDecision` and report `allowed`, `explicitDeny` or `implicitDeny` with `MissingContextValues`. `GetContextKeysForCustomPolicy` and `GetContextKeysForPrincipalPolicy` are served.
- **IAM Access Analyzer.** Not served.
- **AWS Organizations.** Served (`organizations.go`), including `SERVICE_CONTROL_POLICY` and `RESOURCE_CONTROL_POLICY` documents. The gate does not evaluate either.
- **Console sign-in.** `ui/packages/simulator-aws/src/console/federation.ts` exchanges the operator's identity provider token for credentials with an unsigned `AssumeRoleWithWebIdentity`, using `RoleSessionName=console`. It caches them until a minute before expiry and signs every console request with them. The console's role and the OIDC provider for its issuer must exist in IAM, as they would in a real account.

<!-- doctest: part name=aws -->
### Examples

Install the simulator from the clone:

<!-- doctest: run -->
```sh
go -C "$SOCKERLESS_SRC" install ./simulator-aws
```

Start it in a terminal of its own:

<!-- doctest: background id=aws ready="Listening on" -->
```sh
simulator-aws
```

It listens on port 4566. In your first terminal, point the AWS CLI at it and
sign in with the seeded key:

<!-- doctest: run expect="\"Arn\": \"arn:aws:iam::123456789012:user/sockerless-sim-admin\"" -->
```sh
export AWS_ENDPOINT_URL=http://localhost:4566 AWS_DEFAULT_REGION=us-east-1
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
aws sts get-caller-identity
```

The caller is `arn:aws:iam::123456789012:user/sockerless-sim-admin`.

#### A user with one permission

Create `alice`, allow her one action, and keep her new access key in
`ALICE_KEY` and `ALICE_SECRET`:

<!-- doctest: run expect="(?m)^AKIA[0-9A-Z]{16}$" -->
```sh
aws iam create-user --user-name alice
aws iam put-user-policy --user-name alice --policy-name sqs-read \
    --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}'
ALICE=$(aws iam create-access-key --user-name alice \
    --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)
read -r ALICE_KEY ALICE_SECRET <<<"$ALICE"
export ALICE_KEY ALICE_SECRET
echo "$ALICE_KEY"
```

As `alice`, list the queues:

<!-- doctest: run expect="\"QueueUrls\": \\[\\]" -->
```sh
AWS_ACCESS_KEY_ID=$ALICE_KEY AWS_SECRET_ACCESS_KEY=$ALICE_SECRET aws sqs list-queues
```

```text
{
    "QueueUrls": []
}
```

Her policy does not allow `sqs:CreateQueue`:

<!-- doctest: run expect="\\(AccessDeniedException\\) when calling the CreateQueue operation: User: arn:aws:iam::123456789012:user/alice is not authorized to perform: sqs:CreateQueue because no identity-based policy allows the sqs:CreateQueue action" -->
```sh
! AWS_ACCESS_KEY_ID=$ALICE_KEY AWS_SECRET_ACCESS_KEY=$ALICE_SECRET aws sqs create-queue --queue-name q1
```

```text
aws: [ERROR]: An error occurred (AccessDeniedException) when calling the CreateQueue operation: User: arn:aws:iam::123456789012:user/alice is not authorized to perform: sqs:CreateQueue because no identity-based policy allows the sqs:CreateQueue action
```

A wrong secret fails authentication before IAM evaluates anything:

<!-- doctest: run expect="\\(SignatureDoesNotMatch\\) when calling the ListQueues operation: The request signature we calculated does not match the signature you provided\\." -->
```sh
! AWS_ACCESS_KEY_ID=$ALICE_KEY AWS_SECRET_ACCESS_KEY=wrong aws sqs list-queues
```

```text
aws: [ERROR]: An error occurred (SignatureDoesNotMatch) when calling the ListQueues operation: The request signature we calculated does not match the signature you provided. Check your AWS Secret Access Key and signing method.
```

#### Assume a role

Create a role whose trust policy names `alice`, and allow it to list buckets:

<!-- doctest: run expect="\"Arn\": \"arn:aws:iam::123456789012:role/reader\"" -->
```sh
aws iam create-role --role-name reader --assume-role-policy-document \
    '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/alice"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name reader --policy-name s3-list \
    --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}'
```

The administrator, whom the trust policy does not name, cannot assume it:

<!-- doctest: run expect="\\(AccessDenied\\) when calling the AssumeRole operation: User: arn:aws:iam::123456789012:user/sockerless-sim-admin is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::123456789012:role/reader" -->
```sh
! aws sts assume-role --role-arn arn:aws:iam::123456789012:role/reader --role-session-name s1
```

```text
aws: [ERROR]: An error occurred (AccessDenied) when calling the AssumeRole operation: User: arn:aws:iam::123456789012:user/sockerless-sim-admin is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::123456789012:role/reader
```

`alice` can, without an identity policy for `sts:AssumeRole`, because the trust
policy names her. Keep the session's credentials in `S1_KEY`, `S1_SECRET` and
`S1_TOKEN`, and ask who they belong to:

<!-- doctest: run expect="\"Arn\": \"arn:aws:sts::123456789012:assumed-role/reader/s1\"" -->
```sh
SESSION=$(AWS_ACCESS_KEY_ID=$ALICE_KEY AWS_SECRET_ACCESS_KEY=$ALICE_SECRET \
  aws sts assume-role --role-arn arn:aws:iam::123456789012:role/reader \
    --role-session-name s1 --duration-seconds 900 \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)
read -r S1_KEY S1_SECRET S1_TOKEN <<<"$SESSION"
export S1_KEY S1_SECRET S1_TOKEN
AWS_ACCESS_KEY_ID=$S1_KEY AWS_SECRET_ACCESS_KEY=$S1_SECRET AWS_SESSION_TOKEN=$S1_TOKEN \
  aws sts get-caller-identity
```

The caller is `arn:aws:sts::123456789012:assumed-role/reader/s1`. The session
holds the role's permissions and nothing else: it lists buckets, and it cannot
create one.

<!-- doctest: run expect="\"Buckets\": \\[" -->
```sh
AWS_ACCESS_KEY_ID=$S1_KEY AWS_SECRET_ACCESS_KEY=$S1_SECRET AWS_SESSION_TOKEN=$S1_TOKEN \
  aws s3api list-buckets
```

<!-- doctest: run expect="\\(AccessDenied\\) when calling the CreateBucket operation: User: arn:aws:sts::123456789012:assumed-role/reader/s1 is not authorized to perform: s3:CreateBucket because no identity-based policy or resource-based policy allows the s3:CreateBucket action" -->
```sh
! AWS_ACCESS_KEY_ID=$S1_KEY AWS_SECRET_ACCESS_KEY=$S1_SECRET AWS_SESSION_TOKEN=$S1_TOKEN \
  aws s3api create-bucket --bucket b1
```

```text
aws: [ERROR]: An error occurred (AccessDenied) when calling the CreateBucket operation: User: arn:aws:sts::123456789012:assumed-role/reader/s1 is not authorized to perform: s3:CreateBucket because no identity-based policy or resource-based policy allows the s3:CreateBucket action
```

#### Simulate a principal's policy

As the administrator again:

<!-- doctest: run expect="s3:ListAllMyBuckets\\s+allowed" expect="s3:CreateBucket\\s+implicitDeny" -->
```sh
aws iam simulate-principal-policy --policy-source-arn arn:aws:iam::123456789012:role/reader \
    --action-names s3:ListAllMyBuckets s3:CreateBucket \
    --query 'EvaluationResults[].[EvalActionName,EvalDecision]' --output text
```

```text
s3:ListAllMyBuckets	allowed
s3:CreateBucket	implicitDeny
```

Then stop `simulator-aws` with Ctrl-C.

<!-- doctest: stop id=aws -->

### Limits

Each limit has a `BUGS.md` row. Those marked "observed" were reproduced against the running simulator.

| Limit | Detail | Bug |
|---|---|---|
| Amazon S3 public access details | `x-amz-acl` and `x-amz-grant-*` on `PutObject`, `CreateBucket` and `CopyObject` are not persisted; `BlockPublicAcls` and `BlockPublicPolicy` do not refuse public ACL and policy writes, there is no account-level Block Public Access, and new buckets do not default to `BucketOwnerEnforced` with Block Public Access on; `BucketOwnerEnforced` does not refuse ACL writes with `AccessControlListNotSupported`; a caller with `s3:GetObject` but not `s3:ListBucket` gets `NoSuchKey` for a missing key where S3 answers 403. | [3765](../BUGS.md) |
| Role permissions boundaries | `PutRolePermissionsBoundary` stores the boundary, but `iamPermissionBoundaryDocs` reads users only. A role's boundary does not cap its sessions (observed). | [3737](../BUGS.md) |
| Session policies | `AssumeRole`, `GetFederationToken` and the federation calls ignore `Policy` and `PolicyArns`. `PackedPolicySize` is always 0. A deny-all session policy does not restrict the session (observed). | [3737](../BUGS.md) |
| Federated-user and root sessions | `GetFederationToken` and `AssumeRoot` sessions resolve to no policy documents, so the gate denies them every action (observed for federation). | [3737](../BUGS.md) |
| Service control and resource control policies | AWS Organizations stores them, but `iamAuthorizeWithContext` never evaluates them. | [3738](../BUGS.md) |
| AWS managed policies | None are served. Attaching `arn:aws:iam::aws:policy/…` grants nothing. | 3662 |
| Role `MaxSessionDuration` | `stsDurationSeconds` clamps to 43200 and ignores the role's maximum. A 12-hour session of a role with a 1-hour maximum succeeds (observed). | [3737](../BUGS.md) |
| MFA | `stsRequestMFA` checks only that `SerialNumber` and `TokenCode` are present. It does not verify the TOTP code. | [3737](../BUGS.md) |
| Streaming payloads | `chunk-signature` values and trailing checksums are not verified (`awschunked.go`). | [3739](../BUGS.md) |
| AWS KMS key policies | A key policy combines with identity policies like any resource policy. An identity policy alone grants key use even when the key policy does not delegate to the account, where KMS requires the key policy. | [3739](../BUGS.md) |
| Policy variables | Substituted in `Resource` / `NotResource` only, not in `Condition` values. | [3739](../BUGS.md) |
| `SimulatePrincipalPolicy` | Reads role policies only. A user ARN evaluates no policies (observed: `implicitDeny` for an action `alice` holds). `MatchedStatements` is always empty. | [3739](../BUGS.md) |
| `DecodeAuthorizationMessage`, `GetAccessKeyInfo` | The first base64-decodes its input and answers a fixed body otherwise. The simulator never encodes a denial for it to decode. The second answers the simulator's account for any key. | [3739](../BUGS.md) |
| EC2 instance profile trust | The metadata service serves the profile role's session without checking that the trust policy admits `ec2.amazonaws.com`. `iam:PassRole` is checked at launch. | [3739](../BUGS.md) |
| IAM entity IDs | `CreateUser` and `CreateRole` mint IDs such as `AIDA175AF85B-BCFA-40` from a UUID, not AWS's 21-character uppercase alphanumeric form (observed). | [3740](../BUGS.md) |
| Service-linked roles | Any service principal is accepted, and deletion completes at once (`iam_slr_oidc.go`). | [3740](../BUGS.md) |
| Policy encoding | Policy documents are form-encoded (`+` for a space). | 3661 |
| Condition keys | 615 of 4,310 measured (action, key) pairs are not built. REST services are not measured. | 2965, 3584 |
| Directory-bucket actions | Some are authorized as `s3:` actions. | 3586 |
| Amazon EFS client authorization | `elasticfilesystem:Client*` is never checked on mount. | 3583 |
| Tag-on-create for REST services | Not authorized for six REST services. | 3160 |
| Metadata at link-local addresses | `169.254.169.254` and `169.254.170.2` are not routable from a workload. | 3485 |
| Untrusted-role messages | The messages for an untrusted role are unsourced. | 3560 |

## Google Cloud

The Google Cloud simulator (`simulator-gcp/`) serves Identity and Access Management (IAM), the IAM Service Account Credentials API, the Security Token Service, the OAuth 2.0 token endpoint and the Compute Engine metadata server. Every token it issues is an RS256 JWT it signs itself, and every data-plane request must carry one. IAM policy then decides every call: the caller performs a method only when a policy on the resource or one of its ancestors grants the permission the method requires. Read [Enforcement](#google-cloud-enforcement) for how each method's permission is derived and what a refusal looks like.

### Principals

| Principal | Created by | Token path | Member string a binding matches |
|---|---|---|---|
| Service account | `projects.serviceAccounts.create` (`iam_service_accounts.go`) | JWT-bearer grant, impersonation, metadata server | `serviceAccount:{email}` |
| Compute Engine default service account | Seeded with each project (`iamEnsureComputeDefaultServiceAccount`) | Same | `serviceAccount:{number}-compute@developer.gserviceaccount.com` |
| Workforce Identity Federation subject | Security Token Service exchange (`sts.go`) | `POST /v1/token` | `principal://iam.googleapis.com/locations/{l}/workforcePools/{p}/subject/{s}` |
| Bootstrap service account `sockerless-bootstrap@sockerless.iam.gserviceaccount.com` | Seeded at startup in project `sockerless` (`iamSeedBootstrapServiceAccount`, `iam_bootstrap.go`), with the published key `49c4c9ee1dffb89ca13534183ea5e484e46abaaa`, and bound to `roles/owner` on `organizations/123456789012` (`iamSeedBootstrapOwner`) | JWT-bearer grant signed with that key | `serviceAccount:sockerless-bootstrap@sockerless.iam.gserviceaccount.com` |
| Service agents, such as `service-{number}@gcp-sa-eventarc.iam.gserviceaccount.com` | Bound to their roles with each project (`iamSeedProjectGrants`) | Minted in-process when a service acts for one of its resources (`gcpCallAs`) | `serviceAccount:{email}` |

The simulator holds no users and no groups, because it serves no Cloud Identity or Google Workspace directory. `validateIAMMembers` (`iam.go`) accepts `user:`, `group:` and `domain:` members, but no token names a user, group or domain, so those bindings never match a caller.

#### Service accounts and keys

`registerIAMServiceAccounts` serves create, get, list, patch, update, delete, `undelete` (inside the 30-day retention window, `iamServiceAccountRetention`), `enable` and `disable`. A disabled account cannot exchange an assertion at the token endpoint, and its keys cannot sign a V4 signed URL.

`registerIAMServiceAccountKeys` (`iam_service_account_keys.go`) serves the keys collection:

| Method | Behaviour |
|---|---|
| `keys.create` | Generates an RSA 2048 key and returns `privateKeyData`, a base64 `TYPE_GOOGLE_CREDENTIALS_FILE` (`gcpMakeSAKeyJSON`). Stores only the public half (`iamSAKeyPublics`). The organization policy `iam.disableServiceAccountKeyCreation` refuses it with `FAILED_PRECONDITION`. |
| `keys.upload` | Takes a PEM X.509 certificate holding an RSA 1024 or 2048 key. Derives the key ID from the public key (`serviceAccountKeyID`) and records `keyOrigin: USER_PROVIDED`. `iam.disableServiceAccountKeyUpload` refuses it. |
| `keys.disable` / `keys.enable` | Flips `Disabled`. A disabled key signs nothing (`serviceAccountSigned` skips it). The system-managed key answers `INVALID_ARGUMENT`. |
| `keys.get` / `keys.list` / `keys.delete` | Serve the stored metadata, with `publicKeyData` when the caller names a `publicKeyType`. |

Each account also has one system-managed key (`serviceAccountSigningKey`). It is generated on first use, persisted in `iam_sa_system_keys`, and listed with `keyType: SYSTEM_MANAGED`. `signBlob` and `signJwt` sign with it. Its private half never leaves the simulator.

#### Workload Identity Federation and workforce pools

`iam_pools.go` serves workload identity pools, their providers, namespaces, managed identities and keys, workforce pools with their providers, subjects, SCIM tenants and keys, and OAuth clients with their credentials. Each mutating method returns a long-running operation that is already `done` (`newIAMLRO`). The resources are stored as the caller sent them (`iamResources`).

Only workforce pool providers take part in a token exchange. The Security Token Service resolves an audience of the form `//iam.googleapis.com/locations/{l}/workforcePools/{p}/providers/{id}` and nothing else (`workforceProviderFromAudience`). A workload identity pool is configuration only. `workforcePools.subjects.revokeSessions` revokes every token issued to that subject up to the current second (`workforceSessionRevoked`).

#### Member strings

`validateIAMMembers` accepts `allUsers`, `allAuthenticatedUsers` and the prefixes `user:`, `serviceAccount:`, `group:`, `domain:`, `principal:`, `principalSet:`, `projectOwner:`, `projectEditor:` and `projectViewer:`. The email and domain forms must contain a dot. Anything else answers `400 INVALID_ARGUMENT`.

`gcpMemberMatches` (`iam_permissions.go`) decides whether a member covers a caller:

| Member | Matches |
|---|---|
| `allUsers` | Every caller, with or without a token |
| `allAuthenticatedUsers` | Every caller whose token names a principal |
| `serviceAccount:{email}` | A token whose `sub` is that email (`gcpSubjectPrincipal`) |
| `principal://iam.googleapis.com/locations/…/subject/{s}` | A workforce token for that subject |
| `projectOwner:{p}`, `projectEditor:{p}`, `projectViewer:{p}` | A caller that holds `roles/owner`, `roles/editor` or `roles/viewer` on project `p`, granted there or on an ancestor (`gcpProjectGrantsRole`) |
| `principalSet://iam.googleapis.com/locations/…/workforcePools/{pool}/*` | A workforce token for any subject of that pool |
| Other `principalSet://…`, `user:`, `group:`, `domain:` | Nobody today |

### Tokens

All issuers sign with one process-wide RSA key (`accessSigner`, `token_signing.go`). With persistence on, the simulator stores the key in `token_signing_keys`, so tokens and the JWKS `kid` survive a restart. `GET /.well-known/openid-configuration` and `GET /.well-known/jwks.json` publish the key (`registerTokenDiscovery`).

| Token | Minted by | `iss` | `aud` | `sub` |
|---|---|---|---|---|
| Access token | `signAccessToken` | `https://accounts.google.com` | `https://sockerless-sim.googleapis.com/` | Service-account email, or `locations/…/workforcePools/{p}/subject/{s}` |
| ID token | `signInvokeIDToken`, `signIdentityToken`, `signServiceAccountIDToken` | `https://accounts.google.com` | The requested audience | Service-account email, also in `email` and `azp` |

Real Google access tokens are opaque. A client must treat the simulator's the same way; only the audience above separates an access token from an ID token.

#### Data-plane bearer verification

`bearerAuthMiddleware` (`token_signing.go`) wraps the REST mux. For every request that a Google API method routes, `verifyRequestBearer` requires `Authorization: Bearer`, verifies the signature, issuer, audience and expiry (`verifiedAccessTokenClaims`), and rejects a revoked workforce session. A failure answers `401 UNAUTHENTICATED`. Unrouted paths answer `404` before the credential is examined (`publishesAPIMethod`).

`isAuthExempt` lists what a client reaches without a token: the token endpoints, `/v1/introspect`, Discovery documents, `/.well-known/`, `/health`, `/computeMetadata/` (gated by `Metadata-Flavor` instead), the console under `/ui/` and `/auth/`, the Artifact Registry Docker data plane (its own token service, `ar_dataplane_auth.go`), and `*.run.app` hosts (the Cloud Run front end authenticates invokers itself). The Cloud Storage XML data plane (`GET /{bucket}/{object}`) checks the bearer itself, or a V4 signed URL in its place (`gcsSignedURLAuthorized`), whose signer it then authorizes as the caller.

The gRPC listener (the HTTP port plus two, or `SIM_GCP_GRPC_PORT`) verifies the bearer in each call's `authorization` metadata with the same check (`grpc_auth.go`), and refuses a missing or invalid one with `UNAUTHENTICATED` and an `ErrorInfo` detail. Google's client libraries send credentials only over TLS, so `SIM_GCP_GRPC_TLS_CERT` and `SIM_GCP_GRPC_TLS_KEY` make the port a TLS front end that serves both gRPC and REST, as Google's hosts do.

A bearer may also be a JWT a service account signs itself, which google-auth sends by default (`sa_self_signed_jwt.go`). It must be RS256 with a `kid` naming a key IAM registered and has not disabled, for an account that is not disabled; carry the account's email as `iss` and `sub`; name `https://<api>.googleapis.com/` as `aud`, or carry a `scope` claim; and live at most an hour. The caller is then that service account.

#### OAuth 2.0 token endpoint

`registerOAuth2` (`oauth2.go`) serves `POST /token`, `/oauth2/v4/token` and `/o/oauth2/token`, the three hosts Google publishes the grant on. For `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer` with an `assertion`, `verifyServiceAccountAssertion`:

1. Splits the JWT and requires `alg: RS256`.
2. Resolves `iss` to a stored service account, refusing an unknown or disabled one.
3. Verifies the signature against the key `kid` names, or against every enabled user-managed key when there is no `kid` (`serviceAccountSigned`).
4. Requires a non-zero, unexpired `exp`.

It does not pin `aud`, because a client pointed at the simulator puts the simulator's token URL there. A failure answers HTTP 400 with the RFC 6749 body, for example `{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`. On success it returns an access token for the account. When the assertion carries `target_audience`, it also returns an `id_token` with that audience. `gcloud auth print-identity-token` and `google.golang.org/api/idtoken` use this path.

A client's first token comes from the **bootstrap service account**, the
Google Cloud counterpart of the AWS simulator's `test` key and the Azure
simulator's bootstrap application. The simulator seeds the account and a key
whose RSA private key is published, in `simulator-gcp/iam_bootstrap.go` and
`testutil/gcpbootstrap`. IAM reports both through `serviceAccounts.get` and
`keys.list`, as `USER_MANAGED`. `go run
github.com/e6qu/sockerless-cloud/testutil/cmd/gcp-bootstrap-key@main -o
bootstrap-key.json` writes the key's credential file, with `token_uri`
pointing at the simulator. Each client then signs in the documented way:

| Client | How |
|---|---|
| gcloud | `gcloud auth activate-service-account --key-file=bootstrap-key.json`, and `gcloud config set auth/token_host http://localhost:4567/token` (bq reads its token from `auth/token_host`) |
| Cloud Client Libraries (Go, Python, …) | `GOOGLE_APPLICATION_CREDENTIALS=bootstrap-key.json` (Application Default Credentials) |
| Terraform | the Google provider's `credentials` argument |

A JWT-bearer request with no `assertion` gets `invalid_request` ("Missing
required parameter: assertion"), and any other `grant_type` gets
`unsupported_grant_type`, both with HTTP 400.

#### Service Account Credentials API

The IAM Service Account Credentials methods share IAM's path, `POST /v1/projects/{project}/serviceAccounts/{email}:{verb}`. Point gcloud's `api_endpoint_overrides/iamcredentials` at the simulator.

| Verb | Returns |
|---|---|
| `generateAccessToken` | `accessToken` whose `sub` is the impersonated account, and `expireTime` in whole seconds. `lifetime` defaults to 3600s. The cap is 3600s, or 43200s when `constraints/iam.allowServiceAccountCredentialLifetimeExtension` lists the account (`iamAccessTokenLifetime`). |
| `generateIdToken` | `token` with the requested `audience`, plus `email` and `email_verified` when `includeEmail` is set. |
| `signBlob` | `keyId` and `signedBlob`, an RSASSA-PKCS1-v1_5 SHA-256 signature by the system-managed key. |
| `signJwt` | `keyId` and `signedJwt`, an RS256 JWS by the system-managed key. |

The caller needs `iam.serviceAccounts.getAccessToken` (`generateAccessToken`), `getOpenIdToken` (`generateIdToken`), `signBlob` or `signJwt` on the target account, which the Service Account Token Creator role (`roles/iam.serviceAccountTokenCreator`) grants and no basic role does: even an Owner grants itself that role on the account first. The simulator does not follow `delegates`, and impersonates a disabled account. `getAllowedLocations` answers an unrestricted trust boundary (`encodedLocations: "0x0"`).

#### Security Token Service

`handleSTSTokenExchange` (`sts.go`) takes `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` with a `subject_token` of type `jwt` or `id_token` and a workforce provider `audience`. `federateWorkforceSubject`:

1. Reads the provider's `oidc.issuerUri` and `oidc.clientId`.
2. Runs OpenID Connect discovery against the issuer, fetches its JWKS, and verifies signature, issuer and expiry (`verifyWorkforceSubjectToken`).
3. Requires the token's audience to include `clientId`.
4. Takes the subject from the `google.subject` mapping when it has the form `assertion.<claim>`, and from `sub` otherwise (`mapWorkforceSubject`).
5. Returns a one-hour access token for `locations/{l}/workforcePools/{p}/subject/{s}`.

Errors use the RFC 6749 body: `invalid_request`, `invalid_target` for an unknown provider, and `invalid_grant` for a failed verification. The console's Shauth sign-in calls the same function.

#### Introspection and tokeninfo

`POST /v1/introspect` (`handleSTSIntrospect`) implements RFC 7662. It requires client authentication, HTTP Basic or a `client_id` field, but does not check the secret. A token the simulator minted answers `active: true` with `sub`, `scope`, `iss`, `exp` and `iat`. A workforce token adds `username: principal://iam.googleapis.com/…`. Any other token answers `{"active": false}`.

The simulator does not serve `oauth2.googleapis.com/tokeninfo`, `/oauth2/v3/certs` or `/robot/v1/metadata/x509/{email}`. A relying party verifies ID tokens against `/.well-known/jwks.json`.

### Authorization

#### Policies and where they live

A policy (`IAMPolicy`, `iam.go`) holds `bindings`, `auditConfigs`, `etag` and `version`. Two stores hold them: `iam_project_policies` for projects (key `project/{id}`), and `iam_resource_policies` for everything else (keys such as `folder/{id}`, `organization/{id}`, `bucket/{name}`, `serviceAccount/{email}`, or the resource's relative name).

`handleResourceIAM` serves the AIP-141 triple for projects, folders, organizations, service accounts, and every resource reached through the catch-all `POST /v1/{resource...}:{verb}`. Pub/Sub, Cloud KMS, Secret Manager, API Gateway, Bigtable and the other services with their own verb dispatchers call it too. Cloud Storage serves bucket, managed folder and object policies on its JSON API paths (`GET|PUT /storage/v1/b/{bucket}/iam`, `gcs_object_iam.go`, `gcs_folders.go`).

| Behaviour | Implementation |
|---|---|
| `getIamPolicy` on a resource without a policy | Stores and returns an empty policy, version 1, so its etag stays stable. A bucket gets its default legacy-role policy (`gcsSeedDefaultBucketPolicy`). |
| `setIamPolicy` etag | A non-empty etag that differs from the stored one answers `409 ABORTED` (`gcpIAMETagConflict`); Cloud Storage bucket policies answer Cloud Storage's own `412 Precondition Failed` (`conditionNotMet`). An empty etag overwrites. Every write gets a fresh etag. Writes are serialized by `gcpIAMPolicyWrites`. |
| `updateMask` | The default is `bindings,etag`. `auditConfigs` change only when the mask names them. |
| `version` | Stored as sent, 1 when absent. `options.requestedPolicyVersion` is ignored. |
| `auditConfigs` | Validated against `ADMIN_READ`, `DATA_READ` and `DATA_WRITE` (`validateIAMAuditConfigs`). |

#### Roles and permissions

`gcpRoleGrants` (`iam_catalog.go`) decides whether a role includes a permission:

- **Predefined roles** come from `iam_catalog_vendored.json`, which `scripts/gen-gcp-iam-catalog.go` compiles from the role and permission pages of every served service (`specs/cloud-api/gcp/iam-roles-*.md.gz`): 355 roles, service agent roles among them, and the 2,600 permissions the services define. `roles.get` and `roles.list` serve them with the etag `AA==` Google gives every predefined role, and `permissions.queryTestablePermissions` answers the permissions.
- **Basic roles** (`roles/owner`, `roles/editor`, `roles/viewer`): Google lists their permissions only in its console, so `gcpBasicRoleGrants` follows the roles overview (`iam-roles-overview.md.gz`). Viewer holds the read-only permissions (verbs `get…`, `list…`, `search…`, `read`, `select`, `query…`, `lookup…`), Editor every permission except those that manage access (`setIamPolicy`, `iam.roles.*` writes), tag bindings and billing, and Owner every permission. Neither Owner nor Editor holds the service account credential permissions (`getAccessToken`, `getOpenIdToken`, `signBlob`, `signJwt`, `implicitDelegation`). In Cloud Storage a basic role holds only the permissions `storage-iam-roles.md.gz` lists as intrinsic; a project's members reach its buckets and objects through the convenience members of each bucket's policy.
- **Custom roles**: `projects.roles.*` and `organizations.roles.*` (`registerCustomRoles`) store `includedPermissions`, `stage` and a soft-delete flag. A deleted role, or one at stage `DISABLED`, grants nothing.

A role the simulator does not define grants nothing.

#### Conditions

`gcpConditionHolds` (`iam_conditions.go`) compiles a binding's `condition.expression` with cel-go and caches the program. The environment offers:

- `request.time`;
- `resource.name`, `resource.type` and `resource.service`, for the resource types in `gcpIAMResourceTypes`, taken from the vendored `specs/cloud-api/gcp/iam-conditions-resource-attributes.md.gz`;
- `resource.matchTag`, `matchTagId`, `hasTagKey` and `hasTagKeyId`, over the resource's effective Resource Manager tags.

An expression that fails to compile or evaluate is false, so its binding grants nothing.

#### Evaluation and inheritance

`gcpPermissionsHeldBy` collects the roles of every binding that both matches the caller and whose condition holds, then answers the requested permissions those roles include, in request order. Policies combine by union (`gcpPermissionsHeldUnder`). The simulator serves no deny policies (IAM v2) and no principal access boundary enforcement.

`gcpPolicyChain` (`iam_gate_policies.go`) gathers the policies that govern a resource named by its relative name, outermost first: the organization's and each folder's above the project (`crmOrgPolicyAncestry`), the project's, then the policy of each resource on the path down to it, as a key ring's policy governs its keys. A Cloud Storage bucket adds its own policy and, while uniform bucket-level access is off, its ACL read as legacy roles; an object adds its policy and ACL (`gcsBucketPolicies`, `gcsObjectPolicies`). A resource outside any project, folder or organization, such as a billing account or a global operation, inherits the organization `organizations/123456789012`.

Every `testIamPermissions`, the Cloud Run invoker check, Memorystore for Redis IAM `AUTH` and the Cloud Storage service agent's publish check evaluate the same chain.

Google grants roles the simulator grants as real bindings, visible through `getIamPolicy`:

| Member | Role | Where | Seeded by |
|---|---|---|---|
| Bootstrap service account | `roles/owner` | `organizations/123456789012` | `iamSeedBootstrapOwner`, at startup |
| Compute Engine default service account, Google APIs Service Agent (`{number}@cloudservices.gserviceaccount.com`) | `roles/editor` | Each project (`compute-service-accounts.md.gz`) | `iamSeedProjectGrants`, when the project is created |
| Each served service's agent, such as `service-{number}@gcp-sa-pubsub.iam.gserviceaccount.com` | The role `iam-service-agents.md.gz` names, such as `roles/pubsub.serviceAgent` | Each project | `iamSeedProjectGrants` |

A service acting for one of its resources calls the target API as its agent (`gcpCallAs`), so Cloud Scheduler publishes with `roles/cloudscheduler.serviceAgent` and Eventarc builds its transport topic with `roles/eventarc.serviceAgent`. A workload calls as the account its metadata server hands it a token for. Writing a project's or the organization's policy without these bindings removes them, as it does on Google Cloud.

<a id="google-cloud-enforcement"></a>

#### Enforcement

`iamGateMiddleware` (REST, inside `bearerAuthMiddleware`) and `iamGateUnaryInterceptor` / `iamGateStreamInterceptor` (gRPC) decide every authenticated call (`iam_gate.go`). A gRPC call is decided as the REST call its `google.api.http` binding spells; a streaming call is decided on its first message.

1. **Method.** The call resolves to the Discovery method that describes its path (`gateMatchDiscovery`, over every embedded Discovery document). Where two APIs publish one path, the host decides, then the claims `auditRESTClaims` records; an operations path several APIs share admits a caller holding either API's permission.
2. **Permission**, from the first source that names one (`gateChecks`, `iam_gate_checks.go`):
   1. the method tables Cloud Storage (`storage-iam-json.md.gz`) and Pub/Sub (`pubsub-access-control.md.gz`) publish, compiled into the catalog with what each permission is checked on (the project, a source or destination object, the topic a request names);
   2. `gateMethodPermissions`, the methods whose identifiers do not spell their permission, each citing its vendored page: Firestore's document methods (`firestore-security-iam.md.gz`), Spanner's session methods (`spanner-iam.md.gz`), the Service Account Credentials methods, Cloud Logging's `entries.write`;
   3. the service's vendored audit-logging page (`audit_methods_vendored.json`), for Cloud Run, Cloud SQL, BigQuery, Cloud Logging, Resource Manager, Service Usage, Serverless VPC Access, API Gateway and Dataflow, and their operations and locations mixins;
   4. Google's `service.resource.verb` convention over the method's RPC (`auditRPC.permission`, with the RPC exceptions `auditRPCPermissions` lists) or Discovery identifier (`gateConventionPermission`).

   A permission counts only when a served service defines it in the vendored catalog; Cloud Storage's footnoted permissions, which a method checks only in the case the footnote names, are not required.
3. **Resource.** The permission is checked on the relative name the path addresses (a create on its parent collection), or the target its table names.
4. **Decision.** The caller must hold every permission under `gcpPolicyChain` of its resource. A Cloud Storage bucket that does not exist is admitted, so the handler answers 404, as Cloud Storage does whoever asks. So is a project that does not exist on Compute Engine, Cloud Storage and Pub/Sub (`gateAnswersUnknownProject`), which resolve the project before they authorize; every other API refuses it, as a project the caller may not read.

A method no source gives a permission for is **unsourced**: only a principal bound to `roles/owner` on the resource or an ancestor performs it, since Owner holds every permission Google defines. A permission a method table names that no vendored role page lists (Cloud Storage's `storage.objects.viewFullContext`) is unsourced the same way. [BUGS.md](../BUGS.md) lists the unsourced methods by service.

Some methods need no permission: every `testIamPermissions`, the IAM catalogs (`roles.get` and `roles.list` on predefined roles, `queryTestablePermissions`, `queryGrantableRoles`, `lintPolicy`, `queryAuditableServices`), and Resource Manager's `projects.list`, `projects.search`, `folders.search` and `organizations.search`, as well as `projects.create` without a parent.

A refused call answers 403 in its API's shape:

| API | Refusal |
|---|---|
| Cloud Storage | `{"error":{"code":403,"message":"{email} does not have storage.objects.delete access to the Google Cloud Storage object. Permission 'storage.objects.delete' denied on resource (or it may not exist).","errors":[{"domain":"global","reason":"forbidden",…}]}}`; a bucket or project names `bucket` or `Google Cloud project` |
| Compute Engine | `Required 'compute.instances.get' permission for 'projects/{p}/zones/{z}/instances/{i}'`, reason `forbidden` |
| BigQuery | `Access Denied: Table p:d.t: Permission bigquery.tables.getData denied on table p:d.t (or it may not exist).`, reason `accessDenied` |
| Pub/Sub | `PERMISSION_DENIED`, `User not authorized to perform this action.` |
| Spanner | `PERMISSION_DENIED`, `Caller is missing IAM permission spanner.databases.select on resource projects/…/databases/d.` |
| Firestore | `PERMISSION_DENIED`, `Missing or insufficient permissions.` |
| Resource Manager, and any unsourced method | `PERMISSION_DENIED`, `The caller does not have permission` |
| Every other API | `PERMISSION_DENIED`, `Permission '{permission}' denied on resource '{name}' (or it may not exist).`, with an `ErrorInfo` of reason `IAM_PERMISSION_DENIED`, domain `iam.googleapis.com` and metadata `permission` and `resource` |

Over gRPC the call fails with `codes.PermissionDenied` and the same message, and the `ErrorInfo` detail where the REST answer carries one.

A refused call reaches no handler, so Cloud Audit Logs writes no entry for it.

The Cloud Storage XML data plane checks `storage.objects.get` itself, through the same bucket and object policies. The Artifact Registry Docker data plane (`arAuthorizeV2`, which maps each request to `downloadArtifacts`, `uploadArtifacts` or `deleteArtifacts` but refuses only anonymous tokens) and the Cloud Run front end decide their own callers; the front end checks `run.routes.invoke` (`cloudRunInvokerAuthorized`).

### Workload identity

#### Metadata server

`registerComputeMetadata` (`metadata.go`) serves `/computeMetadata/v1/` to Compute Engine instances, Cloud Run services, jobs and worker pools, Cloud Run functions (which run as Cloud Run services), and Cloud Build steps. `gceMetadataAdmit` (`metadata_workload.go`) identifies the caller by source address. It looks up a Compute Engine instance by private IP, or the container that owns the address by its labels, and reads the resource's service account. It answers 404 to any other caller and 403 to a read without `Metadata-Flavor: Google`.

| Path under `instance/service-accounts/{default\|email}/` | Answer |
|---|---|
| `email` | The resource's service account, or the project's Compute Engine default service account when it names none |
| `token` | `{"access_token","expires_in","token_type"}`, a one-hour access token for that account |
| `identity?audience=…` | A one-hour ID token with that audience (`signIdentityToken`); 400 without `audience` |
| `scopes` | Always `https://www.googleapis.com/auth/cloud-platform` |
| `aliases` | The account's aliases |

An account the workload does not run as answers 404. `?wait_for_change=true` long-polls any metadata path until its content's ETag differs from `last_etag` (or from the content when the read arrived) or `timeout_sec` passes (`gceMetadataServe`, `metadata_wait.go`). Each store the metadata tree renders from announces its writes on `gceMetadataChanges`.

#### How a workload reaches it

Every engine network a workload joins runs a network sidecar (`networkSidecars`, `dns_resolver.go`). `gcpWorkloadNetwork.apply` (`gcp_networks.go`) points the container's DNS at the sidecar, adds hosts entries for `metadata.google.internal` and `metadata`, and sets `GCE_METADATA_HOST`, `GCE_METADATA_IP` and `GCE_METADATA_ROOT` to the sidecar's address. The simulator's Cloud DNS resolver also answers `metadata.google.internal` with that address (`cloudDNSCatalog`). The sidecar forwards each metadata request with its own token and the workload's address. `sim.SidecarTrust` trusts that address only with a registered token, so a workload cannot claim another's address.

#### Cloud Run invoker check

`cloudRunInvokerAuthorized` admits a request when:

1. The service has its invoker IAM check disabled; or
2. The combined policies grant `run.routes.invoke` to `allUsers`; or
3. The credential in `X-Serverless-Authorization`, or else `Authorization`, is an ID token signed by the simulator whose audience is a service URL (with or without a trailing slash) or a custom audience (`cloudRunVerifyIDToken`), and its `email` (or `sub`) holds `run.routes.invoke`.

An access token is not an ID token and fails step 3. The container receives the credential header with the JWT signature replaced by `SIGNATURE_REMOVED_BY_GOOGLE` (`cloudRunForwardedHeader`). Cloud Scheduler and Cloud Tasks attach `oidcToken` or `oauthToken` credentials for their target's service account (`gcpPushAuthorization`, `push_auth.go`).

### Cloud Audit Logs

`auditLogMiddleware` (REST) and `auditUnaryInterceptor` (gRPC) write Cloud Audit Logs entries to `projects/{p}/logs/cloudaudit.googleapis.com%2Factivity` and `%2Fdata_access` (`emitAuditLog`, `audit_logs.go`). Eventarc audit-log triggers receive them.

- Admin Activity entries are always written. Data Access entries are written only when the project policy's `auditConfigs` enable the log type for the service or `allServices`, minus `exemptedMembers` (`auditDataAccessEnabled`).
- `authenticationInfo` carries `principalEmail` and `principalSubject` from the bearer token (`auditCallerFromCredential`).
- `authorizationInfo` names the permission from the vendored table or the service's own table, always with `granted: true`: a call the IAM gate refuses never reaches the audit layer.
- No service writes Policy Denied entries.

[BUGS.md 3119](../BUGS.md) lists the services and methods that write no entries, including `SignBlob`, `SignJwt` and every `TestIamPermissions`.

<!-- doctest: part name=gcp -->
### Examples

Install the simulator from the clone:

<!-- doctest: run -->
```sh
go -C "$SOCKERLESS_SRC" install ./simulator-gcp
```

Start it in a terminal of its own:

<!-- doctest: background id=gcp ready="Listening on" -->
```sh
simulator-gcp
```

It listens on port 4567. In your first terminal, write the bootstrap
credential file and sign gcloud in with it. `CLOUDSDK_CONFIG` gives these
examples a gcloud configuration of their own, so your usual one stays as it
is. The operator token `OP` and two curl headers, `A` and `J`, serve the
examples that follow:

<!-- doctest: run expect="Activated service account credentials for: \\[sockerless-bootstrap@sockerless\\.iam\\.gserviceaccount\\.com\\]" -->
```sh
export CLOUDSDK_CONFIG="$PWD/gcloud-config" S=http://localhost:4567
go -C "$SOCKERLESS_SRC" run ./testutil/cmd/gcp-bootstrap-key -o "$PWD/bootstrap-key.json"
gcloud auth activate-service-account --key-file=bootstrap-key.json
gcloud config set auth/token_host "$S/token"
OP=$(gcloud auth print-access-token)
export OP A="Authorization: Bearer $OP" J='Content-Type: application/json'
```

Outside a clone, `go run
github.com/e6qu/sockerless-cloud/testutil/cmd/gcp-bootstrap-key@main -o
bootstrap-key.json` writes the same file.

#### A service account's own token, through the real JWT-bearer grant

Create the service account `reader` and a key for it, then sign a JWT with the
key and exchange it at the token endpoint, as the client libraries do. `RT`
keeps `reader`'s access token:

<!-- doctest: run expect="\"token_type\": \"Bearer\"" -->
```sh
curl -s -X POST "$S/v1/projects/sockerless/serviceAccounts" -H "$A" -H "$J" \
  -d '{"accountId":"reader"}'
curl -s -X POST "$S/v1/projects/sockerless/serviceAccounts/reader@sockerless.iam.gserviceaccount.com/keys" \
  -H "$A" -H "$J" -d '{}' | jq -r .privateKeyData | base64 -d > reader-key.json

b64() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
jq -r .private_key reader-key.json > reader.pem
NOW=$(date +%s)
H=$(jq -cj '{alg:"RS256",typ:"JWT",kid:.private_key_id}' reader-key.json | b64)
C=$(jq -cj --arg aud "$S/token" --argjson now "$NOW" \
  '{iss:.client_email,scope:"https://www.googleapis.com/auth/cloud-platform",aud:$aud,iat:$now,exp:($now+3600)}' \
  reader-key.json | b64)
SIG=$(printf '%s.%s' "$H" "$C" | openssl dgst -sha256 -sign reader.pem | b64)
curl -s -X POST "$S/token" \
  --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  --data-urlencode "assertion=$H.$C.$SIG" > reader-token.json
jq '{token_type, expires_in}' reader-token.json
RT=$(jq -r .access_token reader-token.json)
export H C RT
```

The token endpoint verifies the signature. An assertion whose signature covers
other content is refused:

<!-- doctest: run expect="\\{\"error\":\"invalid_grant\",\"error_description\":\"Invalid JWT Signature\\.\"\\}" -->
```sh
BAD=$(printf 'other content' | openssl dgst -sha256 -sign reader.pem | openssl base64 -A | tr '+/' '-_' | tr -d '=')
curl -s -X POST "$S/token" \
  --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  --data-urlencode "assertion=$H.$C.$BAD"
```

```text
{"error":"invalid_grant","error_description":"Invalid JWT Signature."}
```

gcloud does the same exchange after `gcloud config set auth/token_host
$S/token`, `gcloud config set api_endpoint_overrides/iam $S/` and `gcloud auth
activate-service-account --key-file=reader-key.json`.

#### Grant a role on a bucket and test it

Create a bucket with uniform bucket-level access, read its policy, add a
binding for `reader`, and write the policy back:

<!-- doctest: run expect="\\{\"role\":\"roles/storage\\.objectViewer\",\"members\":\\[\"serviceAccount:reader@sockerless\\.iam\\.gserviceaccount\\.com\"\\]\\}" -->
```sh
curl -s -X POST "$S/storage/v1/b?project=sockerless" -H "$A" -H "$J" \
  -d '{"name":"iam-demo-bucket","iamConfiguration":{"uniformBucketLevelAccess":{"enabled":true}}}' \
  | jq -c '{name, iamConfiguration}'
curl -s "$S/storage/v1/b/iam-demo-bucket/iam" -H "$A" \
  | jq '.bindings += [{"role":"roles/storage.objectViewer",
        "members":["serviceAccount:reader@sockerless.iam.gserviceaccount.com"]}]' > policy.json
curl -s -X PUT "$S/storage/v1/b/iam-demo-bucket/iam" -H "$A" -H "$J" -d @policy.json \
  | jq -c '.bindings[-1]'
```

The write gave the policy a new etag, so writing `policy.json` again, with the
etag it was read at, is refused:

<!-- doctest: run expect="\"reason\":\"conditionNotMet\"" expect="HTTP 412" -->
```sh
curl -s -w '\nHTTP %{http_code}\n' -X PUT "$S/storage/v1/b/iam-demo-bucket/iam" \
  -H "$A" -H "$J" -d @policy.json
```

Ask, as `reader`, which of two permissions it holds on the bucket:

<!-- doctest: run expect="\\{\"kind\":\"storage#testIamPermissionsResponse\",\"permissions\":\\[\"storage\\.objects\\.get\"\\]\\}" -->
```sh
curl -s "$S/storage/v1/b/iam-demo-bucket/iam/testPermissions?permissions=storage.objects.get&permissions=storage.objects.delete" \
  -H "Authorization: Bearer $RT" | jq -c .
```

```text
{"kind":"storage#testIamPermissionsResponse","permissions":["storage.objects.get"]}
```

A request without a token is refused:

<!-- doctest: run expect="\"status\":\"UNAUTHENTICATED\"" expect="HTTP 401" -->
```sh
curl -s -w '\nHTTP %{http_code}\n' \
  "$S/storage/v1/b/iam-demo-bucket/iam/testPermissions?permissions=storage.objects.get"
```

Upload an object as the operator. `reader` reads it, and its delete is refused
for the permission `testPermissions` reported missing:

<!-- doctest: run expect="(?m)^hello$" expect="reader@sockerless\\.iam\\.gserviceaccount\\.com does not have storage\\.objects\\.delete access" expect="HTTP 403" -->
```sh
echo hello > hello.txt
curl -s -X POST "$S/upload/storage/v1/b/iam-demo-bucket/o?uploadType=media&name=hello.txt" \
  -H "$A" -H 'Content-Type: text/plain' --data-binary @hello.txt | jq -c '{name, size}'
curl -s "$S/storage/v1/b/iam-demo-bucket/o/hello.txt?alt=media" -H "Authorization: Bearer $RT"
curl -s -w '\nHTTP %{http_code}\n' -X DELETE \
  "$S/storage/v1/b/iam-demo-bucket/o/hello.txt" -H "Authorization: Bearer $RT"
```

Bind `roles/storage.objectAdmin` the same way and the delete answers 204.

#### Impersonate a service account

Basic roles hold no impersonation permission, so even the bootstrap Owner
first takes the Service Account Token Creator role on `reader`. Then mint a
ten-minute access token for it:

<!-- doctest: run expect="\"accessToken\":\"eyJ" expect="\"expireTime\":\"[0-9T:-]+Z\"" -->
```sh
gcloud config set api_endpoint_overrides/iam $S/
gcloud iam service-accounts add-iam-policy-binding reader@sockerless.iam.gserviceaccount.com \
  --member=serviceAccount:sockerless-bootstrap@sockerless.iam.gserviceaccount.com \
  --role=roles/iam.serviceAccountTokenCreator
curl -s -X POST "$S/v1/projects/-/serviceAccounts/reader@sockerless.iam.gserviceaccount.com:generateAccessToken" \
  -H "$A" -H "$J" -d '{"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"600s"}'
```

```text
{"accessToken":"eyJ…","expireTime":"2026-10-26T01:34:24Z"}
```

A lifetime past the one-hour cap is refused:

<!-- doctest: run expect="Requested lifetime 7200s exceeds the maximum lifetime of 3600s" expect="\"status\":\"INVALID_ARGUMENT\"" expect="HTTP 400" -->
```sh
curl -s -w '\nHTTP %{http_code}\n' -X POST \
  "$S/v1/projects/-/serviceAccounts/reader@sockerless.iam.gserviceaccount.com:generateAccessToken" \
  -H "$A" -H "$J" -d '{"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"7200s"}'
```

gcloud impersonates through the same API once it is pointed at the simulator:

<!-- doctest: run expect="(?m)^eyJ[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$" -->
```sh
gcloud config set api_endpoint_overrides/iamcredentials "$S/"
gcloud auth print-access-token --impersonate-service-account=reader@sockerless.iam.gserviceaccount.com
```

It also prints ID tokens for the account it is signed in as. The token's
claims name the audience and the account:

<!-- doctest: run expect="\"aud\": \"https://hello-abc-uc\\.a\\.run\\.app\"" expect="\"email\": \"sockerless-bootstrap@sockerless\\.iam\\.gserviceaccount\\.com\"" -->
```sh
gcloud auth print-identity-token --audiences=https://hello-abc-uc.a.run.app \
  | cut -d. -f2 | tr '_-' '/+' | jq -R '@base64d | fromjson | {aud, email}'
```

Then stop `simulator-gcp` with Ctrl-C.

<!-- doctest: stop id=gcp -->

<a id="google-cloud-limits"></a>

### Limits

| Gap | Tracked |
|---|---|
| 741 of 2,891 Discovery methods have no sourced permission and admit only an Owner. | [3769](../BUGS.md) |
| Basic roles follow the roles overview, not Google's console lists; a method's secondary permissions (`iam.serviceAccounts.actAs` on deploy, Cloud Storage's footnoted ones) are not checked; BigQuery dataset access entries grant nothing; list and search results are not filtered to what the caller may see; `iam.automaticIamGrantsForDefaultServiceAccounts` is not honoured; a refused call writes no audit entry. | [3770](../BUGS.md) |
| The Artifact Registry Docker data plane does not evaluate repository policies. | [3742](../BUGS.md) |
| Impersonation ignores `delegates` and serves disabled accounts. | [3742](../BUGS.md) |
| `principalSet://` members other than a whole pool (`…/*`) never match; no attribute mapping produces a principal set. | [3742](../BUGS.md) |
| Workload identity pools take no part in token exchange; the Security Token Service accepts only workforce providers. | [3742](../BUGS.md) |
| `requestedPolicyVersion` is ignored. | [3742](../BUGS.md) |
| A V4 signed URL signed by `signBlob` (system-managed key) is not verified: `serviceAccountSigned` reads only user-managed keys. | [3742](../BUGS.md) |
| No deny policies, principal access boundaries, groups, users or `tokeninfo`. | [3742](../BUGS.md) |
| The metadata server ignores an instance's configured scopes. | [3751](../BUGS.md) |
| Conditions on basic roles or on `allUsers` are accepted. | [3577](../BUGS.md) |
| Cloud Run conditions see no `request.host` or `request.path`. | [3578](../BUGS.md) |
| User-account ID tokens and path-bearing audiences at the Cloud Run front end. | [3251](../BUGS.md) |
| Workload identity pool JWKS and OpenID configuration. | [3411](../BUGS.md) |
| A workforce pool reads back without `sessionDuration`. | [3670](../BUGS.md) |
| The `-` project wildcard on a missing account answers 404, not 403. | [3357](../BUGS.md) |
| Cloud Audit Logs coverage gaps; no Policy Denied entries. | [3119](../BUGS.md) |
| The metadata server reports Cloud Run and Cloud Build instances by fixed values. | [3537](../BUGS.md) |

## Azure

The Azure simulator (`simulator-azure/`) serves its own Microsoft Entra ID: a directory, an OAuth 2.0 and OpenID Connect token service, and a slice of Microsoft Graph. Every token the simulator mints is an RS256 JWT signed with one key. Azure Resource Manager and each data plane verify that signature, the expiry and the audience. Azure Resource Manager then checks the token's tenant and authorizes every operation with Azure role-based access control (Azure RBAC); Storage, Key Vault and Event Grid authorize a bearer with Azure RBAC data actions. The other data planes authenticate the caller and stop there, or authorize it with their own keys.

### Microsoft Entra ID

#### Tenant and subscription

The simulator presents one directory. Its tenant ID is `11111111-1111-1111-1111-111111111111` (`simTenantID`, `managedidentity.go`). The seeded subscription is `00000000-0000-0000-0000-000000000001` ("Simulator Subscription").

The token endpoint does not check the tenant segment of its path. A token carries whatever tenant the path names in `tid` and `iss`; an AD FS authority (`/adfs/...`) maps to `simTenantID` (`handleAzureToken`, `auth.go`). Azure Resource Manager refuses a token whose `tid` is not the tenant of the subscription the path names, with 401 `InvalidAuthenticationTokenTenant` and a challenge naming the subscription's authority (`armTenantRefusal`, `arm_rbac.go`). A subscription belongs to `simTenantID` until `acceptOwnership` moves it. Key Vault checks the token's `iss` against the vault's `tenantId`.

#### Bootstrap application registration

`entraSeedBootstrap` (`entra.go`) seeds every directory, idempotently, with:

| Object | Value |
|---|---|
| Application (client) ID | `test-client-id` |
| Client secret | `test-client-secret` (stored as a SHA-256 hash, valid for ten years from first start) |
| Application object ID | `00000000-0000-0000-0000-0000000000b1` |
| Service principal object ID (the token's `oid`) | `00000000-0000-0000-0000-0000000000b2` |
| Display name | `Sockerless Bootstrap`, `signInAudience` `AzureADMyOrg` |

The bootstrap principal is **Owner on the tenant root management group** (`/providers/Microsoft.Management/managementGroups/11111111-1111-1111-1111-111111111111`), through role assignment `00000000-0000-0000-0000-0000000000b4` that `rbacSeedBootstrapOwner` (`rbac.go`) seeds when the assignment is absent. The role assignments API lists it like any other. The root group holds every subscription of the tenant, so the bootstrap principal manages every resource. Owner grants no data actions: to read a Key Vault that uses Azure RBAC, or a blob with a bearer, assign it a data role, as the second example does.

The seed also registers the first-party service principal "Microsoft Azure App Service" (app ID `abfa0a7c-a6b6-4736-8310-5855508787cd`, object ID `00000000-0000-0000-0000-0000000000a5`). App Service reads Key Vault certificates as that principal.

The SDK, CLI and Terraform suites all sign in as the bootstrap registration. The Terraform suites set `ARM_CLIENT_ID`, `ARM_CLIENT_SECRET`, `ARM_TENANT_ID` and `ARM_SUBSCRIPTION_ID` to the values above (`terraform-tests/helpers_test.go`).

#### Directory objects

| Object | Graph routes (v1.0 and beta) | Notes |
|---|---|---|
| Applications | `/applications`, `addPassword`, `removePassword`, `owners` | `passwordCredentials` validate the `client_credentials` grant. A write's `keyCredentials` is dropped (`entraApplicationServiceOwned`). |
| Service principals | `/servicePrincipals`, `addPassword`, `removePassword`, `owners` | A secret added to a service principal also authenticates its application (`entraClientSecretMatches`). The token endpoint requires a service principal for the application. |
| Users | `/users`, `memberOf`, `manager` | `passwordProfile.password` is stored as a verifier, never returned. Tokens resolve a user by `userPrincipalName`. |
| Groups | `/groups`, `members`, `owners`, `memberOf` | Members are any object ID. Membership nests, and `entraTransitiveGroupIDs` resolves it transitively. |
| Signed-in caller | `/me`, `/me/memberOf`, `/me/transitiveMemberOf` | |
| Any object | `/directoryObjects/{id}` | `entra_directory.go` |

Graph returns `secretText` only in the `addPassword` response; later reads carry the hint and metadata. A managed identity materializes as a service principal of type `ManagedIdentity` whose object ID is the identity's `principalId` (`entraRegisterServicePrincipal`).

`specs/SIM_SURFACE_TABLES/azure-entra.md` lists every Graph route the simulator registers. Its extractor skips routes composed from a variable, which is how applications and service principals register (`registerEntraApplicationRoutes`, `registerEntraServicePrincipalRoutes`), so read the source for those.

**Graph enforces no authentication.** Its routes are not Azure Resource Manager paths, so `AzureBearerVerificationMiddleware` skips them. The `/me` routes read `oid` from the bearer without verifying the signature (`parseOIDFromBearer`).

### Tokens

#### Endpoints

`AzureAuthMiddleware` (`auth.go`) serves the token service ahead of the mux:

| Endpoint | Purpose |
|---|---|
| `POST /{tenant}/oauth2/v2.0/token` | v2.0 token endpoint. Takes `scope`. |
| `POST /{tenant}/oauth2/token` | v1.0 token endpoint. Takes `resource`. The bare `/oauth2/token` and `/oauth2/exchange` belong to Azure Container Registry. |
| `GET /{tenant}/oauth2/v2.0/authorize` | Authorization endpoint. Redirects with a code straight away; it has no sign-in page. |
| `GET /{tenant}[/v2.0]/.well-known/openid-configuration` | OpenID Connect discovery. |
| `GET /{tenant}/discovery[/v2.0]/keys` | JSON Web Key Set (`simjwt.JWKS`). |
| `GET /{tenant}[/v2.0]/userinfo` | UserInfo. Requires a valid bearer. |
| `GET /common/discovery/instance` | Instance discovery (`auth_instance_discovery.go`). |

Both token endpoints share `handleAzureToken`. The audience comes from the first non-OpenID `scope` with `/.default` removed, or else from `resource`, or else defaults to `https://management.azure.com/` (`azureTokenAudienceFromForm`, `azureAudienceFromScope`). The scopes `https://management.azure.com/.default` and `https://storage.azure.com/.default` map to the trailing-slash audiences. Every other scope keeps the bare form: `https://vault.azure.net/.default` yields `aud` `https://vault.azure.net`.

#### Grants

| `grant_type` | Client authentication | Behaviour |
|---|---|---|
| `client_credentials` with a secret | `client_secret_post` or `client_secret_basic`. Presenting both with different client IDs is refused (`azureTokenClientID`). | `handleAzureRegisteredAppClientCredentials`: the secret must match an unexpired password credential on the application or one of its service principals, and a service principal must exist. Otherwise it answers `AADSTS7000215`, `AADSTS7000218` or `AADSTS700016`. Issues an app-only token for one hour. |
| `client_credentials` with `client_assertion` | `client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer` | `handleAzureFederatedClientCredentials` (`auth_federation.go`): workload identity federation, described under "Federated identity credentials and workload identity". |
| `authorization_code` | `client_id` and `redirect_uri` must match the code. No secret is checked. | The code lives 60 seconds and is single-use, with PKCE `plain` or `S256`. `login_hint` on the authorize request binds the code to a directory user. Without it, the token speaks for the built-in user `test-oid` (`sockerless-test@example.com`). `openid` adds an `id_token`; `offline_access` adds a refresh token. |
| `refresh_token` | `client_id` must match | Refresh tokens persist in `azure_auth_refresh_tokens`. |
| `password` (ROPC) | none | `handleAzureROPC` checks the password against the PBKDF2-SHA256 verifier Graph stored from `passwordProfile.password` (`entra_password.go`). A wrong password, or a user with none, gets `invalid_grant` with `AADSTS50126`; an unknown user `AADSTS50034`. MSAL's home-realm discovery, `GET /common/UserRealm/{user}`, is served. |

There is no certificate credential. The token endpoint sends every `client_assertion` down the federated path, which accepts only the client ID of a user-assigned managed identity. A certificate-signed assertion from an application registration therefore fails with `invalid_client`.

#### Claims

| Claim | App-only token (secret or federated) | User token (code, refresh, ROPC) | Managed identity token |
|---|---|---|---|
| `aud` | from `scope` or `resource` | from `scope` | the `resource` query parameter |
| `iss` | `https://sts.windows.net/{tid}/` | same | `https://sts.windows.net/11111111-…/` |
| `tid` | the path's tenant | the path's tenant | `simTenantID` |
| `oid`, `sub` | service principal object ID (federated: the identity's `principalId`) | user's `oid` and `sub` | identity's `principalId` |
| `appid` | client ID | `sockerless-sim` | identity's client ID |
| `idtyp` | `app` | — | `app` |
| other | `ver` `1.0`; federated tokens add `xms_fed_sub` | `ver` `1.0` | `xms_mirid` (the identity's resource ID), `ver` `1.0` |

Tokens carry no `roles`, `scp`, `groups` or `wids` claim. Only the `id_token` carries `groups` (the user's direct group IDs), `name`, `preferred_username`, `nonce` and `email`, with `ver` `2.0` and the v2.0 issuer.

The v2.0 discovery document names the issuer `<origin>/{tenant}/v2.0`, which matches the URL it is fetched from. The v1.0 document names `https://sts.windows.net/{tenant}/`. The signing key persists in `azure_auth_signing_keys` (`registerAzureAuthState`), so tokens issued before a restart still verify against the JWKS.

#### How each surface verifies a bearer

Every verifier calls `verifyAzureSimJWT`, which checks the RS256 signature against the simulator's key and the expiry. The surfaces then differ:

| Surface | Accepted `aud` | Further checks | Code |
|---|---|---|---|
| Azure Resource Manager (`/subscriptions`, `/subscriptions/…`, `/tenants`, `/providers/…`) | `https://management.azure.com[/]`, `https://management.core.windows.net[/]`, the simulator's own origin | `tid` is the subscription's tenant; Azure RBAC actions. A bad token gets 401 `invalid_token` with a `WWW-Authenticate` challenge | `AzureBearerVerificationMiddleware`, `dataplane_auth.go`; `armAuthorizeAndServe`, `arm_rbac.go` |
| Azure Storage (Blob, Files, Queue, Table) | `https://storage.azure.com[/]`, or the account's own endpoint for the service (`https://<account>.blob.<suffix>[/]`), which azurerm requests outside Azure's public cloud | Azure RBAC actions and data actions | `AuthorizeStorageDataPlane`, `blob_authorization.go`; `storageOperationOf`, `storage_rbac.go` |
| Azure Key Vault | `https://vault.azure.net[/]`, `cfa8b339-82a2-471a-a3c9-0fc0be7a4093`, `https://<keyVaultDns>` | `iss` names the vault's tenant; network rules; access policies or Azure RBAC | `keyVaultAuthorizeDataPlane`, `keyvault_dataplane_auth.go` |
| Azure Event Grid publish | `https://eventgrid.azure.net[/]` | `Microsoft.EventGrid/events/send/action` on the topic or domain | `eventGridEntraTokenAuthorizes` |
| Azure Container Registry `/oauth2/exchange` | the registry audience (`acrEntraAudienceValid`) | none; the exchanged token gets every scope it asks for | `acrExchangeIdentity`, `acr.go` |
| App Service Kudu (SCM site) | an Azure Resource Manager or App Service audience | none | `kuduAuthorize`, `web_kudu.go` |
| UserInfo | any | `sub` present | `handleAzureUserInfo` |

Azure Resource Manager's check runs before every slice, including the slices that claim ARM paths through their own wrappers (role assignments at any scope, tags, resource existence checks). It skips requests addressed to a data plane's host (`azureARMRequest`). Two exemptions apply under `/subscriptions/`. Container instance exec and attach sessions (`/execSessions/`, `/attachSessions/`) authenticate with their own session password (`azureARMPathUsesSessionAuth`). Container Apps log streams take the app's `getAuthToken` token (`isContainerAppsStreamPath`).

#### Cloud metadata discovery

`GET /metadata/endpoints` (`registerMetadata`, `metadata.go`) points every endpoint back at the simulator. It answers a single object for `api-version=2022-09-01` (azurerm and go-azure-sdk) and an array otherwise (azurestack). It names `loginEndpoint`, `resourceManager`, `graph` and `microsoftGraphResourceId`, plus the `keyVaultDns` and `storage` suffixes and three accepted management audiences. The azurerm and azuread providers need only `metadata_host`:

```hcl
provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"
  metadata_host   = "localhost:4568"
  features {}
}
```

The azuread provider fetches `https://<metadata_host>/metadata/endpoints` and hardcodes the scheme. The Terraform suites therefore run it behind their HTTPS gateway (`terraform-tests/entra_apply_test.go`).

### Authorization with Azure RBAC

#### Role definitions

The built-in catalog is the slice `builtinRoleDefs` in `authorization.go`, with real role GUIDs and permission sets. It is hand-written, not generated: `specs/cloud-api/azure/` vendors only the authorization REST specification (`authorization-arm-authorization-roledefinitionscalls-2022-04-01`, `…-roleassignmentscalls-2022-04-01`), not a built-in role list. The 31 roles are:

- Owner, Contributor, Reader, User Access Administrator;
- AcrPull, AcrPush;
- Storage Account Contributor, Storage Blob Data Owner, Storage Blob Data Contributor, Storage Blob Data Reader, Storage Blob Delegator, Storage Queue Data Contributor, Storage Queue Data Reader, Storage Queue Data Message Sender, Storage Queue Data Message Processor, Storage Table Data Contributor, Storage Table Data Reader, Storage File Data Privileged Contributor, Storage File Data Privileged Reader, Storage File Data SMB Share Contributor;
- Key Vault Administrator, Key Vault Reader, Key Vault Secrets Officer, Key Vault Secrets User, Key Vault Crypto Officer, Key Vault Crypto User, Key Vault Certificates Officer, Key Vault Certificate User;
- EventGrid Data Sender;
- Monitoring Reader, Log Analytics Contributor.

`GET {scope}/providers/Microsoft.Authorization/roleDefinitions[/{id}]` works at any scope, through middleware in `registerAuthorization`, and honours `$filter=roleName eq '…'`. Custom roles (`handleRoleDefinitionPut`, `handleRoleDefinitionDelete`) are created and deleted at subscription scope only. They are stored as written, with `type` defaulting to `CustomRole`, and evaluated like built-in roles. `assignableScopes` is stored but not enforced.

#### Role assignments

`PUT`, `GET` and `DELETE {scope}/providers/Microsoft.Authorization/roleAssignments/{guid}` work at any scope: management group, subscription, resource group, resource or child resource. Subscription and resource-group scopes have concrete routes; the middleware serves the rest. Writing one needs `Microsoft.Authorization/roleAssignments/write`, which Owner and User Access Administrator hold and Contributor's `notActions` remove.

`doRoleAssignmentPut` makes three checks:

- the name is a GUID;
- the role definition exists, built-in or custom (otherwise 400 `RoleDefinitionDoesNotExist`);
- `principalId` is a GUID. The principal does not have to exist.

The handler stores `roleDefinitionId`, `principalId`, `principalType` and `scope`. It drops `condition`, `conditionVersion` and `description`.

The list operation honours `$filter=principalId eq '…'` and `atScope()`. Without `atScope()` it returns every assignment in the store, at whatever scope (`doRoleAssignmentList`). `Permissions_ListForResourceGroup` and `Permissions_ListForResource` (`handlePermissionsList`) return the permission sets of the caller's and its groups' assignments that cover the scope.

Creating a subscription through `Microsoft.Subscription/aliases` assigns its creator Owner on it. Accepting a subscription's ownership moves it to the caller's tenant, deletes every role assignment at or beneath it and assigns the accepting principal Owner, as a directory transfer does.

**Inheritance** is a case-insensitive, segment-bounded prefix match on the resource ID (`rbacScopeCovers`). An assignment covers its own scope and everything beneath it, and the root scope `/` covers everything. A tenant's root management group, whose ID is the tenant ID, also holds every management group and every subscription of that tenant. No other management group holds anything but the paths under its own ID.

#### Evaluation

`rbacAllows` (`rbac.go`) grants an action, or a data action, when an assignment meets all of these:

- its principal is the caller's `oid`, or a group the caller belongs to transitively (`entraTransitiveGroupIDs`);
- its scope covers the request's scope;
- one permission entry of its role lists the operation in `actions` (`dataActions`) and not in that entry's `notActions` (`notDataActions`).

Matching is case-insensitive, and `*` stands for any run of characters (`rbacActionMatchesAny`, built on `sim.Wildcard{FoldCase: true}`). The simulator stores no deny assignments and serves no conditions (Azure attribute-based access control).

**Azure Resource Manager** derives each request's action from its path and verb, as ARM names actions (`armOperationOf`, `arm_rbac.go`): the namespace after the last `/providers/` segment (`Microsoft.Resources` for a path without one), the resource types along the path, and `read` for `GET` and `HEAD`, `write` for `PUT` and `PATCH`, `delete` for `DELETE`. A `POST` to a trailing segment is that segment's action: `POST …/storageAccounts/{a}/listKeys` checks `Microsoft.Storage/storageAccounts/listKeys/action` over the account. A collection read checks the collection's type over its parent: `GET …/resourceGroups/{rg}/providers/Microsoft.Storage/storageAccounts` checks `Microsoft.Storage/storageAccounts/read` over the resource group. `TestARMOperationOfDerivesEveryVendoredOperation` derives the action of every operation in the vendored Swagger documents.

A refusal is ARM's 403:

```text
{"error":{"code":"AuthorizationFailed","message":"The client '<oid>' with object id '<oid>' does not have authorization to perform action 'Microsoft.ManagedIdentity/userAssignedIdentities/write' over scope '/subscriptions/…/userAssignedIdentities/x' or the scope is invalid. If access was recently granted, please refresh your credentials."}}
```

The client is the token's `upn` or `unique_name` when it carries one, and its object ID otherwise. Some reads are authorized differently (`armAuthorizationOf`):

| Request | Authorization |
|---|---|
| `GET /subscriptions` | Any caller; the answer lists the subscriptions the caller holds an assignment in, above or beneath. |
| `GET /subscriptions/{s}`, `…/locations`, `…/providers`, `…/providers/{namespace}` | A caller holding any assignment in, above or beneath the subscription. |
| A collection read the caller may not perform over its parent | Answered with the members the caller may read, when it holds an assignment beneath the parent. Reader on one resource group lists that group alone. |
| `GET /tenants`, tenant-level provider operations (`/providers/{namespace}/…` other than `Microsoft.Authorization` and management groups) | Any authenticated caller. |
| Polls of a location's operation status (`…/locations/{l}/…operation…/{id}`, `/subscriptions/{s}/operationresults/{id}`) | Any authenticated caller of the subscription's tenant. |

**Storage** authorizes a bearer with the permission "Authorize with Microsoft Entra ID" lists for the operation (`storageOperationOf`, `storage_rbac.go`), checked at the container, queue, table or share beneath the account's `…/{blob,queue,table,file}Services/default`. Service, container, queue, table and share operations need actions (`…/blobServices/containers/write` creates a container); blob, message, entity and file operations need data actions (`…/blobServices/containers/blobs/read` reads a blob). A refusal is 403 `AuthorizationPermissionMismatch`. Get User Delegation Key needs `…/blobServices/generateUserDelegationKey/action`. A blob batch authorizes each sub-request as the batch's caller.

**Key Vault** in RBAC mode and **Event Grid** publish check their data actions through the same `rbacAllows`.

#### Which surfaces enforce what

| Surface | Credentials accepted | Authorization | Code |
|---|---|---|---|
| Azure Resource Manager | Entra bearer from the subscription's tenant | Azure RBAC actions at the request's scope, as above. Refusal: 403 `AuthorizationFailed`; a token from another tenant: 401 `InvalidAuthenticationTokenTenant`. | `AzureBearerVerificationMiddleware`, `armAuthorizeAndServe` |
| Microsoft Graph | Entra bearer with Graph's audience (`https://graph.microsoft.com`, its app ID, or the simulator origin `/metadata/endpoints` advertises); 401 `InvalidAuthenticationToken` otherwise | none: any authenticated caller performs any Graph operation | `graph_auth.go`, `entra.go` |
| Key Vault, `enableRbacAuthorization: true` | Entra bearer | Azure RBAC data actions at the object (`…/vaults/{v}/secrets/{name}`) or above, for the caller and its groups. Refusal: 403 `ForbiddenByRbac`. | `keyVaultGrants`, `kvDataPlaneOperation` |
| Key Vault, access policies | Entra bearer | An access policy whose `objectId` is the caller or a group of the caller's, whose `tenantId` matches, and whose `applicationId` (for a compound identity) matches `appid`, granting the permission or `all`. Refusal: 403 `AccessDenied`. | `keyVaultGrants` |
| Key Vault network rules | — | `publicNetworkAccess: Disabled` refuses with `ForbiddenByConnection`. `defaultAction: Deny` admits only IP rules and virtual network rules, and refuses with `ForbiddenByFirewall`. Network rules run before the permission model. | `kvNetworkRefusal` |
| Blob, Files, Queue, Table | Shared Key and Shared Key Lite (HMAC-SHA256 over the documented string-to-sign, either account key); service SAS and account SAS (signature, window, permissions, resource); user delegation SAS; Entra bearer with the Storage audience; anonymous where the container's public access allows | Shared Key and key-signed SAS by signature and signed permissions, refused with 403 `KeyBasedAuthenticationNotPermitted` when the account's `allowSharedKeyAccess` is `false`. A user delegation SAS by the key it names: one the account issued and has not revoked (`revokeUserDelegationKeys`), within the key's window, whose HMAC the signature is, and whose owner holds the operation's permission when the signature is used. A bearer by Azure RBAC. The Table service answers in OData JSON, the others in XML. | `AuthorizeStorageDataPlane`, `storageUserDelegationSASAuthorizes` |
| Get User Delegation Key (Blob and Files) | Entra bearer with the Storage audience only | `…/generateUserDelegationKey/action`; the key is bound to the caller's `oid` and `tid` | `storageIssueUserDelegationKey`, `blob_service_ops.go` |
| Service Bus and Event Hubs | `SharedAccessSignature sr=…&sig=…&se=…&skn=…` over HTTP or AMQP CBS | The signature must come from a key of the named rule at the namespace or the entity. The signed audience must cover the entity. | `verifyMessagingSAS`, `authorizeSBHTTPRequest`, `messaging_sas.go` |
| Event Grid publish | `aeg-sas-key`, `aeg-sas-token`, Entra bearer | key, signature and resource-prefix match; a bearer needs `Microsoft.EventGrid/events/send/action` on the topic or domain | `eventgrid_dataplane_auth.go` |
| Azure Cosmos DB | master-key tokens (the read-only keys authorize reads only) | `type=resource` and `type=aad` are refused with 401 | `cosmosAuthorizeDataPlane` |
| Azure Container Registry | admin credentials, an Entra token exchanged at `/oauth2/exchange`, anonymous pull | An Entra principal is granted every requested scope; AcrPull and AcrPush are not evaluated. | `acr.go`, `acr_dataplane_auth.go` |

Managed identities act as their own principal: a token from the instance metadata service or the App Service identity endpoint carries the identity's `principalId` as `oid`, so a workload reaches what the identity's role assignments grant. A system-assigned identity holds none until one is assigned.

The simulator's own in-process calls on a service's behalf are made as that first-party service, not as a tenant principal: Event Grid writes a dead letter and delivers to a queue as `Microsoft.EventGrid` (`sim.Caller`), which a storage account admits as a trusted Microsoft service (`azureTrustedServiceCall`, `azure_call.go`). Other in-process calls carry a credential of their own, as the KEDA queue scaler presents its rule's account key or managed identity token.

### Managed identities for Azure resources

**User-assigned identities**: `Microsoft.ManagedIdentity/userAssignedIdentities` (`registerManagedIdentity`, `managedidentity.go`). Create assigns a random `principalId` and `clientId`, keeps both on update, and registers a `ManagedIdentity` service principal under the `principalId`.

**System-assigned identities**: virtual machines, App Service and Azure Functions apps and slots, container apps, and Container Apps jobs carry one through their `identity` block (`applyManagedIdentity`, `web_identity.go`; `acaApplyIdentity`, `containerapps_identity.go`). The principal lasts as long as the identity. Turning the identity off and on again issues a new one. `syncManagedIdentityPrincipal` keeps the directory's service principal in step. `GET {scope}/providers/Microsoft.ManagedIdentity/identities/default` returns it (`handleSystemAssignedIdentityGetByScope`).

#### How a workload gets a token

| Workload | Endpoint | Caller identification | Code |
|---|---|---|---|
| Virtual machine | `GET /metadata/identity/oauth2/token?resource=…` with `Metadata: true`; selects by `client_id`, `object_id` or `msi_res_id` | The request's source address, matched to the VM's NIC address (`azureMetadataVMsByIP`) | `handleIMDSIdentityToken` |
| App Service, Azure Functions, Azure Container Apps (apps and jobs) | `GET $IDENTITY_ENDPOINT?api-version=2019-08-01&resource=…` with `X-IDENTITY-HEADER: $IDENTITY_HEADER`; selects by `client_id`, `principal_id`/`object_id` or `mi_res_id` | The per-workload `IDENTITY_HEADER` secret, issued on first start and persisted (`workloadIdentityHeader`) | `handleAppServiceIdentityToken`, `workload_identity.go` |

Both endpoints mint 24-hour tokens. With no selector the token belongs to the system-assigned identity. A request for an identity the workload does not carry fails: Azure Instance Metadata Service (IMDS) answers `invalid_request` "Identity not found", and the App Service endpoint answers "Unable to load the proper Managed Identity.". The instance metadata service answers virtual machines only.

#### The network sidecar

Every workload network carries a sidecar container (`sim/sidecar`, `azureEnsureSidecar` in `azure_workload_networks.go`). On a virtual network it sits at the address Azure reserves for its resolver (`azureVNetResolverAddress`); on any other network it sits at the second host of the range. It answers DNS and forwards metadata requests to the simulator. Each forwarded request carries `X-Sockerless-Sidecar-Token` and `X-Sockerless-Sidecar-Client`. `sim.SidecarTrust` (`sim/sidecar_trust.go`) accepts the client address only with a registered token, makes it the request's `RemoteAddr` and strips both headers. A workload that reaches the simulator directly therefore cannot claim another workload's address.

- A virtual machine gets `AZURE_INSTANCE_METADATA_ENDPOINT=http://<sidecar>/metadata/instance` (`azureVMInstanceSpec`, `compute_vm_backend.go`).
- An App Service app or container app with an identity gets `IDENTITY_ENDPOINT=http://<sidecar>/msi/token` and its `IDENTITY_HEADER` (`workloadMetadataEnv`, `metadata.go`). A workload without an identity gets neither.

#### Federated identity credentials and workload identity

`…/userAssignedIdentities/{name}/federatedIdentityCredentials/{fic}` stores `issuer`, `subject` and `audiences`. A token request presents the identity's `client_id` and an external OIDC token as `client_assertion`, of type `jwt-bearer`. `azureVerifyFederatedAssertion` then:

1. discovers the assertion's issuer and verifies its signature against the issuer's JWKS (`sim/oidcfed`);
2. finds a federated identity credential whose issuer, subject and audience all match the verified token.

The issued token speaks for the identity (`oid` is its `principalId`) and records the external subject in `xms_fed_sub`. The simulator federates only into user-assigned identities. It serves no `federatedIdentityCredentials` on Graph applications.

### Console sign-in

The Azure console signs in through the console's own Shauth layer, then federates the operator into Azure on the server. `azureConsoleOptions` (`federation_broker.go`) registers `GET /auth/federation/token?scope=arm|logs|graph` in `ConsoleOptions.AuthRoutes`. `handleFederationToken` sends a `client_credentials` grant with the operator's Shauth assertion as `client_assertion` to `/{tenant}/oauth2/v2.0/token`. It sends it in-process when `SOCKERLESS_CONSOLE_FEDERATION_ENDPOINT` is empty, and over HTTP otherwise. It returns only `access_token`, `token_type` and `expires_in` to the browser. The exchange runs on the server because Microsoft Entra serves no CORS response for this grant.

The browser then reads Azure Resource Manager, Log Analytics and Microsoft Graph directly. `SOCKERLESS_CONSOLE_FEDERATION_TENANT` and `SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID` (a user-assigned identity carrying a federated identity credential for the Shauth issuer) must be set together; `loadConsoleFederation` fails startup otherwise. Without them, `federationTokenEndpoint` is empty and the console stays unauthenticated.

<!-- doctest: part name=azure -->
### Examples

Install the simulator from the clone:

<!-- doctest: run -->
```sh
go -C "$SOCKERLESS_SRC" install ./simulator-azure
```

Start it in a terminal of its own:

<!-- doctest: background id=azure ready="Listening on" -->
```sh
simulator-azure
```

It listens on port 4568. The examples call the simulator with curl; output is
trimmed.

#### Get a token with the bootstrap secret, then call Azure Resource Manager

`ARM` keeps the Azure Resource Manager token. Its claims name the bootstrap
service principal:

<!-- doctest: run expect="\\{\"aud\":\"https://management\\.azure\\.com/\",\"iss\":\"https://sts\\.windows\\.net/11111111-1111-1111-1111-111111111111/\",\"tid\":\"11111111-1111-1111-1111-111111111111\",\"oid\":\"00000000-0000-0000-0000-0000000000b2\",\"appid\":\"test-client-id\",\"idtyp\":\"app\"\\}" expect="\\{\"subscriptionId\":\"00000000-0000-0000-0000-000000000001\",\"tenantId\":\"11111111-1111-1111-1111-111111111111\"\\}" -->
```sh
export S=http://localhost:4568 T=11111111-1111-1111-1111-111111111111
ARM=$(curl -s -X POST "$S/$T/oauth2/v2.0/token" \
  -d grant_type=client_credentials -d client_id=test-client-id \
  -d client_secret=test-client-secret -d scope=https://management.azure.com/.default | jq -r .access_token)
export ARM
cut -d. -f2 <<<"$ARM" | tr '_-' '/+' | jq -cR '@base64d | fromjson | {aud,iss,tid,oid,appid,idtyp}'
curl -s "$S/subscriptions?api-version=2022-12-01" -H "Authorization: Bearer $ARM" | jq -c '.value[]|{subscriptionId,tenantId}'
```

```text
{"aud":"https://management.azure.com/","iss":"https://sts.windows.net/11111111-1111-1111-1111-111111111111/","tid":"11111111-1111-1111-1111-111111111111","oid":"00000000-0000-0000-0000-0000000000b2","appid":"test-client-id","idtyp":"app"}
{"subscriptionId":"00000000-0000-0000-0000-000000000001","tenantId":"11111111-1111-1111-1111-111111111111"}
```

A wrong secret gets no token:

<!-- doctest: run expect="\\{\"error\":\"invalid_client\",\"error_description\":\"AADSTS7000215: Invalid client secret provided\\." -->
```sh
curl -s -X POST "$S/$T/oauth2/v2.0/token" \
  -d grant_type=client_credentials -d client_id=test-client-id \
  -d client_secret=wrong -d scope=https://management.azure.com/.default
```

```text
{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided. …"}
```

A call with no bearer answers 401 with a challenge:

<!-- doctest: run expect="HTTP/1\\.1 401 Unauthorized" expect="(?i)www-authenticate: Bearer authorization_uri=\"http://localhost:4568/common/oauth2/v2\\.0/authorize\", error=\"invalid_token\"" -->
```sh
curl -s -o /dev/null -D - "$S/subscriptions?api-version=2022-12-01"
```

```text
HTTP/1.1 401 Unauthorized
Www-Authenticate: Bearer authorization_uri="http://localhost:4568/common/oauth2/v2.0/authorize", error="invalid_token", …
```

#### Create a role assignment; see a denied and an allowed Key Vault call

Create a resource group and a Key Vault that uses Azure RBAC, as the bootstrap
principal, which is Owner:

<!-- doctest: run expect="\"https://iamdemokv\\.vault\\.localhost:4568/\"" -->
```sh
export SUB=00000000-0000-0000-0000-000000000001
export V=/subscriptions/$SUB/resourceGroups/iam-demo/providers/Microsoft.KeyVault/vaults/iamdemokv
curl -s -X PUT "$S/subscriptions/$SUB/resourcegroups/iam-demo?api-version=2021-04-01" \
  -H "Authorization: Bearer $ARM" -H 'Content-Type: application/json' -d '{"location":"westeurope"}' \
  | jq -c '{id, properties}'
curl -s -X PUT "$S$V?api-version=2023-07-01" -H "Authorization: Bearer $ARM" -H 'Content-Type: application/json' \
  -d "{\"location\":\"westeurope\",\"properties\":{\"tenantId\":\"$T\",\"sku\":{\"family\":\"A\",\"name\":\"standard\"},\"enableRbacAuthorization\":true}}" \
  | jq .properties.vaultUri
```

The vault's `vaultUri` reads `https://iamdemokv.vault.localhost:4568/`.
Without TLS configured, the simulator serves that host over plain HTTP, which
is the URL the calls below use.

Owner grants no data actions, so the principal's Key Vault token (`KV`) cannot
set a secret yet:

<!-- doctest: run expect="\"innererror\":\\{\"code\":\"ForbiddenByRbac\"\\}" expect="Action: 'Microsoft\\.KeyVault/vaults/secrets/setSecret/action'" expect="Assignment: \\(not found\\)" expect="HTTP 403" -->
```sh
KV=$(curl -s -X POST "$S/$T/oauth2/v2.0/token" -d grant_type=client_credentials -d client_id=test-client-id \
  -d client_secret=test-client-secret -d scope=https://vault.azure.net/.default | jq -r .access_token)
export KV
curl -s -w '\nHTTP %{http_code}\n' -X PUT "http://iamdemokv.vault.localhost:4568/secrets/db-password?api-version=7.4" \
  -H "Authorization: Bearer $KV" -H 'Content-Type: application/json' -d '{"value":"s3cret"}'
```

```text
{"error":{"code":"Forbidden","innererror":{"code":"ForbiddenByRbac"},"message":"Caller is not authorized to perform action on resource.\r\n…Caller: appid=test-client-id;oid=00000000-0000-0000-0000-0000000000b2;…\r\nAction: 'Microsoft.KeyVault/vaults/secrets/setSecret/action'\r\nResource: '/subscriptions/…/vaults/iamdemokv/secrets/db-password'\r\nAssignment: (not found)…"}}
HTTP 403
```

Assign Key Vault Secrets Officer (`b86a8fe4-44ce-4948-aee5-eccb2c155cd7`) to
the bootstrap principal at the vault, and set the secret again:

<!-- doctest: run expect="\"scope\":\"/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/iam-demo/providers/Microsoft\\.KeyVault/vaults/iamdemokv\"" expect="HTTP 201" expect="\"value\":\"s3cret\"" expect="HTTP 200" -->
```sh
curl -s -w '\nHTTP %{http_code}\n' -X PUT \
  "$S$V/providers/Microsoft.Authorization/roleAssignments/6f1c2a52-0d6e-4c1b-9a59-3c7d1c2b9e01?api-version=2022-04-01" \
  -H "Authorization: Bearer $ARM" -H 'Content-Type: application/json' \
  -d "{\"properties\":{\"roleDefinitionId\":\"/subscriptions/$SUB/providers/Microsoft.Authorization/roleDefinitions/b86a8fe4-44ce-4948-aee5-eccb2c155cd7\",\"principalId\":\"00000000-0000-0000-0000-0000000000b2\",\"principalType\":\"ServicePrincipal\"}}"
curl -s -w '\nHTTP %{http_code}\n' -X PUT "http://iamdemokv.vault.localhost:4568/secrets/db-password?api-version=7.4" \
  -H "Authorization: Bearer $KV" -H 'Content-Type: application/json' -d '{"value":"s3cret"}'
```

```text
{"id":"…/vaults/iamdemokv/providers/Microsoft.Authorization/roleAssignments/6f1c2a52-…","properties":{"roleDefinitionId":"…/b86a8fe4-…","principalId":"00000000-0000-0000-0000-0000000000b2","scope":"…/vaults/iamdemokv"}}
HTTP 201
{"id":"https://iamdemokv.vault.localhost:4568/secrets/db-password/8797dfe1-…","value":"s3cret","attributes":{"enabled":true,…}}
HTTP 200
```

The role grants only `Microsoft.KeyVault/vaults/secrets/*`, so the same
principal still cannot create a key:

<!-- doctest: run expect="\"innererror\":\\{\"code\":\"ForbiddenByRbac\"\\}" expect="Action: 'Microsoft\\.KeyVault/vaults/keys/create/action'" expect="HTTP 403" -->
```sh
curl -s -w '\nHTTP %{http_code}\n' -X POST "http://iamdemokv.vault.localhost:4568/keys/k1/create?api-version=7.4" \
  -H "Authorization: Bearer $KV" -H 'Content-Type: application/json' -d '{"kty":"RSA"}'
```

The vault refuses a token minted for another audience, such as the Azure
Resource Manager token:

<!-- doctest: run expect="AKV10022: Invalid audience\\. Expected https://vault\\.azure\\.net, found: https://management\\.azure\\.com/\\." expect="HTTP 401" -->
```sh
curl -s -w '\nHTTP %{http_code}\n' "http://iamdemokv.vault.localhost:4568/secrets/db-password?api-version=7.4" \
  -H "Authorization: Bearer $ARM"
```

```text
{"error":{"code":"Unauthorized","message":"AKV10022: Invalid audience. Expected https://vault.azure.net, found: https://management.azure.com/."}}
HTTP 401
```

Then stop `simulator-azure` with Ctrl-C.

<!-- doctest: stop id=azure -->

### Limits

- **Azure Resource Manager authorization has gaps.** Location-scoped operation-status polls and tenant-level provider operations are open to any authenticated caller; `Microsoft.Subscription` aliases and ownership are not authorized by billing roles; the vendored specifications carry no `Microsoft.Authorization/providerOperations` catalog to check derived actions against ([3776](../BUGS.md)).
- **Storage authorization has gaps.** The Files service does not require `x-ms-file-request-intent: backup` or the backup-semantics data actions of a bearer; Put Blob of a new blob does not accept `…/blobs/add/action` alone; Copy Blob does not authorize its source; the Queue service verifies no user delegation SAS ([3777](../BUGS.md)).
- **Microsoft Graph authorizes nothing.** Graph requires a token with its audience, but tokens carry no `roles` or `scp`, so app roles and delegated permissions are not modelled, and any authenticated caller performs any Graph operation ([3745](../BUGS.md)).
- **Credentials are partial.** There are no certificate credentials (`keyCredentials` is dropped) and no federated credentials on application registrations. The `authorization_code` grant does not check a client secret ([3745](../BUGS.md)).
- **Role assignments:** no conditions (ABAC), no deny assignments, no management-group hierarchy below the tenant root group. `principalId` is not checked against the directory. Custom roles work at subscription scope only, with `assignableScopes` unenforced. The unfiltered list returns assignments from every scope ([3746](../BUGS.md)).
- **Service Bus and Event Hubs:** an authorization rule's `rights` (`Listen`, `Send`, `Manage`) are not checked against the operation. Entra-authenticated access to the data plane is not served; `BUGS.md` row 3540 covers its scale-rule side.
- **Azure Cosmos DB:** RBAC (`type=aad`) and resource tokens are refused, not modelled.
- **Azure Container Registry:** an Entra principal gets every scope; AcrPull and AcrPush are not evaluated ([3747](../BUGS.md), which also covers Service Bus rights and Cosmos DB tokens).
- **App Service identity endpoint:** row 3391 covers its refusal statuses and bodies, which no source states.
