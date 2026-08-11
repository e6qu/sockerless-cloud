import { describe, expect, it } from "vitest";
import {
  serviceAccountKeyDataURI,
  serviceAccountKeyFile,
  serviceAccountKeyId,
} from "../console/keyfile.js";

const KEY_NAME =
  "projects/sockerless/serviceAccounts/runner@sockerless.iam.gserviceaccount.com/keys/7e1f42ac-fdcc-289c-2695-aa82726b8f71";

// A real-shape credential file, base64-encoded the way the API's
// privateKeyData carries it.
const CREDENTIALS = JSON.stringify({
  type: "service_account",
  project_id: "sockerless",
  private_key_id: "7e1f42ac-fdcc-289c-2695-aa82726b8f71",
  private_key: "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n",
  client_email: "runner@sockerless.iam.gserviceaccount.com",
  token_uri: "https://oauth2.googleapis.com/token",
});

describe("serviceAccountKeyId", () => {
  it("extracts the key id from the full resource name", () => {
    expect(serviceAccountKeyId({ name: KEY_NAME })).toBe("7e1f42ac-fdcc-289c-2695-aa82726b8f71");
  });
});

describe("serviceAccountKeyFile", () => {
  it("names the download like the real console — project plus a key-id prefix", () => {
    const file = serviceAccountKeyFile("sockerless", { name: KEY_NAME, privateKeyData: btoa(CREDENTIALS) });
    expect(file).not.toBeNull();
    expect(file!.filename).toBe("sockerless-7e1f42acfdcc.json");
  });

  it("decodes privateKeyData into the credential file's JSON", () => {
    const file = serviceAccountKeyFile("sockerless", { name: KEY_NAME, privateKeyData: btoa(CREDENTIALS) });
    expect(file!.contents).toBe(CREDENTIALS);
    const parsed = JSON.parse(file!.contents) as Record<string, string>;
    expect(parsed.type).toBe("service_account");
    expect(parsed.client_email).toBe("runner@sockerless.iam.gserviceaccount.com");
  });

  it("yields nothing for a key fetched after creation — the API returns privateKeyData exactly once", () => {
    expect(serviceAccountKeyFile("sockerless", { name: KEY_NAME })).toBeNull();
  });
});

describe("serviceAccountKeyDataURI", () => {
  it("round-trips the file contents through the download href", () => {
    const file = serviceAccountKeyFile("sockerless", { name: KEY_NAME, privateKeyData: btoa(CREDENTIALS) })!;
    const uri = serviceAccountKeyDataURI(file);
    expect(uri.startsWith("data:application/json;charset=utf-8,")).toBe(true);
    expect(decodeURIComponent(uri.slice(uri.indexOf(",") + 1))).toBe(CREDENTIALS);
  });
});
