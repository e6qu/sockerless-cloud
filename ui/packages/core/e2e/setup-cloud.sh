#!/usr/bin/env bash
# Provisions the same cloud resources that an operator supplies to a backend.
# The only test-specific input is the simulator endpoint coordinate.
set -euo pipefail

if [[ -z "${SIMULATOR_URL:-}" || -z "${SIMULATOR_SETUP:-}" ]]; then
  echo "ERROR: SIMULATOR_URL and SIMULATOR_SETUP are required" >&2
  exit 1
fi

put_json() {
  local url="$1"
  local body="$2"
  # ARM_BEARER, set by the Azure cases below, carries the bearer Azure
  # Resource Manager requires (see azure_arm_bearer).
  local auth_header=()
  if [[ -n "${ARM_BEARER:-}" ]]; then
    auth_header=(--header "Authorization: Bearer ${ARM_BEARER}")
  fi
  curl --fail --silent --show-error \
    --request PUT \
    --header "Content-Type: application/json" \
    "${auth_header[@]}" \
    --data "$body" \
    "$url" >/dev/null
}

# azure_arm_bearer acquires an Azure Resource Manager bearer token the way an
# administrator's script outside a workload does: the client_credentials grant
# for the application registration the tenant is seeded with
# (test-client-id / test-client-secret) at the Microsoft Entra token endpoint.
# The managed-identity endpoint answers only workloads that hold an identity.
# Emits the raw access token on stdout.
azure_arm_bearer() {
  curl --fail --silent --show-error \
    --data-urlencode "grant_type=client_credentials" \
    --data-urlencode "client_id=test-client-id" \
    --data-urlencode "client_secret=test-client-secret" \
    --data-urlencode "scope=https://management.azure.com/.default" \
    "$1/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token" | jq -r .access_token
}

# The Google Cloud simulator's well-known bootstrap service account and the key
# it seeds the account with.
GCP_BOOTSTRAP_EMAIL="sockerless-bootstrap@sockerless.iam.gserviceaccount.com"
GCP_BOOTSTRAP_KEY_ID="49c4c9ee1dffb89ca13534183ea5e484e46abaaa"
GCP_BOOTSTRAP_PRIVATE_KEY='-----BEGIN PRIVATE KEY-----
MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQCgIehWBhPTjtE4
Dq1bxsFfLbjm8gQXLIBXDExpfTN5iiQaJXmiR7kZXO8y+IGgh2BuJCJNpxWwrPHj
qWaymDd/qjaU8Ni15o84+0V1zKtoQ+tmnQqDi0VTEenLpV8iRYG68ambB1FZ5F+A
4n3IgAMVfUXe6GUdMaRLQUZv4blFimKxNORR0NzL3msr7toxmHhTikmiN23cIBFB
OEIHIlXXe2az54FHBPXuuGTNhsDKMRiw32+XIeXi+nRF3XuopiIVUOihhbqmjomV
+kgSx5O20QwQ8ITtVXq75Yp9KFCTtyCUY2MtPIVRTnMYTkFrUV613hEOc2MiI+hU
7BgXk5kxAgMBAAECggEADVRNOe3nFBbhCPzFxuwwN/s/FzBBkWlBpJ1QNf63PHB4
iVmhupWkTX1sJs7DQYjLSNfGqDaWil6XAvecphSgGAcj5Hl43Ilc1iYqVcAr63Gt
5FyBncoHwv6seqhhFyH4hsZrcw1kouos3fZceIZZW15SZNfs5MV3UtFT6ue765MT
4saqJqCSt9i26PnP6VILDCpmcXoPF5gMlSNLKmp7q0jlxLOsXalQULFGPOg7MEE0
maOzd9M4A5B3dUo4ucBG6BdjK763VasR7NeWKlzSiotG/NS+fqZy6C5bNPzxKktt
EDZlLxtqiahthmFzRbpUj4hqSiBmY92wG+2HnBynzQKBgQDRO3SGDlTZ6RbTzYvr
PJGQY6u6J+jT3JHoBMs6cGVWOJceu4UUSreTO4oYlDcLWMkKAF3snZha6us5h0k2
NIO0MVEnx+VWDIHpHEA1VCoAe48cufN1CXsbZRwF14zxSY1IpZ+kLVwrh4UQt7Ns
RdcsB+xC0229K439EiUYeDCvRQKBgQDD7OW8y/A74f8tI8+QSXCFo1VdDGc8MfjX
ieKaeRvDn5GsxvAGjRODenQL8s35TOztUdnn5AAouaWqX1T0NkvDyRKMD/w/1vQj
XxtXY8zS3ADzbdrNNhuBOeQek15iDxzo8vRwIFtpJEaZ47uFoapg9MhLhj/TWlZt
dIOD7Wn6/QKBgQCxz5QsJNvJQIRMxT6mUBfonDuhfS8pdSZuBjzr0f7cskUDMhtb
bk/qCMxILRVGSFK5dCxd3GRQx5nVjvoR3bkbllnAs+lbIk/42jBa4ca8GEU8BW83
xDHhyyBweolluLsfxNkRfhb5SzczSNprJkNBQQ5wkBuPiwckzZnG3MWK/QKBgQC6
OjGAS8Rz1vNqn5BKUFPg4ohgIkr6H3GsxcYbGCR4w/J+bLV3NJ8oqLsnJEn9vc2y
Icviq4gvLY63K1gfrIBFf5jegs88iv+Ro3Pkk9r7TdylhXSk+9oW8cTP/zmGMWFE
VrJa5ZadkLp+ycTTVwAQmnrLtrZyGXrvHQFo48CJrQKBgG2E+MuCRr0kRc7jEkHE
tJUXWzmzh7IgglfexVWc2/PLNlP2foy/8oiE+hn4QO9/xd3q3LIsNl5xlT61AtZK
XCB2hUM2czc3i/9DRIo249YLUSkUGJB6PHXNkq3b2NEL2uKkvz6Wzpof30OV7Pn6
eQDiTQyEvNvkuOrWUcehkn2z
-----END PRIVATE KEY-----'

