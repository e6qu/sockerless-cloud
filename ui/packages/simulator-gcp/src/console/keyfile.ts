import type { ServiceAccountKey } from "../api.js";

// The one-time key download, computed from the real CreateServiceAccountKey
// response: privateKeyData is the base64-encoded service-account credential
// file, and the console saves its decoded JSON under the real console's
// naming — `<project>-<key-id prefix>.json`.

// serviceAccountKeyId extracts the key id from the key's full resource name
// (projects/{p}/serviceAccounts/{email}/keys/{keyId}).
export function serviceAccountKeyId(key: Pick<ServiceAccountKey, "name">): string {
  return key.name.slice(key.name.lastIndexOf("/") + 1);
}

export interface ServiceAccountKeyFile {
  filename: string;
  contents: string;
}

// serviceAccountKeyFile decodes the create response's privateKeyData into the
// credential file to save. Only the create response carries privateKeyData —
// the API never returns it again, so a key listed or fetched later yields
// null: there is nothing to download.
export function serviceAccountKeyFile(project: string, key: ServiceAccountKey): ServiceAccountKeyFile | null {
  if (!key.privateKeyData) return null;
  const idPrefix = serviceAccountKeyId(key).replace(/[^a-zA-Z0-9]/g, "").slice(0, 12);
  return {
    filename: `${project}-${idPrefix}.json`,
    contents: atob(key.privateKeyData),
  };
}

// serviceAccountKeyDataURI carries the decoded credential file as a download
// href, self-contained in the page (no object URLs to revoke).
export function serviceAccountKeyDataURI(file: ServiceAccountKeyFile): string {
  return `data:application/json;charset=utf-8,${encodeURIComponent(file.contents)}`;
}
