import { API_VERSION, armResourceOf, type ArmEnvelope, type ArmGenericResource } from "./api.js";
import { armFetch, armListAll, armWrite } from "./portal/arm.js";

// A storage account's containers and blobs. Containers are ARM resources
// (blobServices/default/containers), as the portal manages them; blobs are
// read and written on the account's Blob service endpoint with an account
// SAS the ListAccountSas action issues, the portal's "Access key"
// authentication method. A browser reaches the Blob service only where the
// account's CORS rules allow the console's origin.

const BLOB_SERVICE_VERSION = "2023-11-03";

export const listContainers = async (accountId: string): Promise<ArmGenericResource[]> =>
  (await armListAll<ArmEnvelope>(`${accountId}/blobServices/default/containers?api-version=${API_VERSION.storage}`)).map(armResourceOf);

export async function createContainer(accountId: string, name: string, publicAccess: string): Promise<void> {
  await armWrite("PUT", `${accountId}/blobServices/default/containers/${name}?api-version=${API_VERSION.storage}`, { properties: { publicAccess } });
}

export async function deleteContainer(accountId: string, name: string): Promise<void> {
  await armWrite("DELETE", `${accountId}/blobServices/default/containers/${name}?api-version=${API_VERSION.storage}`);
}

async function accountSas(accountId: string): Promise<string> {
  const response = await armFetch(`${accountId}/ListAccountSas?api-version=${API_VERSION.storage}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      signedServices: "b",
      signedResourceTypes: "sco",
      signedPermission: "rwdlac",
      signedProtocol: "https,http",
      signedExpiry: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
    }),
  });
  const body = (await response.json()) as { accountSasToken?: string };
  if (!body.accountSasToken) throw new Error("ListAccountSas returned no token.");
  return body.accountSasToken;
}

export interface BlobItem {
  name: string;
  contentLength: number;
  contentType: string;
  lastModified: string;
  blobType: string;
  accessTier: string;
}

function blobUrl(endpoint: string, container: string, blob?: string): string {
  const base = endpoint.endsWith("/") ? endpoint : `${endpoint}/`;
  return `${base}${encodeURIComponent(container)}${blob ? `/${blob.split("/").map(encodeURIComponent).join("/")}` : ""}`;
}

async function blobFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("x-ms-version", BLOB_SERVICE_VERSION);
  let response: Response;
  try {
    response = await fetch(url, { ...init, headers });
  } catch {
    throw new Error(
      "The Blob service refused this origin. Add a CORS rule that allows it under Settings > Resource sharing (CORS), then try again.",
    );
  }
  if (!response.ok) {
    const xml = await response.text();
    const message = new DOMParser().parseFromString(xml, "application/xml").querySelector("Message")?.textContent;
    throw new Error(message || `The Blob service returned HTTP ${response.status}.`);
  }
  return response;
}

export async function listBlobs(accountId: string, endpoint: string, container: string, prefix = ""): Promise<BlobItem[]> {
  const sas = await accountSas(accountId);
  const items: BlobItem[] = [];
  let marker = "";
  do {
    const url = `${blobUrl(endpoint, container)}?restype=container&comp=list${prefix ? `&prefix=${encodeURIComponent(prefix)}` : ""}${marker ? `&marker=${encodeURIComponent(marker)}` : ""}&${sas}`;
    const doc = new DOMParser().parseFromString(await (await blobFetch(url)).text(), "application/xml");
    for (const node of Array.from(doc.querySelectorAll("Blobs > Blob"))) {
      const property = (tag: string) => node.querySelector(`Properties > ${tag}`)?.textContent ?? "";
      items.push({
        name: node.querySelector("Name")?.textContent ?? "",
        contentLength: Number(property("Content-Length") || "0"),
        contentType: property("Content-Type"),
        lastModified: property("Last-Modified"),
        blobType: property("BlobType"),
        accessTier: property("AccessTier"),
      });
    }
    marker = doc.querySelector("EnumerationResults > NextMarker")?.textContent ?? "";
  } while (marker);
  return items;
}

export async function uploadBlob(accountId: string, endpoint: string, container: string, file: File, overwrite: boolean): Promise<void> {
  const sas = await accountSas(accountId);
  const headers: Record<string, string> = {
    "x-ms-blob-type": "BlockBlob",
    "x-ms-blob-content-type": file.type || "application/octet-stream",
  };
  if (!overwrite) headers["If-None-Match"] = "*";
  await blobFetch(`${blobUrl(endpoint, container, file.name)}?${sas}`, { method: "PUT", headers, body: file });
}

export async function downloadBlob(accountId: string, endpoint: string, container: string, blob: string): Promise<Blob> {
  const sas = await accountSas(accountId);
  return (await blobFetch(`${blobUrl(endpoint, container, blob)}?${sas}`)).blob();
}

export async function deleteBlob(accountId: string, endpoint: string, container: string, blob: string): Promise<void> {
  const sas = await accountSas(accountId);
  await blobFetch(`${blobUrl(endpoint, container, blob)}?${sas}`, { method: "DELETE" });
}