# base64url emits its stdin base64url-encoded without padding, as a JWT
# carries each segment.
base64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
}

# gcp_bootstrap_token mints an access token for the simulator's bootstrap
# service account the way Google documents the service-account flow over raw
# HTTP: sign a JWT-bearer assertion with the account's key and exchange it at
# the OAuth 2.0 token endpoint. The metadata server answers only workloads, so
# an administrator's script outside one provisions with this credential. Emits
# the raw access token on stdout.
gcp_bootstrap_token() {
  local token_uri="$1/token" now header claims signing_input signature key_file
  now="$(date +%s)"
  header="$(jq -cjn --arg kid "$GCP_BOOTSTRAP_KEY_ID" '{alg: "RS256", typ: "JWT", kid: $kid}' | base64url)"
  claims="$(jq -cjn --arg iss "$GCP_BOOTSTRAP_EMAIL" --arg aud "$token_uri" --argjson now "$now" '{
    iss: $iss, sub: $iss, aud: $aud,
    scope: "https://www.googleapis.com/auth/cloud-platform",
    iat: $now, exp: ($now + 3600)
  }' | base64url)"
  signing_input="${header}.${claims}"
  key_file="$(mktemp)"
  printf '%s\n' "$GCP_BOOTSTRAP_PRIVATE_KEY" >"$key_file"
  signature="$(printf '%s' "$signing_input" | openssl dgst -sha256 -sign "$key_file" | base64url)"
  rm -f "$key_file"
  curl --fail --silent --show-error \
    --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer" \
    --data-urlencode "assertion=${signing_input}.${signature}" \
    "$token_uri" | jq -r .access_token
}

# hex_sha256 emits the lowercase hex SHA-256 of its stdin.
hex_sha256() {
  openssl dgst -sha256 -hex | sed 's/^.*= //'
}

