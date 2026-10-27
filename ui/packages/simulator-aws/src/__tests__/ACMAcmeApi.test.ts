import { beforeAll, describe, expect, it } from "vitest";
import { signInToConsole, uniqueName } from "@sockerless/ui-core/testing";
import {
  createACMAcmeDomainValidation,
  createACMAcmeEndpoint,
  createACMAcmeExternalAccountBinding,
  deleteACMAcmeDomainValidation,
  deleteACMAcmeEndpoint,
  fetchACMAcmeAccounts,
  fetchACMAcmeDomainValidations,
  fetchACMAcmeEndpoints,
  fetchACMAcmeExternalAccountBindings,
  getACMAcmeExternalAccountBindingCredentials,
  revokeACMAcmeAccount,
  revokeACMAcmeExternalAccountBinding,
} from "../api.js";
import { awsFetch } from "../console/federation.js";

beforeAll(signInToConsole);

const ROLE_ARN = "arn:aws:iam::123456789012:role/AcmeClient";

// createEndpoint creates an AWS Certificate Manager ACME endpoint through the
// console and returns its ARN, the one ListAcmeEndpoints did not list before.
async function createEndpoint(): Promise<string> {
  const before = new Set((await fetchACMAcmeEndpoints()).map((endpoint) => endpoint.acmeEndpointArn));
  await createACMAcmeEndpoint("REQUIRED");
  const created = (await fetchACMAcmeEndpoints()).filter((endpoint) => !before.has(endpoint.acmeEndpointArn));
  expect(created).toHaveLength(1);
  return created[0].acmeEndpointArn;
}

// createHostedZone creates the Amazon Route 53 public hosted zone a DNS
// prevalidation writes its record into, and returns its bare ID.
async function createHostedZone(domain: string): Promise<string> {
  const response = await awsFetch({
    service: "route53",
    method: "POST",
    path: "/2013-04-01/hostedzone",
    headers: { "content-type": "application/xml" },
    body:
      `<CreateHostedZoneRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">` +
      `<Name>${domain}.</Name><CallerReference>${uniqueName("acme")}</CallerReference></CreateHostedZoneRequest>`,
  });
  const xml = new DOMParser().parseFromString(await response.text(), "text/xml");
  expect(response.status).toBe(201);
  return (xml.getElementsByTagName("Id")[0]?.textContent ?? "").replace("/hostedzone/", "");
}

const encoder = new TextEncoder();

function base64url(bytes: ArrayBuffer | Uint8Array): string {
  let binary = "";
  for (const byte of new Uint8Array(bytes)) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64url(text: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(text.replace(/-/g, "+").replace(/_/g, "/")), (char) => char.charCodeAt(0));
}

function base64urlJSON(value: unknown): string {
  return base64url(encoder.encode(JSON.stringify(value)));
}

// registerAcmeAccount registers an RFC 8555 account at the endpoint's
// directory the way an ACME client does: an ES256 account key, bound to the
// AWS account through the external account binding's HS256 MAC.
async function registerAcmeAccount(directoryUrl: string, binding: { keyId: string; macKey: string }, contact: string) {
  const directoryResponse = await fetch(directoryUrl);
  expect(directoryResponse.status).toBe(200);
  const directory = (await directoryResponse.json()) as { newAccount: string };
  const nonce = directoryResponse.headers.get("replay-nonce") ?? "";

  const accountKey = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  const exported = await crypto.subtle.exportKey("jwk", accountKey.publicKey);
  const jwk = { crv: exported.crv, kty: exported.kty, x: exported.x, y: exported.y };

  const bindingProtected = base64urlJSON({ alg: "HS256", kid: binding.keyId, url: directory.newAccount });
  const bindingPayload = base64urlJSON(jwk);
  const macKey = await crypto.subtle.importKey(
    "raw",
    fromBase64url(binding.macKey),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const bindingSignature = await crypto.subtle.sign("HMAC", macKey, encoder.encode(`${bindingProtected}.${bindingPayload}`));

  const protectedHeader = base64urlJSON({ alg: "ES256", jwk, nonce, url: directory.newAccount });
  const payload = base64urlJSON({
    termsOfServiceAgreed: true,
    contact: [contact],
    externalAccountBinding: { protected: bindingProtected, payload: bindingPayload, signature: base64url(bindingSignature) },
  });
  const signature = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    accountKey.privateKey,
    encoder.encode(`${protectedHeader}.${payload}`),
  );
  const response = await fetch(directory.newAccount, {
    method: "POST",
    headers: { "content-type": "application/jose+json" },
    body: JSON.stringify({ protected: protectedHeader, payload, signature: base64url(signature) }),
  });
  expect(response.status).toBe(201);
  return response.headers.get("location") ?? "";
}

