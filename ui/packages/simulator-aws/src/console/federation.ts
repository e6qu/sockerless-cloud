import { signedFetch, type AwsCredentials } from "./sigv4.js";

// The console reads the real AWS APIs, and it reaches them exactly as it would
// reach real AWS: it federates the signed-in operator's assertion into
// temporary credentials at the Security Token Service, then signs every request
// with them. Only the coordinates — the endpoint base URLs and the role to
// assume — change between the simulator and real AWS; there is no
// simulator-versus-cloud branch and no simulator-served credential broker.
//
// Whether requests are signed is a real deployment condition, not a fallback: a
// console wired to an identity provider federates the operator and signs every
// call, and a federation failure there is surfaced rather than hidden; a console
// with no identity provider — a local or test instance — reads unsigned, the
// mode the account control reports.

// The single Region this console operates in: every SigV4 signature scopes to
// it, and the header/Overview badges display it, so the UI can never show a
// Region other than the one requests are actually signed for.
export const REGION = "us-east-1";

interface ConsoleConfig {
  identityEndpoint?: string;
  cloudApiEndpoint?: string;
  federationEndpoint?: string;
  federationAudience?: string; // the role ARN to assume
  federationSubject?: string;
}

let configPromise: Promise<ConsoleConfig> | null = null;
let cachedCreds: { value: AwsCredentials; expiresAt: number } | null = null;
let inflightCreds: Promise<AwsCredentials> | null = null;

async function consoleConfig(): Promise<ConsoleConfig> {
  if (!configPromise) {
    configPromise = fetch("/ui/config.json", { credentials: "include" }).then((response) => {
      if (!response.ok) throw new Error(`/ui/config.json returned HTTP ${response.status}`);
      return response.json() as Promise<ConsoleConfig>;
    });
  }
  return configPromise;
}

function xmlText(xml: Document, tag: string): string {
  return xml.getElementsByTagName(tag)[0]?.textContent ?? "";
}

async function federatedCredentials(config: ConsoleConfig): Promise<AwsCredentials> {
  if (cachedCreds && cachedCreds.expiresAt - 60_000 > Date.now()) {
    return cachedCreds.value;
  }
  // One in-flight exchange: a console render fires many signed reads at
  // once, and without deduplication each would start its own
  // AssumeRoleWithWebIdentity exchange before the first response lands in
  // cachedCreds.
  if (inflightCreds) {
    return inflightCreds;
  }
  inflightCreds = assumeFederatedCredentials(config);
  try {
    return await inflightCreds;
  } finally {
    inflightCreds = null;
  }
}

async function assumeFederatedCredentials(config: ConsoleConfig): Promise<AwsCredentials> {
  const now = Date.now();
  const subjectResponse = await fetch(config.federationSubject!, { credentials: "include" });
  if (!subjectResponse.ok) {
    throw new Error(`could not read the operator assertion: HTTP ${subjectResponse.status}`);
  }
  const { subject_token: subjectToken } = (await subjectResponse.json()) as { subject_token: string };

  const body = new URLSearchParams({
    Action: "AssumeRoleWithWebIdentity",
    Version: "2011-06-15",
    RoleArn: config.federationAudience!,
    RoleSessionName: "console",
    WebIdentityToken: subjectToken,
  });
  const response = await fetch(`${config.federationEndpoint ?? ""}/`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
    credentials: "include",
  });
  if (!response.ok) {
    throw new Error(`AssumeRoleWithWebIdentity failed: HTTP ${response.status}`);
  }
  const xml = new DOMParser().parseFromString(await response.text(), "text/xml");
  const credentials: AwsCredentials = {
    accessKeyId: xmlText(xml, "AccessKeyId"),
    secretAccessKey: xmlText(xml, "SecretAccessKey"),
    sessionToken: xmlText(xml, "SessionToken"),
  };
  const expiration = Date.parse(xmlText(xml, "Expiration")) || now + 3_600_000;
  cachedCreds = { value: credentials, expiresAt: expiration };
  return credentials;
}

export interface AwsRequest {
  service: string;
  method?: string;
  path?: string;
  headers?: Record<string, string>;
  body?: string;
}

// awsFetch reaches a real AWS API at the configured cloud coordinate, signing
// the request with federated credentials when the console is wired to an
// identity provider.
export async function awsFetch(request: AwsRequest): Promise<Response> {
  const config = await consoleConfig();
  const base = config.cloudApiEndpoint ?? "";
  const url = `${base}${request.path ?? "/"}`;
  const method = request.method ?? "POST";

  if (config.identityEndpoint) {
    return signedFetch({
      method,
      url,
      service: request.service,
      region: REGION,
      headers: request.headers,
      body: request.body,
      credentials: await federatedCredentials(config),
    });
  }
  return fetch(url, {
    method,
    headers: new Headers(request.headers),
    body: method === "GET" || method === "HEAD" ? undefined : request.body,
    credentials: "include",
  });
}

/**
 * An AWS API failure with the service's own error code — the `__type` of an
 * awsjson1.1 error or the `Code` of a Query-protocol ErrorResponse — so a page
 * can act on the specific condition (AWS Organizations'
 * AWSOrganizationsNotInUseException drives the "Create an organization" state)
 * instead of pattern-matching message text.
 */
export class AwsApiError extends Error {
  readonly code: string;
  constructor(message: string, code: string) {
    super(message);
    this.name = "AwsApiError";
    this.code = code;
  }
}

