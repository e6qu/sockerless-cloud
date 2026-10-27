// Gives a jsdom test the two things a browser gives the console and jsdom does
// not: a document served from the console's own origin, and a fetch with the
// browser's semantics on top of Node's — a relative URL resolves against the
// document, cookies travel per the request's credentials mode through the
// document's cookie jar, and redirects are followed hop by hop so every hop's
// cookies land in that jar. Every request still goes over the network to the
// real simulator.

// The parts of the JSDOM instance Vitest's jsdom environment exposes as the
// `jsdom` global that this file drives; jsdom ships no type declarations.
interface JSDOM {
  reconfigure(settings: { url: string }): void;
  cookieJar: {
    getCookieStringSync(url: string): string;
    setCookieSync(cookie: string, url: string, options: { ignoreError: boolean }): void;
  };
  window: { document: Document; fetch: typeof fetch };
}

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);
const MAX_REDIRECTS = 20;

function documentInstance(): JSDOM {
  const dom = (globalThis as { jsdom?: JSDOM }).jsdom;
  if (!dom) throw new Error("the console browser needs Vitest's jsdom environment");
  return dom;
}

function requestURL(input: RequestInfo | URL, base: string): URL {
  if (input instanceof URL) return new URL(input.href);
  if (typeof input === "string") return new URL(input, base);
  return new URL(input.url, base);
}

export function installConsoleBrowser(origin: string): void {
  const dom = documentInstance();
  dom.reconfigure({ url: `${origin}/ui/` });
  const jar = dom.cookieJar;
  const nodeFetch = globalThis.fetch;

  const browserFetch = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const page = new URL(dom.window.document.baseURI);
    let url = requestURL(input, page.href);
    let method = (init.method ?? "GET").toUpperCase();
    let body = init.body;
    const credentials = init.credentials ?? "same-origin";
    for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
      const headers = new Headers(init.headers);
      if (credentials === "include" || (credentials === "same-origin" && url.origin === page.origin)) {
        const cookie = jar.getCookieStringSync(url.href);
        if (cookie) headers.set("cookie", cookie);
      }
      if (method !== "GET" && method !== "HEAD") headers.set("origin", page.origin);
      const response = await nodeFetch(url, { method, headers, body, redirect: "manual", signal: init.signal });
      if (credentials !== "omit") {
        for (const cookie of response.headers.getSetCookie()) {
          jar.setCookieSync(cookie, url.href, { ignoreError: true });
        }
      }
      const location = response.headers.get("location");
      if (init.redirect === "manual" || !REDIRECT_STATUSES.has(response.status) || !location) {
        return response;
      }
      if (init.redirect === "error") throw new TypeError(`fetch ${url.href} redirected`);
      url = new URL(location, url);
      if (response.status === 303 || ((response.status === 301 || response.status === 302) && method === "POST")) {
        method = "GET";
        body = undefined;
      }
    }
    throw new TypeError(`fetch ${url.href} redirected more than ${MAX_REDIRECTS} times`);
  };
  globalThis.fetch = browserFetch as typeof fetch;
  dom.window.fetch = browserFetch as typeof fetch;
}

/** A resource name no other test or run shares, for tests that share one simulator. */
export function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

// signInToConsole opens the console the way an operator's browser does: the
// console's auth layer redirects to the OpenID Connect issuer, the issuer signs
// the directory's default user in and redirects back, and the console's session
// cookie lands in the document's cookie jar.
export async function signInToConsole(): Promise<void> {
  const response = await fetch("/ui/", { credentials: "include" });
  await response.body?.cancel();
  if (!response.ok || new URL(response.url).pathname !== "/ui/") {
    throw new Error(`signing in to the console ended at ${response.url} with HTTP ${response.status}`);
  }
}
