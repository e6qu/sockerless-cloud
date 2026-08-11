// The console reads the real Google Cloud APIs, and it reaches them exactly as
// it would reach the real cloud: it federates the signed-in operator's Shauth
// assertion into a cloud credential through the cloud's own Security Token
// Service, then calls the real API paths with it. Only the coordinates — the
// endpoint base URLs and the federation audience — change between the simulator
// and the real cloud. There is no simulator-versus-cloud branch and no
// simulator-served credential broker.
//
// Whether a credential is attached is a real deployment condition, not a
// fallback: a console wired to an identity provider federates the operator and
// every call carries a token, and a federation failure there is surfaced rather
// than hidden; a console with no identity provider — a local or test instance —
// runs unauthenticated, the mode the account control reports. The two are
// distinguished by the console's own configuration, never guessed.

interface ConsoleConfig {
  identityEndpoint?: string;
  // Cloud data-plane coordinates. Empty means the console's own origin.
  cloudApiEndpoint?: string;
  federationEndpoint?: string;
  federationAudience?: string;
  // Where the console's auth layer exposes the operator's assertion.
  federationSubject?: string;
}

let configPromise: Promise<ConsoleConfig> | null = null;
let cachedToken: { value: string; expiresAt: number } | null = null;
let inflightToken: Promise<string> | null = null;

async function consoleConfig(): Promise<ConsoleConfig> {
  if (!configPromise) {
    configPromise = fetch("/ui/config.json", { credentials: "include" }).then((response) => {
      if (!response.ok) throw new Error(`/ui/config.json returned HTTP ${response.status}`);
      return response.json() as Promise<ConsoleConfig>;
    });
  }
  return configPromise;
}

// federatedToken exchanges the operator's assertion for a short-lived cloud
// access token at the cloud's Security Token Service — the real Workforce
// Identity Federation token exchange, at whichever coordinate the console is
// configured for.
async function federatedToken(config: ConsoleConfig): Promise<string> {
  if (cachedToken && cachedToken.expiresAt - 30_000 > Date.now()) {
    return cachedToken.value;
  }
  // One in-flight exchange: a console render fires many authorized reads at
  // once, and without deduplication each would start its own token exchange
  // before the first response lands in cachedToken.
  if (inflightToken) {
    return inflightToken;
  }
  inflightToken = exchangeFederatedToken(config);
  try {
    return await inflightToken;
  } finally {
    inflightToken = null;
  }
}

