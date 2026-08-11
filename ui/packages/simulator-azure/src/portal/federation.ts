// The portal reads the real Azure Resource Manager APIs, and it reaches them
// exactly as it would reach real Azure: it federates the signed-in operator's
// Shauth assertion into an Azure token through Microsoft Entra's own Workload
// Identity Federation. Only the coordinates — the endpoint base URLs and the
// identity the console federates as — change between the simulator and real
// Azure; there is no simulator-versus-cloud branch.
//
// Unlike Azure Resource Manager and Microsoft Graph, real Microsoft Entra
// serves no CORS for the client_credentials token endpoint, so a browser
// cannot read that response cross-origin — the exchange itself runs
// server-side, in the console's own federation broker
// (simulator-azure/shared/federation_broker.go): the portal asks its own
// origin for a token, the broker performs the client_credentials grant with
// the signed-in operator's assertion (which never leaves the server), and
// returns only the token and its lifetime. Everything after the token — Azure
// Resource Manager, Microsoft Graph, and Log Analytics reads — stays
// browser-side against the cloud's own CORS-serving surfaces, reached
// directly by the SPA exactly as it would reach real Azure.
//
// Whether a credential is attached is a real deployment condition, not a
// fallback: a portal wired to a cloud identity federates the operator and
// every call carries a token, and a federation failure there is surfaced
// rather than hidden; a portal with no cloud identity configured — a local or
// test instance, or a console auth layer with no cloud identity assigned yet —
// runs unauthenticated, the mode the account control reports. The two are
// distinguished by the portal's own configuration, never guessed.

interface PortalConfig {
  identityEndpoint?: string;
  // Cloud data-plane coordinates. Empty means the portal's own origin.
  cloudApiEndpoint?: string;
  // Azure Monitor's Log Analytics query API is a distinct host from Azure
  // Resource Manager (api.loganalytics.io vs management.azure.com), reached with
  // its own token audience. Empty means the portal's own origin.
  logsApiEndpoint?: string;
  // Microsoft Graph is likewise its own host (graph.microsoft.com) with its
  // own token audience. Empty means the portal's own origin.
  graphApiEndpoint?: string;
  // The console's own server-side federation broker: GET
  // "${federationTokenEndpoint}?scope=arm|logs|graph" with the operator's
  // session cookie returns a short-lived Azure token for that resource. Empty
  // means this deployment has no cloud identity to federate the operator
  // into — the tenant and client ID the exchange itself needs live at the
  // broker, never in this browser-visible config.
  federationTokenEndpoint?: string;
  // The Microsoft Entra directory tenant this deployment federates into.
  // Display-only: CLI usage samples (e.g. the app registration blade's
  // `az login --tenant`) show it; the exchange itself no longer needs it
  // client-side.
  federationTenant?: string;
}

// Azure Resource Manager, Log Analytics, and Microsoft Graph are separate
// resources, each reached with a token scoped to it. The broker resolves
// these short names to Microsoft Entra's own resource scopes server-side.
export type CloudScope = "arm" | "logs" | "graph";

let configPromise: Promise<PortalConfig> | null = null;
const cachedTokens: Partial<Record<CloudScope, { value: string; expiresAt: number }>> = {};
// One in-flight exchange per scope: a portal render fires many authorized
// reads at once, and without deduplication each of them would start its own
// broker exchange before the first response lands in cachedTokens.
const inflightTokens: Partial<Record<CloudScope, Promise<string>>> = {};

async function portalConfig(): Promise<PortalConfig> {
  if (!configPromise) {
    configPromise = fetch("/ui/config.json", { credentials: "include" }).then((response) => {
      if (!response.ok) throw new Error(`/ui/config.json returned HTTP ${response.status}`);
      return response.json() as Promise<PortalConfig>;
    });
  }
  return configPromise;
}

