import { armFetch, armGet } from "./portal/arm.js";

// The api-versions the console reads and writes each provider at: the ones
// the vendored Azure REST specifications publish these operations under.
export const API_VERSION = {
  subscriptions: "2022-12-01",
  jobs: "2024-03-01",
  sites: "2023-12-01",
  registries: "2023-07-01",
  storage: "2023-01-01",
  workspaces: "2022-10-01",
  resourceGroups: "2021-04-01",
} as const;

export const BLADE_API_VERSION = {
  virtualMachines: "2022-03-01",
  network: "2025-03-01",
  dns: "2018-05-01",
  privateDns: "2024-06-01",
  keyVault: "2023-07-01",
  redis: "2024-11-01",
  postgresql: "2025-08-01",
  cosmos: "2024-08-15",
  serviceBus: "2021-11-01",
  serviceBusNamespaces: "2024-01-01",
  eventHub: "2024-01-01",
  eventGrid: "2022-06-15",
  apiManagement: "2022-08-01",
  logic: "2019-05-01",
  applicationInsights: "2020-02-02",
  containerInstance: "2021-10-01",
  managedIdentity: "2024-11-30",
} as const;

export type ResourceTags = Record<string, string>;

// Every ARM resource arrives in one envelope: id, name, type, location, kind,
// sku, tags, and the provider's own properties, which blades read by the
// property names the resource's REST specification documents.
export interface ArmSku {
  name?: string;
  tier?: string;
  family?: string;
  size?: string;
  capacity?: number;
}

export interface ArmGenericResource {
  id: string;
  name: string;
  type: string;
  location: string;
  kind: string;
  sku: ArmSku;
  tags: ResourceTags;
  properties: Record<string, unknown>;
}

export interface ArmEnvelope {
  id?: string;
  name?: string;
  type?: string;
  location?: string;
  kind?: string;
  sku?: ArmSku;
  tags?: ResourceTags;
  properties?: Record<string, unknown>;
}

export function armResourceOf(resource: ArmEnvelope): ArmGenericResource {
  return {
    id: resource.id ?? "",
    name: resource.name ?? "",
    type: resource.type ?? "",
    location: resource.location ?? "",
    kind: resource.kind ?? "",
    sku: resource.sku ?? {},
    tags: resource.tags ?? {},
    properties: resource.properties ?? {},
  };
}

// Microsoft Entra ID app registrations, through Microsoft Graph's
// applications and servicePrincipals.

export interface ClientSecretMetadata {
  keyId: string;
  displayName: string;
  hint: string;
  startDateTime: string;
  endDateTime: string;
}

/** The addPassword response: the only place Graph returns the secretText. */
export interface MintedClientSecret extends ClientSecretMetadata {
  secretText: string;
}

export interface AppRegistration {
  id: string;
  appId: string;
  displayName: string;
  signInAudience: string;
  passwordCredentials: ClientSecretMetadata[];
}

interface GraphApplication {
  id?: string;
  appId?: string;
  displayName?: string;
  signInAudience?: string;
  passwordCredentials?: Partial<ClientSecretMetadata>[];
}

function secretOf(credential: Partial<ClientSecretMetadata>): ClientSecretMetadata {
  return {
    keyId: credential.keyId ?? "",
    displayName: credential.displayName ?? "",
    hint: credential.hint ?? "",
    startDateTime: credential.startDateTime ?? "",
    endDateTime: credential.endDateTime ?? "",
  };
}

function appRegistrationOf(app: GraphApplication): AppRegistration {
  return {
    id: app.id ?? "",
    appId: app.appId ?? "",
    displayName: app.displayName ?? "",
    signInAudience: app.signInAudience ?? "",
    passwordCredentials: (app.passwordCredentials ?? []).map(secretOf),
  };
}

function graphPost(path: string, body: unknown): Promise<Response> {
  return armFetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, "graph");
}

export const fetchAppRegistrations = async (): Promise<AppRegistration[]> =>
  ((await armGet<{ value?: GraphApplication[] }>("/v1.0/applications", "graph")).value ?? []).map(appRegistrationOf);

export const fetchAppRegistration = async (objectId: string): Promise<AppRegistration> =>
  appRegistrationOf(await armGet<GraphApplication>(`/v1.0/applications/${objectId}`, "graph"));

/** New registration: the application object and the service principal that
 *  materializes it in the tenant, as the portal creates both. */
export const createAppRegistration = async (displayName: string): Promise<AppRegistration> => {
  const app = appRegistrationOf((await (await graphPost("/v1.0/applications", { displayName, signInAudience: "AzureADMyOrg" })).json()) as GraphApplication);
  await graphPost("/v1.0/servicePrincipals", { appId: app.appId });
  return app;
};

export const deleteAppRegistration = async (objectId: string): Promise<void> => {
  await armFetch(`/v1.0/applications/${objectId}`, { method: "DELETE" }, "graph");
};

export const addClientSecret = async (objectId: string, displayName: string, endDateTime: string): Promise<MintedClientSecret> => {
  const credential = (await (await graphPost(`/v1.0/applications/${objectId}/addPassword`, { passwordCredential: { displayName, endDateTime } })).json()) as Partial<MintedClientSecret>;
  return { ...secretOf(credential), secretText: credential.secretText ?? "" };
};

export const removeClientSecret = async (objectId: string, keyId: string): Promise<void> => {
  await graphPost(`/v1.0/applications/${objectId}/removePassword`, { keyId });
};