async function exchangeFederatedToken(config: ConsoleConfig): Promise<string> {
  const now = Date.now();
  const subjectResponse = await fetch(config.federationSubject!, { credentials: "include" });
  if (!subjectResponse.ok) {
    throw new Error(`could not read the operator assertion: HTTP ${subjectResponse.status}`);
  }
  const { subject_token: subjectToken } = (await subjectResponse.json()) as { subject_token: string };

  const body = new URLSearchParams({
    grant_type: "urn:ietf:params:oauth:grant-type:token-exchange",
    audience: config.federationAudience!,
    subject_token: subjectToken,
    subject_token_type: "urn:ietf:params:oauth:token-type:jwt",
    requested_token_type: "urn:ietf:params:oauth:token-type:access_token",
    scope: "https://www.googleapis.com/auth/cloud-platform",
  });
  const exchange = await fetch(`${config.federationEndpoint ?? ""}/v1/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
  });
  if (!exchange.ok) {
    throw new Error(`Security Token Service exchange failed: HTTP ${exchange.status}`);
  }
  const token = (await exchange.json()) as { access_token: string; expires_in: number };
  cachedToken = { value: token.access_token, expiresAt: now + token.expires_in * 1000 };
  return token.access_token;
}

// authorizedFetch reaches a real Google Cloud API path at the configured cloud
// coordinate, attaching a federated credential when the console is wired to an
// identity provider.
export async function authorizedFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const config = await consoleConfig();
  const headers = new Headers(init.headers);
  if (config.identityEndpoint) {
    headers.set("Authorization", `Bearer ${await federatedToken(config)}`);
  }
  return fetch(`${config.cloudApiEndpoint ?? ""}${path}`, { ...init, headers, credentials: "include" });
}

// A Google Cloud API failure with the service's own `status` (the enum GCP's
// JSON error body carries, e.g. "ALREADY_EXISTS", "NOT_FOUND") and HTTP
// `code`, so a page can act on the specific condition — the Cloud Storage
// bucket-name-taken 409 and the Artifact Registry repository-already-exists
// 409 both read the real `error.message` — instead of a bare HTTP status.
export class GcpApiError extends Error {
  readonly status: number;
  readonly reason: string;
  constructor(message: string, status: number, reason: string) {
    super(message);
    this.name = "GcpApiError";
    this.status = status;
    this.reason = reason;
  }
}

// Every Google Cloud JSON API answers a failure as
// `{"error": {"code", "message", "status", "details"}}` (GCPError in the
// simulator, the same shape the real API's googleapi.Error unmarshals).
// Shared so every caller reports the real service message rather than a bare
// status code.
async function gcpErrorFromResponse(path: string, response: Response): Promise<GcpApiError> {
  let message = "";
  let reason = "";
  try {
    const parsed = (await response.json()) as { error?: { message?: string; status?: string } };
    message = parsed.error?.message ?? "";
    reason = parsed.error?.status ?? "";
  } catch {
    // The body was not the API's JSON error shape (a proxy error page, an
    // empty body); the HTTP status below is all the information there is.
  }
  return new GcpApiError(
    message ? `${path}: ${message}` : `${path} returned HTTP ${response.status}`,
    response.status,
    reason,
  );
}

// authorizedJSON reads a real Google Cloud API path as JSON, raising the API's
// own error rather than masking it.
export async function authorizedJSON<T>(path: string): Promise<T> {
  const response = await authorizedFetch(path);
  if (!response.ok) {
    throw await gcpErrorFromResponse(path, response);
  }
  return (await response.json()) as T;
}

// authorizedJSONPost posts a JSON body to a real Google Cloud API path — the
// shape Cloud Logging's `entries:list` and other list-by-POST methods take.
export async function authorizedJSONPost<T>(path: string, body: unknown): Promise<T> {
  const response = await authorizedFetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await gcpErrorFromResponse(path, response);
  }
  return (await response.json()) as T;
}

// authorizedJSONPatch sends a partial update to a real Google Cloud API path —
// the shape storage.buckets.patch, artifactregistry repositories.patch,
// functions.patch and jobs.patch take. The update mask, where the operation
// uses one, rides on the path's query string the way the real API expects it.
export async function authorizedJSONPatch<T>(path: string, body: unknown): Promise<T> {
  const response = await authorizedFetch(path, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await gcpErrorFromResponse(path, response);
  }
  return (await response.json()) as T;
}

// authorizedJSONDelete deletes a real Google Cloud API resource, raising the
// API's own error rather than masking it. Most Google Cloud deletes answer
// with a JSON body (an Empty `{}`, or a long-running Operation); a few —
// storage.buckets.delete among them — answer 204 No Content with nothing to
// parse, which the caller reads as an empty value rather than a parse error.
export async function authorizedJSONDelete<T>(path: string): Promise<T> {
  const response = await authorizedFetch(path, { method: "DELETE" });
  if (!response.ok) {
    throw await gcpErrorFromResponse(path, response);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

// cloudEndpoint resolves the cloud data-plane coordinate the console is
// configured for — the console's own origin when embedded in the simulator,
// the real cloud host otherwise. Surfaced so pages that print vendor-CLI
// usage name the same coordinate every console API call already uses.
export async function cloudEndpoint(): Promise<string> {
  const config = await consoleConfig();
  return config.cloudApiEndpoint || window.location.origin;
}