// resolveFederation turns the portal's configuration into an explicit
// decision, with no implicit default: federationTokenEndpoint is the sole
// signal that this deployment intends to federate the operator into Azure.
// The server only ever sets it alongside identityEndpoint (see
// simulator-azure/shared/server.go's RegisterUI), so there is no partial
// state to reject here the way there was when the browser held the tenant
// and client ID itself — a console auth layer with no cloud identity
// configured is a supported deployment shape (unauthenticated reads), not a
// broken one.
function resolveFederation(config: PortalConfig): string | null {
  return config.federationTokenEndpoint || null;
}

// federatedToken asks the console's own federation broker for a short-lived
// Azure token scoped to a resource. The broker performs the real Microsoft
// Entra Workload Identity Federation client-assertion grant server-side (see
// federation.ts's module comment); this call only ever reaches the portal's
// own origin.
async function federatedToken(tokenEndpoint: string, scope: CloudScope): Promise<string> {
  const cached = cachedTokens[scope];
  if (cached && cached.expiresAt - 30_000 > Date.now()) {
    return cached.value;
  }

  const inflight = inflightTokens[scope];
  if (inflight) {
    return inflight;
  }
  const exchange = (async () => {
    const now = Date.now();
    const response = await fetch(`${tokenEndpoint}?scope=${scope}`, { credentials: "include" });
    if (!response.ok) {
      throw new Error(`console federation broker returned HTTP ${response.status}`);
    }
    const token = (await response.json()) as { access_token: string; expires_in: number };
    cachedTokens[scope] = { value: token.access_token, expiresAt: now + token.expires_in * 1000 };
    return token.access_token;
  })();
  inflightTokens[scope] = exchange;
  try {
    return await exchange;
  } finally {
    delete inflightTokens[scope];
  }
}

function baseFor(config: PortalConfig, scope: CloudScope): string {
  if (scope === "logs") return config.logsApiEndpoint ?? "";
  if (scope === "graph") return config.graphApiEndpoint ?? "";
  return config.cloudApiEndpoint ?? "";
}

// directoryTenantId is the Microsoft Entra tenant the portal federates into —
// the value `az login --service-principal --tenant …` takes. Null when the
// portal runs without federation configured.
export async function directoryTenantId(): Promise<string | null> {
  const config = await portalConfig();
  return config.federationTenant ?? null;
}

// authorizedFetch reaches a real Azure path at the configured cloud coordinate
// for the given scope, attaching a scope-specific federated credential when the
// portal is configured to federate (see resolveFederation).
export async function authorizedFetch(path: string, scope: CloudScope = "arm", init: RequestInit = {}): Promise<Response> {
  const config = await portalConfig();
  const tokenEndpoint = resolveFederation(config);
  const headers = new Headers(init.headers);
  if (tokenEndpoint) {
    headers.set("Authorization", `Bearer ${await federatedToken(tokenEndpoint, scope)}`);
  }
  // The cloud call authenticates with the federated Bearer token, never a
  // cookie — exactly as a browser reaches real Azure Resource Manager and
  // Microsoft Graph. Omitting credentials is also what makes the cross-origin
  // read work: real ARM/Graph answer a browser with Access-Control-Allow-Origin
  // but not Access-Control-Allow-Credentials (they are token-, not
  // cookie-authenticated), so a credentialed request would be rejected. Only
  // the console's own same-origin endpoints (config, the federation broker)
  // carry the session cookie.
  return fetch(`${baseFor(config, scope)}${path}`, { ...init, headers, credentials: "omit" });
}

// authorizedJSON reads a real Azure API path as JSON, raising the API's own
// error rather than masking it.
export async function authorizedJSON<T>(path: string, scope: CloudScope = "arm"): Promise<T> {
  const response = await authorizedFetch(path, scope);
  if (!response.ok) {
    throw new Error(`${path} returned HTTP ${response.status}`);
  }
  return (await response.json()) as T;
}

// authorizedJSONPost posts a JSON body to a real Azure API path — the shape
// Log Analytics' query endpoint and other POST reads take.
export async function authorizedJSONPost<T>(path: string, requestBody: unknown, scope: CloudScope = "arm"): Promise<T> {
  const response = await authorizedFetch(path, scope, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(requestBody),
  });
  if (!response.ok) {
    throw new Error(`${path} returned HTTP ${response.status}`);
  }
  return (await response.json()) as T;
}
