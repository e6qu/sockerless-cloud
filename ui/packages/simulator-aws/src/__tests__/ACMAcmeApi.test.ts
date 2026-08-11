import { afterEach, describe, expect, it, vi } from "vitest";
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

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

afterEach(() => {
  mockFetch.mockReset();
});

function jsonResponse(data: unknown): Response {
  return new Response(JSON.stringify(data), {
    status: 200,
    headers: { "content-type": "application/x-amz-json-1.1" },
  });
}

function targetOf(init?: RequestInit): string {
  return new Headers(init?.headers).get("x-amz-target") ?? "";
}

function requestBody(init?: RequestInit): Record<string, unknown> {
  return JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
}

function installACMDataPlane(responses: Record<string, unknown>) {
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    const target = targetOf(init);
    if (!(target in responses)) throw new Error(`unhandled AWS Certificate Manager operation: ${target}`);
    return jsonResponse(responses[target]);
  });
}

describe("AWS Certificate Manager ACME browser data plane", () => {
  it("projects the real control-plane response shapes into console resources", async () => {
    installACMDataPlane({
      "CertificateManager.ListAcmeEndpoints": {
        AcmeEndpoints: [
          {
            AcmeEndpointArn: "arn:aws:acm:us-east-1:123456789012:acme-endpoint/endpoint",
            EndpointUrl: "https://acm.example/acme/endpoint/directory",
            Status: "ACTIVE",
            Contact: "REQUIRED",
            AuthorizationBehavior: "PRE_APPROVED",
            CreatedAt: 1_722_000_000,
          },
        ],
      },
      "CertificateManager.ListAcmeDomainValidations": {
        AcmeDomainValidations: [
          {
            AcmeDomainValidationArn: "arn:aws:acm:us-east-1:123456789012:acme-domain-validation/validation",
            DomainName: "example.com",
            Status: "VALID",
            CreatedAt: 1_722_000_001,
            PrevalidationDetails: {
              DnsPrevalidation: {
                ResourceRecord: {
                  Name: "_token.example.com.",
                  Value: "_value.acm-validations.aws.",
                },
              },
            },
          },
        ],
      },
      "CertificateManager.ListAcmeExternalAccountBindings": {
        ExternalAccountBindings: [
          {
            AcmeExternalAccountBindingArn:
              "arn:aws:acm:us-east-1:123456789012:acme-external-account-binding/binding",
            RoleArn: "arn:aws:iam::123456789012:role/acme",
            CreatedAt: 1_722_000_002,
            ExpiresAt: 1_722_604_802,
          },
        ],
      },
      "CertificateManager.ListAcmeAccounts": {
        AcmeAccounts: [
          {
            AccountUrl: "https://acm.example/acme/endpoint/account/account",
            Contacts: ["mailto:operator@example.com"],
            Status: "VALID",
            PublicKeyThumbprint: "thumbprint",
            CreatedAt: 1_722_000_003,
          },
        ],
      },
    });

    await expect(fetchACMAcmeEndpoints()).resolves.toEqual([
      {
        acmeEndpointArn: "arn:aws:acm:us-east-1:123456789012:acme-endpoint/endpoint",
        endpointUrl: "https://acm.example/acme/endpoint/directory",
        status: "ACTIVE",
        contact: "REQUIRED",
        authorizationBehavior: "PRE_APPROVED",
        createdAt: 1_722_000_000,
      },
    ]);
    await expect(
      fetchACMAcmeDomainValidations("arn:aws:acm:us-east-1:123456789012:acme-endpoint/endpoint"),
    ).resolves.toMatchObject([
      {
        domainName: "example.com",
        status: "VALID",
        recordName: "_token.example.com.",
        recordValue: "_value.acm-validations.aws.",
      },
    ]);
    await expect(
      fetchACMAcmeExternalAccountBindings("arn:aws:acm:us-east-1:123456789012:acme-endpoint/endpoint"),
    ).resolves.toMatchObject([
      {
        roleArn: "arn:aws:iam::123456789012:role/acme",
        expiresAt: 1_722_604_802,
      },
    ]);
    await expect(
      fetchACMAcmeAccounts("arn:aws:acm:us-east-1:123456789012:acme-endpoint/endpoint"),
    ).resolves.toMatchObject([
      {
        contacts: ["mailto:operator@example.com"],
        status: "VALID",
        publicKeyThumbprint: "thumbprint",
      },
    ]);
  });

  it("sends every operator mutation to the matching public AWS operation", async () => {
    const endpointArn = "arn:aws:acm:us-east-1:123456789012:acme-endpoint/endpoint";
    const validationArn =
      "arn:aws:acm:us-east-1:123456789012:acme-domain-validation/endpoint/validation";
    const bindingArn =
      "arn:aws:acm:us-east-1:123456789012:acme-external-account-binding/endpoint/binding";
    const accountUrl = "https://acm.example/acme/endpoint/account/account";
    const requests = new Map<string, Record<string, unknown>>();

    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      const target = targetOf(init);
      requests.set(target, requestBody(init));
      if (target === "CertificateManager.CreateAcmeExternalAccountBinding") {
        return jsonResponse({
          ExternalAccountBinding: {
            AcmeExternalAccountBindingArn: bindingArn,
            RoleArn: "arn:aws:iam::123456789012:role/acme",
            CreatedAt: 1,
            ExpiresAt: 2,
          },
        });
      }
      if (target === "CertificateManager.GetAcmeExternalAccountBindingCredentials") {
        return jsonResponse({ KeyId: "key-id", MacKey: "mac-key" });
      }
      return jsonResponse({});
    });

    await createACMAcmeEndpoint("REQUIRED");
    await createACMAcmeDomainValidation(endpointArn, "example.com", "Z123");
    await expect(
      createACMAcmeExternalAccountBinding(endpointArn, "arn:aws:iam::123456789012:role/acme"),
    ).resolves.toMatchObject({ acmeExternalAccountBindingArn: bindingArn, expiresAt: 2 });
    await expect(getACMAcmeExternalAccountBindingCredentials(bindingArn)).resolves.toEqual({
      keyId: "key-id",
      macKey: "mac-key",
    });
    await revokeACMAcmeExternalAccountBinding(bindingArn);
    await revokeACMAcmeAccount(endpointArn, accountUrl);
    await deleteACMAcmeDomainValidation(validationArn);
    await deleteACMAcmeEndpoint(endpointArn);

    expect(requests.get("CertificateManager.CreateAcmeEndpoint")).toMatchObject({
      AuthorizationBehavior: "PRE_APPROVED",
      Contact: "REQUIRED",
    });
    expect(requests.get("CertificateManager.CreateAcmeDomainValidation")).toMatchObject({
      AcmeEndpointArn: endpointArn,
      DomainName: "example.com",
      PrevalidationOptions: { DnsPrevalidation: { HostedZoneId: "Z123" } },
    });
    expect(requests.get("CertificateManager.CreateAcmeExternalAccountBinding")).toEqual({
      AcmeEndpointArn: endpointArn,
      RoleArn: "arn:aws:iam::123456789012:role/acme",
      Expiration: { Type: "DAYS", Value: 7 },
    });
    expect(requests.get("CertificateManager.GetAcmeExternalAccountBindingCredentials")).toEqual({
      AcmeExternalAccountBindingArn: bindingArn,
    });
    expect(requests.get("CertificateManager.RevokeAcmeAccount")).toEqual({
      AcmeEndpointArn: endpointArn,
      AccountUrl: accountUrl,
    });
    expect(requests.get("CertificateManager.DeleteAcmeDomainValidation")).toEqual({
      AcmeDomainValidationArn: validationArn,
    });
    expect(requests.get("CertificateManager.DeleteAcmeEndpoint")).toEqual({ AcmeEndpointArn: endpointArn });
  });
});