# hmac_hex computes HMAC-SHA256(hexkey, data) and emits lowercase hex.
hmac_hex() {
  local hexkey="$1"
  printf '%s' "$2" | openssl dgst -sha256 -mac HMAC -macopt "hexkey:${hexkey}" | sed 's/^.*= //'
}

# aws_sigv4_post signs an AWS control-plane request (awsJson) with SigV4 the way
# a real AWS SDK / CLI client does and POSTs it. It differs from the real cloud
# ONLY in coordinates: the endpoint base URL (the simulator) and the seeded
# bootstrap administrator credential the simulator provisions (access key
# "test", secret "test", region us-east-1) — the same static credential the AWS
# backends configure. The simulator recomputes and verifies this signature at
# its control-plane chokepoint; an unsigned request is rejected with 403.
#
#   $1 base URL (e.g. http://127.0.0.1:19320)
#   $2 Content-Type header value
#   $3 X-Amz-Target header value, or empty for a protocol that names the
#      operation in the body (the Query protocol)
#   $4 request body
#   $5 signing service (e.g. ecs)
aws_sigv4_request() {
  local base="$1" content_type="$2" target="$3" body="$4" service="$5"
  local access_key="test" secret="test" region="us-east-1"

  local host="${base#http://}"
  host="${host#https://}"
  host="${host%%/*}"

  local amzdate datestamp
  amzdate="$(date -u +%Y%m%dT%H%M%SZ)"
  datestamp="$(date -u +%Y%m%d)"

  local payload_hash
  payload_hash="$(printf '%s' "$body" | hex_sha256)"

  local signed_headers="content-type;host;x-amz-content-sha256;x-amz-date"
  local canonical_headers="content-type:${content_type}
host:${host}
x-amz-content-sha256:${payload_hash}
x-amz-date:${amzdate}
"
  local target_header=()
  if [[ -n "$target" ]]; then
    signed_headers="${signed_headers};x-amz-target"
    canonical_headers="${canonical_headers}x-amz-target:${target}
"
    target_header=(--header "X-Amz-Target: ${target}")
  fi
  local canonical_request="POST
/

${canonical_headers}
${signed_headers}
${payload_hash}"

  local scope="${datestamp}/${region}/${service}/aws4_request"
  local string_to_sign
  string_to_sign="AWS4-HMAC-SHA256
${amzdate}
${scope}
$(printf '%s' "$canonical_request" | hex_sha256)"

  local ksecret_hex
  ksecret_hex="$(printf 'AWS4%s' "$secret" | od -An -v -tx1 | tr -d ' \n')"
  local kdate kregion kservice ksigning signature
  kdate="$(hmac_hex "$ksecret_hex" "$datestamp")"
  kregion="$(hmac_hex "$kdate" "$region")"
  kservice="$(hmac_hex "$kregion" "$service")"
  ksigning="$(hmac_hex "$kservice" "aws4_request")"
  signature="$(hmac_hex "$ksigning" "$string_to_sign")"

  curl --fail --silent --show-error \
    --request POST \
    --header "Content-Type: ${content_type}" \
    ${target_header[@]+"${target_header[@]}"} \
    --header "X-Amz-Date: ${amzdate}" \
    --header "X-Amz-Content-Sha256: ${payload_hash}" \
    --header "Authorization: AWS4-HMAC-SHA256 Credential=${access_key}/${scope}, SignedHeaders=${signed_headers}, Signature=${signature}" \
    --data "$body" \
    "${base}/" >/dev/null
}

# aws_sigv4_post calls an awsJson (1.1) operation by its X-Amz-Target.
aws_sigv4_post() {
  aws_sigv4_request "$1" "application/x-amz-json-1.1" "$2" "$3" "$4"
}

