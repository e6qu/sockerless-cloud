import { authorizedFetch, type CloudScope } from "./federation.js";

// The Azure Resource Manager client every blade writes through. Reads are a
// GET; writes follow ARM's asynchronous-operation contract: a 201 or 202 that
// names an Azure-AsyncOperation or Location URL is polled until the operation
// settles, waiting the Retry-After the service sends between polls.

export class ArmError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(message);
    this.name = "ArmError";
  }
}

/** The path and query of an absolute URL ARM returned (nextLink, Location,
 *  Azure-AsyncOperation), which authorizedFetch prefixes with the configured
 *  cloud coordinate. ARM emits them on the host the request reached. */
export function armPathOf(url: string): string {
  const parsed = new URL(url, "http://arm.invalid");
  return `${parsed.pathname}${parsed.search}`;
}

async function errorOf(response: Response, path: string): Promise<ArmError> {
  let code = "";
  let message = "";
  try {
    // Log Analytics nests the reason a query failed under innererror.
    interface ErrorBody {
      code?: string;
      message?: string;
      innererror?: ErrorBody;
    }
    const body = (await response.json()) as { error?: ErrorBody; message?: string };
    code = body.error?.code ?? "";
    const reasons: string[] = [];
    for (let entry: ErrorBody | undefined = body.error; entry; entry = entry.innererror) {
      if (entry.message && !reasons.includes(entry.message)) reasons.push(entry.message);
    }
    message = reasons.join(" ") || body.message || "";
  } catch {
    // A body that is not JSON leaves the HTTP status as the message.
  }
  return new ArmError(message || `${path} returned HTTP ${response.status}`, response.status, code);
}

export async function armFetch(path: string, init: RequestInit = {}, scope: CloudScope = "arm"): Promise<Response> {
  const response = await authorizedFetch(path, scope, init);
  if (!response.ok) throw await errorOf(response, path);
  return response;
}

export async function armGet<T>(path: string, scope: CloudScope = "arm"): Promise<T> {
  return (await (await armFetch(path, {}, scope)).json()) as T;
}

export interface ArmPage<T> {
  value: T[];
  nextLink?: string;
}

export async function armGetPage<T>(path: string): Promise<ArmPage<T>> {
  const page = await armGet<{ value?: T[]; nextLink?: string }>(path);
  return { value: page.value ?? [], nextLink: page.nextLink ? armPathOf(page.nextLink) : undefined };
}

/** Reads a list to its end, following every nextLink. */
export async function armListAll<T>(path: string): Promise<T[]> {
  const items: T[] = [];
  let next: string | undefined = path;
  while (next) {
    const page: ArmPage<T> = await armGetPage<T>(next);
    items.push(...page.value);
    next = page.nextLink;
  }
  return items;
}

// @azure/core-lro waits 2 seconds between polls when the service sends no
// Retry-After; the console follows the same documented cadence.
const DEFAULT_POLL_SECONDS = 2;

function retryAfterMs(response: Response): number {
  const header = response.headers.get("Retry-After");
  if (header) {
    const seconds = Number(header);
    if (Number.isFinite(seconds)) return Math.max(0, seconds) * 1000;
    const date = Date.parse(header);
    if (Number.isFinite(date)) return Math.max(0, date - Date.now());
  }
  return DEFAULT_POLL_SECONDS * 1000;
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export type OperationStatus = "InProgress" | "Succeeded" | "Failed" | "Canceled";

export interface OperationProgress {
  status: OperationStatus;
  /** The operation's status URL, when the service runs it asynchronously. */
  operationUrl?: string;
}

interface AsyncOperationBody {
  status?: string;
  error?: { code?: string; message?: string };
}

/**
 * Sends a write and settles it. A synchronous answer returns its body. An
 * asynchronous one is polled: Azure-AsyncOperation reports `status` until a
 * terminal state, and then the resource (or the Location result) is read; a
 * Location alone answers 202 until the operation completes.
 */
export async function armWrite<T = unknown>(
  method: "PUT" | "PATCH" | "POST" | "DELETE",
  path: string,
  body?: unknown,
  onProgress?: (progress: OperationProgress) => void,
): Promise<T | undefined> {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  const first = await armFetch(path, init);
  const asyncOperation = first.headers.get("Azure-AsyncOperation");
  const location = first.headers.get("Location");
  const firstBody = await readBody<T>(first);
  if (first.status !== 201 && first.status !== 202) return firstBody;
  if (!asyncOperation && !location) return firstBody;

  const statusUrl = armPathOf((asyncOperation ?? location)!);
  onProgress?.({ status: "InProgress", operationUrl: statusUrl });
  let delay = retryAfterMs(first);

  if (asyncOperation) {
    for (;;) {
      await wait(delay);
      const poll = await armFetch(statusUrl);
      const operation = (await poll.json()) as AsyncOperationBody;
      const status = (operation.status ?? "InProgress") as OperationStatus;
      if (status === "Succeeded") break;
      if (status === "Failed" || status === "Canceled") {
        onProgress?.({ status, operationUrl: statusUrl });
        throw new ArmError(
          operation.error?.message || `The operation ${status.toLowerCase()}.`,
          200,
          operation.error?.code ?? status,
        );
      }
      delay = retryAfterMs(poll);
    }
    onProgress?.({ status: "Succeeded", operationUrl: statusUrl });
    if (method === "DELETE" || method === "POST") {
      return location ? readBody<T>(await armFetch(armPathOf(location))) : undefined;
    }
    return armGet<T>(path);
  }

  for (;;) {
    await wait(delay);
    const poll = await armFetch(statusUrl);
    if (poll.status !== 202) {
      onProgress?.({ status: "Succeeded", operationUrl: statusUrl });
      if (method === "PUT" || method === "PATCH") return armGet<T>(path);
      return readBody<T>(poll);
    }
    delay = retryAfterMs(poll);
  }
}

async function readBody<T>(response: Response): Promise<T | undefined> {
  const text = await response.text();
  if (!text) return undefined;
  try {
    return JSON.parse(text) as T;
  } catch {
    return undefined;
  }
}
