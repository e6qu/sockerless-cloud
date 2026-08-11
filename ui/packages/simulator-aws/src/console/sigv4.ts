// AWS Signature Version 4 signing in the browser, using Web Crypto so the
// console is fully self-contained. The console signs its real AWS API requests
// exactly as a real client does; only the endpoint and the credentials — the
// coordinates — change between the simulator and real AWS.

export interface AwsCredentials {
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken: string;
}

export interface SignedRequestInput {
  method: string;
  url: string;
  service: string;
  region: string;
  headers?: Record<string, string>;
  body?: string;
  credentials: AwsCredentials;
}

const encoder = new TextEncoder();

async function sha256Hex(data: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", encoder.encode(data));
  return hex(new Uint8Array(digest));
}

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

async function hmac(key: BufferSource, data: string): Promise<ArrayBuffer> {
  const cryptoKey = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return crypto.subtle.sign("HMAC", cryptoKey, encoder.encode(data));
}

async function signingKey(secret: string, dateStamp: string, region: string, service: string): Promise<ArrayBuffer> {
  const kDate = await hmac(encoder.encode(`AWS4${secret}`), dateStamp);
  const kRegion = await hmac(kDate, region);
  const kService = await hmac(kRegion, service);
  return hmac(kService, "aws4_request");
}

// signedFetch signs the request and sends it. The host header is signed from the
// request URL but not set on the fetch — the browser sets Host itself, and it
// matches what was signed.
export async function signedFetch(input: SignedRequestInput): Promise<Response> {
  const url = new URL(input.url, window.location.origin);
  const amzDate = new Date().toISOString().replace(/[:-]|\.\d{3}/g, "");
  const dateStamp = amzDate.slice(0, 8);
  const body = input.body ?? "";
  const payloadHash = await sha256Hex(body);

  const headers: Record<string, string> = {
    host: url.host,
    "x-amz-date": amzDate,
    "x-amz-security-token": input.credentials.sessionToken,
    ...Object.fromEntries(Object.entries(input.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v])),
  };
  const signedHeaderNames = Object.keys(headers).sort();
  const canonicalHeaders = signedHeaderNames.map((name) => `${name}:${headers[name].trim()}\n`).join("");
  const signedHeaders = signedHeaderNames.join(";");

  const canonicalRequest = [
    input.method,
    url.pathname || "/",
    url.searchParams.toString(),
    canonicalHeaders,
    signedHeaders,
    payloadHash,
  ].join("\n");

  const scope = `${dateStamp}/${input.region}/${input.service}/aws4_request`;
  const stringToSign = ["AWS4-HMAC-SHA256", amzDate, scope, await sha256Hex(canonicalRequest)].join("\n");
  const signature = hex(new Uint8Array(await hmac(await signingKey(input.credentials.secretAccessKey, dateStamp, input.region, input.service), stringToSign)));
  const authorization = `AWS4-HMAC-SHA256 Credential=${input.credentials.accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`;

  // Host is set by the browser; every other signed header is sent.
  const fetchHeaders = new Headers();
  for (const name of signedHeaderNames) {
    if (name !== "host") fetchHeaders.set(name, headers[name]);
  }
  fetchHeaders.set("authorization", authorization);

  return fetch(url.toString(), {
    method: input.method,
    headers: fetchHeaders,
    body: input.method === "GET" || input.method === "HEAD" ? undefined : body,
    credentials: "include",
  });
}