// Both awsjson1.1 (ECS, ECR, CloudWatch Logs, AWS Organizations, and Lambda's
// REST-JSON error body) answer a failure as `{"__type": "...", "message":
// "..."}` (or the REST-JSON services' capitalised `Message`). Shared so every
// caller reports the same operation: code: message shape the AWS CLI does.
function awsJsonErrorFromBody(text: string, operationLabel: string, status: number): AwsApiError {
  let type = "";
  let message = "";
  try {
    const parsed = JSON.parse(text) as { __type?: string; message?: string; Message?: string };
    type = parsed.__type ?? "";
    message = parsed.message ?? parsed.Message ?? "";
  } catch {
    // The body was not the protocol's JSON error shape (a proxy error page,
    // an empty body); the HTTP status below is all the information there is.
  }
  const code = type.slice(type.lastIndexOf("#") + 1);
  return new AwsApiError(
    code ? `${operationLabel}: ${code}: ${message}` : `${operationLabel} returned HTTP ${status}`,
    code,
  );
}

// awsJson calls an AWS JSON (1.1) target operation and returns the parsed
// result — the protocol ECS, ECR, CloudWatch Logs, and AWS Organizations
// speak. A failure carries the protocol's `__type` error code (stripped of any
// `namespace#` prefix, the way SDKs resolve it) so the operator reads the real
// service error.
export async function awsJson<T>(service: string, target: string, input: Record<string, unknown> = {}): Promise<T> {
  const response = await awsFetch({
    service,
    method: "POST",
    path: "/",
    headers: { "content-type": "application/x-amz-json-1.1", "x-amz-target": target },
    body: JSON.stringify(input),
  });
  if (!response.ok) {
    const operation = target.slice(target.lastIndexOf(".") + 1);
    throw awsJsonErrorFromBody(await response.text(), operation, response.status);
  }
  return (await response.json()) as T;
}

// awsQuery calls an AWS Query protocol operation — the protocol AWS Identity
// and Access Management (IAM) and the Security Token Service speak: a
// form-encoded POST of Action and Version, answered in XML. A failure carries
// the protocol's ErrorResponse Code and Message, so the operator reads the real
// service error rather than a bare status code.
export async function awsQuery(
  service: string,
  version: string,
  action: string,
  params: Record<string, string> = {},
): Promise<Document> {
  const response = await awsFetch({
    service,
    method: "POST",
    path: "/",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ Action: action, Version: version, ...params }).toString(),
  });
  const xml = new DOMParser().parseFromString(await response.text(), "text/xml");
  if (!response.ok) {
    const code = xmlText(xml, "Code");
    const message = xmlText(xml, "Message");
    throw new Error(code ? `${action}: ${code}: ${message}` : `${action} returned HTTP ${response.status}`);
  }
  return xml;
}

// awsRestJson calls an AWS REST-JSON GET operation (Lambda's list surface).
export async function awsRestJson<T>(service: string, path: string): Promise<T> {
  const response = await awsFetch({ service, method: "GET", path });
  if (!response.ok) {
    throw new Error(`${path} returned HTTP ${response.status}`);
  }
  return (await response.json()) as T;
}

// awsRestJsonDelete calls an AWS REST-JSON DELETE operation (Lambda's
// DeleteFunction) that answers 204 No Content on success and the same
// `{"__type", "message"}` error body as awsJson on failure.
export async function awsRestJsonDelete(service: string, path: string): Promise<void> {
  const response = await awsFetch({ service, method: "DELETE", path });
  if (!response.ok) {
    throw awsJsonErrorFromBody(await response.text(), `DELETE ${path}`, response.status);
  }
}

// awsRestXml calls an AWS REST-XML GET operation (S3's ListBuckets) and returns
// the parsed document.
export async function awsRestXml(service: string, path: string): Promise<Document> {
  const response = await awsFetch({ service, method: "GET", path });
  if (!response.ok) {
    throw new Error(`${path} returned HTTP ${response.status}`);
  }
  return new DOMParser().parseFromString(await response.text(), "text/xml");
}

// awsRestXmlDelete calls an AWS REST-XML DELETE operation (S3's DeleteBucket)
// that answers 204 No Content on success and S3's XML `<Error>` body — Code
// and Message — on failure (BucketNotEmpty when objects remain, the same
// condition the real console reports).
export async function awsRestXmlDelete(service: string, path: string): Promise<void> {
  const response = await awsFetch({ service, method: "DELETE", path });
  if (!response.ok) {
    const xml = new DOMParser().parseFromString(await response.text(), "text/xml");
    const code = xmlText(xml, "Code");
    const message = xmlText(xml, "Message");
    throw new Error(code ? `${code}: ${message}` : `DELETE ${path} returned HTTP ${response.status}`);
  }
}

// awsRestXmlPut calls an AWS REST-XML PUT operation (S3's CreateBucket) that
// answers 200 with a Location header on success and S3's XML `<Error>` body —
// Code and Message — on failure (BucketAlreadyOwnedByYou when the bucket
// already exists outside us-east-1, the same condition the real console
// reports). The body is the operation's XML payload, or empty for a
// CreateBucket in this console's own Region (us-east-1 accepts no
// LocationConstraint — sending one there is itself a service error).
export async function awsRestXmlPut(service: string, path: string, body = ""): Promise<void> {
  const response = await awsFetch({ service, method: "PUT", path, body });
  if (!response.ok) {
    const xml = new DOMParser().parseFromString(await response.text(), "text/xml");
    const code = xmlText(xml, "Code");
    const message = xmlText(xml, "Message");
    throw new Error(code ? `${code}: ${message}` : `PUT ${path} returned HTTP ${response.status}`);
  }
}