# aws_query calls a Query-protocol operation (IAM, STS): a form-encoded POST
# whose Action, Version, and parameters are the name=value arguments after the
# base URL and the signing service.
aws_query() {
  local base="$1" service="$2"
  shift 2
  local body="" pair name value
  for pair in "$@"; do
    name="${pair%%=*}"
    value="${pair#*=}"
    body="${body:+${body}&}${name}=$(jq -rn --arg v "$value" '$v|@uri')"
  done
  aws_sigv4_request "$base" "application/x-www-form-urlencoded; charset=utf-8" "" "$body" "$service"
}

case "$SIMULATOR_SETUP" in
  aws-console-federation)
    # The console federates its operator the way a deployment wires it: an IAM
    # OpenID Connect identity provider for the issuer the console signs
    # operators in with, and the role the console assumes with that assertion.
    if [[ -z "${FEDERATION_ISSUER:-}" || -z "${FEDERATION_CLIENT_ID:-}" || -z "${FEDERATION_ROLE_NAME:-}" ]]; then
      echo "ERROR: FEDERATION_ISSUER, FEDERATION_CLIENT_ID, and FEDERATION_ROLE_NAME are required" >&2
      exit 1
    fi
    provider="${FEDERATION_ISSUER#http://}"
    provider="${provider#https://}"
    aws_query "$SIMULATOR_URL" iam Action=CreateOpenIDConnectProvider Version=2010-05-08 \
      "Url=${FEDERATION_ISSUER}" "ClientIDList.member.1=${FEDERATION_CLIENT_ID}" \
      "ThumbprintList.member.1=0000000000000000000000000000000000000000"
    trust="$(jq -cn --arg provider "$provider" --arg client "$FEDERATION_CLIENT_ID" '{
      Version: "2012-10-17",
      Statement: [{
        Effect: "Allow",
        Principal: {Federated: ("arn:aws:iam::123456789012:oidc-provider/" + $provider)},
        Action: "sts:AssumeRoleWithWebIdentity",
        Condition: {StringEquals: {($provider + ":aud"): $client}}
      }]
    }')"
    aws_query "$SIMULATOR_URL" iam Action=CreateRole Version=2010-05-08 \
      "RoleName=${FEDERATION_ROLE_NAME}" "AssumeRolePolicyDocument=${trust}"
    aws_query "$SIMULATOR_URL" iam Action=PutRolePolicy Version=2010-05-08 \
      "RoleName=${FEDERATION_ROLE_NAME}" PolicyName=ConsoleAdministrator \
      'PolicyDocument={"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}'
    ;;
  gcp-console-federation)
    # The console federates its operator the way a deployment wires Workforce
    # Identity Federation: a workforce pool, an OpenID Connect provider beneath
    # it for the issuer the console signs operators in with, and an allow
    # policy granting the pool's principals the project.
    if [[ -z "${FEDERATION_ISSUER:-}" || -z "${FEDERATION_CLIENT_ID:-}" || -z "${FEDERATION_POOL:-}" ||
      -z "${FEDERATION_PROVIDER:-}" || -z "${FEDERATION_PROJECT:-}" ]]; then
      echo "ERROR: FEDERATION_ISSUER, FEDERATION_CLIENT_ID, FEDERATION_POOL, FEDERATION_PROVIDER, and FEDERATION_PROJECT are required" >&2
      exit 1
    fi
    gcp_token="$(gcp_bootstrap_token "$SIMULATOR_URL")"
    gcp_post() {
      curl --fail --silent --show-error --request POST \
        --header "Content-Type: application/json" --header "Authorization: Bearer ${gcp_token}" \
        --data "$2" "$SIMULATOR_URL$1" >/dev/null
    }
    gcp_post "/v1/locations/global/workforcePools?workforcePoolId=${FEDERATION_POOL}" \
      '{"parent":"organizations/123456789012","displayName":"Console operators"}'
    gcp_post "/v1/locations/global/workforcePools/${FEDERATION_POOL}/providers?workforcePoolProviderId=${FEDERATION_PROVIDER}" \
      "$(jq -cn --arg issuer "$FEDERATION_ISSUER" --arg client "$FEDERATION_CLIENT_ID" '{
        displayName: "Console sign-in",
        oidc: {issuerUri: $issuer, clientId: $client, webSsoConfig: {responseType: "CODE", assertionClaimsBehavior: "ONLY_ID_TOKEN_CLAIMS"}},
        attributeMapping: {"google.subject": "assertion.sub"}
      }')"
    policy="$(curl --fail --silent --show-error --request POST \
      --header "Content-Type: application/json" --header "Authorization: Bearer ${gcp_token}" \
      --data '{}' "$SIMULATOR_URL/v1/projects/${FEDERATION_PROJECT}:getIamPolicy")"
    member="principalSet://iam.googleapis.com/locations/global/workforcePools/${FEDERATION_POOL}/*"
    gcp_post "/v1/projects/${FEDERATION_PROJECT}:setIamPolicy" \
      "$(jq -c --arg member "$member" '{policy: (. | .bindings = ((.bindings // []) + [{role: "roles/owner", members: [$member]}]))}' <<<"$policy")"
    ;;
  azure-console-federation)
    # The console federates its operator the way a deployment wires Microsoft
    # Entra Workload Identity Federation: a user-assigned managed identity, a
    # federated identity credential on it trusting the issuer and subject the
    # console signs operators in with, and an Owner role assignment on the
    # subscription. The identity's client ID is assigned by Azure, so the case
    # writes it to SIMULATOR_SETUP_OUTPUT for the console's configuration.
    if [[ -z "${FEDERATION_ISSUER:-}" || -z "${FEDERATION_CLIENT_ID:-}" || -z "${FEDERATION_SUBJECT:-}" ||
      -z "${SIMULATOR_SETUP_OUTPUT:-}" ]]; then
      echo "ERROR: FEDERATION_ISSUER, FEDERATION_CLIENT_ID, FEDERATION_SUBJECT, and SIMULATOR_SETUP_OUTPUT are required" >&2
      exit 1
    fi
    subscription="00000000-0000-0000-0000-000000000001"
    group="console-identity"
    ARM_BEARER="$(azure_arm_bearer "$SIMULATOR_URL")"
    if [[ -z "$ARM_BEARER" ]]; then
      echo "ERROR: failed to acquire an ARM bearer from $SIMULATOR_URL" >&2
      exit 1
    fi
    identity="/subscriptions/$subscription/resourceGroups/$group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/console-operator"
    put_json "$SIMULATOR_URL/subscriptions/$subscription/resourceGroups/$group?api-version=2021-04-01" '{"location":"eastus"}'
    identity_json="$(curl --fail --silent --show-error --request PUT \
      --header "Content-Type: application/json" --header "Authorization: Bearer ${ARM_BEARER}" \
      --data '{"location":"eastus"}' "$SIMULATOR_URL$identity?api-version=2023-01-31")"
    put_json "$SIMULATOR_URL$identity/federatedIdentityCredentials/console-sign-in?api-version=2023-01-31" \
      "$(jq -cn --arg issuer "$FEDERATION_ISSUER" --arg subject "$FEDERATION_SUBJECT" --arg audience "$FEDERATION_CLIENT_ID" \
        '{properties: {issuer: $issuer, subject: $subject, audiences: [$audience]}}')"
    put_json "$SIMULATOR_URL/subscriptions/$subscription/providers/Microsoft.Authorization/roleAssignments/6d0d5c3c-7a3e-4c63-9f5e-0c0a50e0c0de?api-version=2022-04-01" \
      "$(jq -cn --arg principal "$(jq -r .properties.principalId <<<"$identity_json")" --arg sub "$subscription" '{properties: {
        roleDefinitionId: ("/subscriptions/" + $sub + "/providers/Microsoft.Authorization/roleDefinitions/8e3af657-a8ff-443c-a75c-2fe8c4bcb635"),
        principalId: $principal, principalType: "ServicePrincipal"}}')"
    printf 'SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID=%s\n' "$(jq -r .properties.clientId <<<"$identity_json")" >"$SIMULATOR_SETUP_OUTPUT"
    ;;
  aws-ecs)
    aws_sigv4_post \
      "$SIMULATOR_URL" \
      "AmazonEC2ContainerServiceV20141113.CreateCluster" \
      '{"clusterName":"sockerless-e2e"}' \
      "ecs"
    ;;
  gcp)
    # The simulator verifies bearer tokens on its Cloud Storage data plane.
    gcp_token="$(gcp_bootstrap_token "$SIMULATOR_URL")"
    # Cloud Storage creates a bucket only in a project that exists, so create
    # the project first and wait on the create operation's done field.
    operation="$(curl --fail --silent --show-error --request POST \
      --header "Content-Type: application/json" --header "Authorization: Bearer ${gcp_token}" \
      --data '{"projectId":"sockerless-e2e","parent":"organizations/123456789012"}' \
      "$SIMULATOR_URL/v3/projects")"
    until [[ "$(jq -r '.done // false' <<<"$operation")" == "true" ]]; do
      sleep 1
      operation="$(curl --fail --silent --show-error --header "Authorization: Bearer ${gcp_token}" \
        "$SIMULATOR_URL/v3/$(jq -r .name <<<"$operation")")"
    done
    if [[ "$(jq -r '.error // empty' <<<"$operation")" != "" ]]; then
      echo "ERROR: creating project sockerless-e2e failed: $operation" >&2
      exit 1
    fi
    curl --fail --silent --show-error \
      --request POST \
      --header "Content-Type: application/json" \
      --header "Authorization: Bearer ${gcp_token}" \
      --data '{"name":"sockerless-e2e-build"}' \
      "$SIMULATOR_URL/storage/v1/b?project=sockerless-e2e" >/dev/null
    ;;
  azure-aca)
    subscription="00000000-0000-0000-0000-000000000001"
    group="sockerless-e2e"
    ARM_BEARER="$(azure_arm_bearer "$SIMULATOR_URL")"
    if [[ -z "$ARM_BEARER" ]]; then
      echo "ERROR: failed to acquire an ARM bearer from $SIMULATOR_URL" >&2
      exit 1
    fi
    put_json \
      "$SIMULATOR_URL/subscriptions/$subscription/resourceGroups/$group?api-version=2021-04-01" \
      '{"location":"eastus"}'
    put_json \
      "$SIMULATOR_URL/subscriptions/$subscription/resourceGroups/$group/providers/Microsoft.Storage/storageAccounts/sockerlesse2e?api-version=2023-01-01" \
      '{"location":"eastus","sku":{"name":"Standard_LRS"},"kind":"StorageV2","properties":{}}'
    put_json \
      "$SIMULATOR_URL/subscriptions/$subscription/resourceGroups/$group/providers/Microsoft.App/managedEnvironments/sockerless-e2e?api-version=2024-03-01" \
      '{"location":"eastus","properties":{}}'
    ;;
  azure-azf)
    subscription="00000000-0000-0000-0000-000000000001"
    group="sockerless-e2e"
    ARM_BEARER="$(azure_arm_bearer "$SIMULATOR_URL")"
    if [[ -z "$ARM_BEARER" ]]; then
      echo "ERROR: failed to acquire an ARM bearer from $SIMULATOR_URL" >&2
      exit 1
    fi
    put_json \
      "$SIMULATOR_URL/subscriptions/$subscription/resourceGroups/$group?api-version=2021-04-01" \
      '{"location":"eastus"}'
    put_json \
      "$SIMULATOR_URL/subscriptions/$subscription/resourceGroups/$group/providers/Microsoft.Storage/storageAccounts/sockerlesse2e?api-version=2023-01-01" \
      '{"location":"eastus","sku":{"name":"Standard_LRS"},"kind":"StorageV2","properties":{}}'
    ;;
  *)
    echo "ERROR: unknown SIMULATOR_SETUP=$SIMULATOR_SETUP" >&2
    exit 1
    ;;
esac