describe("AWS Certificate Manager ACME browser data plane", () => {
  it("projects the real control-plane response shapes into console resources", async () => {
    const endpointArn = await createEndpoint();
    const endpoints = await fetchACMAcmeEndpoints();
    const endpoint = endpoints.find((candidate) => candidate.acmeEndpointArn === endpointArn);
    expect(endpoint).toMatchObject({
      acmeEndpointArn: endpointArn,
      status: "ACTIVE",
      contact: "REQUIRED",
      authorizationBehavior: "PRE_APPROVED",
    });
    expect(endpoint?.endpointUrl).toMatch(/^http:\/\//);
    expect(endpoint?.createdAt).toBeGreaterThan(0);

    const domain = `${uniqueName("service")}.example.com`;
    const zoneId = await createHostedZone(domain);
    await createACMAcmeDomainValidation(endpointArn, domain, zoneId);
    const validations = await fetchACMAcmeDomainValidations(endpointArn);
    expect(validations).toHaveLength(1);
    expect(validations[0]).toMatchObject({ domainName: domain, status: "VALID" });
    expect(validations[0].recordName).toMatch(new RegExp(`^_[0-9a-f]+\\.${domain.replace(/\./g, "\\.")}\\.$`));
    expect(validations[0].recordValue).toMatch(/\.acm-validations\.aws\.$/);

    const binding = await createACMAcmeExternalAccountBinding(endpointArn, ROLE_ARN);
    const bindings = await fetchACMAcmeExternalAccountBindings(endpointArn);
    expect(bindings).toEqual([binding]);
    expect(binding.roleArn).toBe(ROLE_ARN);
    expect(binding.expiresAt - binding.createdAt).toBe(7 * 24 * 60 * 60);

    const credentials = await getACMAcmeExternalAccountBindingCredentials(binding.acmeExternalAccountBindingArn);
    expect(credentials.keyId).not.toBe("");
    expect(credentials.macKey).not.toBe("");
    const accountUrl = await registerAcmeAccount(endpoint!.endpointUrl, credentials, "mailto:operator@example.com");
    const accounts = await fetchACMAcmeAccounts(endpointArn);
    expect(accounts).toHaveLength(1);
    expect(accounts[0]).toMatchObject({ accountUrl, contacts: ["mailto:operator@example.com"], status: "VALID" });
    expect(accounts[0].publicKeyThumbprint).toMatch(/^[A-Za-z0-9_-]{43}$/);
  });

  it("sends every operator mutation to the matching public AWS operation", async () => {
    const endpointArn = await createEndpoint();
    const domain = `${uniqueName("mutations")}.example.com`;
    await createACMAcmeDomainValidation(endpointArn, domain, await createHostedZone(domain));
    const [validation] = await fetchACMAcmeDomainValidations(endpointArn);
    const endpointUrl = (await fetchACMAcmeEndpoints()).find((e) => e.acmeEndpointArn === endpointArn)!.endpointUrl;

    const accountBinding = await createACMAcmeExternalAccountBinding(endpointArn, ROLE_ARN);
    const accountUrl = await registerAcmeAccount(
      endpointUrl,
      await getACMAcmeExternalAccountBindingCredentials(accountBinding.acmeExternalAccountBindingArn),
      "mailto:revoked@example.com",
    );
    await revokeACMAcmeAccount(endpointArn, accountUrl);
    expect((await fetchACMAcmeAccounts(endpointArn)).find((a) => a.accountUrl === accountUrl)?.status).toBe("REVOKED");

    const unusedBinding = await createACMAcmeExternalAccountBinding(endpointArn, ROLE_ARN);
    await revokeACMAcmeExternalAccountBinding(unusedBinding.acmeExternalAccountBindingArn);
    const revoked = (await fetchACMAcmeExternalAccountBindings(endpointArn)).find(
      (candidate) => candidate.acmeExternalAccountBindingArn === unusedBinding.acmeExternalAccountBindingArn,
    );
    expect(revoked?.revokedAt).toBeGreaterThan(0);

    // DeleteAcmeEndpoint refuses an endpoint that still holds an active binding.
    await revokeACMAcmeExternalAccountBinding(accountBinding.acmeExternalAccountBindingArn);
    await deleteACMAcmeDomainValidation(validation.acmeDomainValidationArn);
    expect(await fetchACMAcmeDomainValidations(endpointArn)).toEqual([]);

    await deleteACMAcmeEndpoint(endpointArn);
    expect((await fetchACMAcmeEndpoints()).map((candidate) => candidate.acmeEndpointArn)).not.toContain(endpointArn);
  });
});
