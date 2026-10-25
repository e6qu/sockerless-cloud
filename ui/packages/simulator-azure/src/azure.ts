import { API_VERSION, armResourceOf, type ArmEnvelope, type ArmGenericResource, type ResourceTags } from "./api.js";
import { armGet, armGetPage, armListAll, armWrite, type ArmPage, type OperationProgress } from "./portal/arm.js";
import { bladeForResource, RESOURCE_GROUP_TYPE } from "./services.js";

// The Azure Resource Manager and Microsoft Graph reads and writes the portal's
// shell, browse pages and resource pages make.

export const AUTHORIZATION_API_VERSION = "2022-04-01";
export const LOCATIONS_API_VERSION = "2022-12-01";
export const TENANTS_API_VERSION = "2022-12-01";

export interface Subscription {
  id: string;
  subscriptionId: string;
  displayName: string;
  state: string;
  tenantId: string;
}

export const listSubscriptions = async (): Promise<Subscription[]> =>
  (await armListAll<Partial<Subscription>>(`/subscriptions?api-version=${API_VERSION.subscriptions}`)).map((sub) => ({
    id: sub.id ?? "",
    subscriptionId: sub.subscriptionId ?? "",
    displayName: sub.displayName ?? "",
    state: sub.state ?? "",
    tenantId: sub.tenantId ?? "",
  }));

export interface Tenant {
  tenantId: string;
  displayName: string;
  defaultDomain: string;
}

export const listTenants = async (): Promise<Tenant[]> =>
  (await armListAll<Partial<Tenant>>(`/tenants?api-version=${TENANTS_API_VERSION}`)).map((tenant) => ({
    tenantId: tenant.tenantId ?? "",
    displayName: tenant.displayName ?? "",
    defaultDomain: tenant.defaultDomain ?? "",
  }));

export interface Location {
  name: string;
  displayName: string;
  regionalDisplayName: string;
}

export const listLocations = async (subscriptionId: string): Promise<Location[]> =>
  (await armListAll<Partial<Location>>(`/subscriptions/${subscriptionId}/locations?api-version=${LOCATIONS_API_VERSION}`)).map(
    (location) => ({
      name: location.name ?? "",
      displayName: location.displayName ?? location.name ?? "",
      regionalDisplayName: location.regionalDisplayName ?? location.displayName ?? location.name ?? "",
    }),
  );

function pageOf(page: ArmPage<ArmEnvelope>): ArmPage<ArmGenericResource> {
  return { value: page.value.map(armResourceOf), nextLink: page.nextLink };
}

export const listResourceGroupsPage = async (subscriptionId: string, top: number, next?: string) =>
  pageOf(await armGetPage<ArmEnvelope>(next ?? `/subscriptions/${subscriptionId}/resourceGroups?api-version=${API_VERSION.resourceGroups}&$top=${top}`));

export const listResourceGroups = async (subscriptionId: string): Promise<ArmGenericResource[]> =>
  (await armListAll<ArmEnvelope>(`/subscriptions/${subscriptionId}/resourceGroups?api-version=${API_VERSION.resourceGroups}`)).map(armResourceOf);

function filterFor(type?: string, extra?: string): string {
  const terms = [type ? `resourceType eq '${type}'` : "", extra ?? ""].filter(Boolean);
  return terms.length ? `&$filter=${encodeURIComponent(terms.join(" and "))}` : "";
}

/** One page of Resources_List, the subscription-wide list of every resource. */
export const listResourcesPage = async (
  subscriptionId: string,
  options: { top: number; type?: string; filter?: string; next?: string },
) =>
  pageOf(
    await armGetPage<ArmEnvelope>(
      options.next ??
        `/subscriptions/${subscriptionId}/resources?api-version=${API_VERSION.resourceGroups}&$top=${options.top}${filterFor(options.type, options.filter)}`,
    ),
  );

/** One page of the provider's own subscription-wide List operation. */
export const listProviderPage = async (subscriptionId: string, provider: string, apiVersion: string, next?: string) =>
  pageOf(await armGetPage<ArmEnvelope>(next ?? `/subscriptions/${subscriptionId}/providers/${provider}?api-version=${apiVersion}`));

/** Resources whose name contains the needle, for global search. */
export const searchResources = async (subscriptionId: string, needle: string): Promise<ArmGenericResource[]> =>
  (
    await listResourcesPage(subscriptionId, {
      top: 10,
      filter: `substringof('${needle.replace(/'/g, "''")}', name)`,
    })
  ).value;

const providerApiVersions = new Map<string, Promise<Map<string, string>>>();

async function providerTypeVersions(subscriptionId: string, namespace: string): Promise<Map<string, string>> {
  const key = `${subscriptionId}/${namespace.toLowerCase()}`;
  let versions = providerApiVersions.get(key);
  if (!versions) {
    versions = armGet<{ resourceTypes?: { resourceType?: string; apiVersions?: string[] }[] }>(
      `/subscriptions/${subscriptionId}/providers/${namespace}?api-version=${API_VERSION.resourceGroups}`,
    ).then(
      (provider) =>
        new Map(
          (provider.resourceTypes ?? [])
            .filter((entry) => entry.resourceType && entry.apiVersions?.length)
            .map((entry) => [entry.resourceType!.toLowerCase(), entry.apiVersions!.find((version) => !version.includes("preview")) ?? entry.apiVersions![0]]),
        ),
    );
    providerApiVersions.set(key, versions);
  }
  return versions;
}

export interface ParsedId {
  subscriptionId: string;
  resourceGroup?: string;
  /** `Namespace/type[/childType...]`, or the resource-group or subscription
   *  pseudo-type. */
  type: string;
  name: string;
}

export function parseResourceId(id: string): ParsedId {
  const segments = id.split("/").filter(Boolean);
  const subscriptionId = segments[1] ?? "";
  if (segments.length <= 2) return { subscriptionId, type: "Microsoft.Resources/subscriptions", name: subscriptionId };
  const resourceGroup = segments[3];
  if (segments.length <= 4) return { subscriptionId, resourceGroup, type: RESOURCE_GROUP_TYPE, name: resourceGroup ?? "" };
  const provider = segments.slice(5);
  const namespace = provider[0] ?? "";
  const types: string[] = [];
  for (let index = 1; index < provider.length; index += 2) types.push(provider[index]);
  return { subscriptionId, resourceGroup, type: [namespace, ...types].join("/"), name: provider[provider.length - 1] ?? "" };
}

/** The api-version a resource is read and written at: its blade's, or else
 *  the newest stable one its resource provider registers. */
export async function apiVersionFor(id: string, kind = ""): Promise<string> {
  const parsed = parseResourceId(id);
  if (parsed.type === RESOURCE_GROUP_TYPE) return API_VERSION.resourceGroups;
  const blade = bladeForResource(parsed.type, kind);
  if (blade) return blade.apiVersion;
  const [namespace, ...rest] = parsed.type.split("/");
  const version = (await providerTypeVersions(parsed.subscriptionId, namespace)).get(rest.join("/").toLowerCase());
  if (!version) throw new Error(`${namespace} registers no api-version for ${rest.join("/")}.`);
  return version;
}

export const getResource = async (id: string, apiVersion: string): Promise<ArmGenericResource> =>
  armResourceOf(await armGet<ArmEnvelope>(`${id}?api-version=${apiVersion}`));

export const getRawResource = async (id: string, apiVersion: string): Promise<unknown> => armGet<unknown>(`${id}?api-version=${apiVersion}`);

export async function deleteResource(id: string, kind = "", onProgress?: (progress: OperationProgress) => void): Promise<void> {
  await armWrite("DELETE", `${id}?api-version=${await apiVersionFor(id, kind)}`, undefined, onProgress);
}

export async function resourceAction(id: string, action: string, apiVersion: string): Promise<void> {
  await armWrite("POST", `${id}/${action}?api-version=${apiVersion}`);
}

export async function putResource<T = ArmEnvelope>(
  id: string,
  apiVersion: string,
  body: unknown,
  onProgress?: (progress: OperationProgress) => void,
): Promise<T | undefined> {
  return armWrite<T>("PUT", `${id}?api-version=${apiVersion}`, body, onProgress);
}

export const listChildren = async (parentId: string, path: string, apiVersion: string): Promise<ArmGenericResource[]> =>
  (await armListAll<ArmEnvelope>(`${parentId}/${path}?api-version=${apiVersion}`)).map(armResourceOf);

export interface RoleDefinition {
  id: string;
  name: string;
  roleName: string;
  description: string;
  roleType: string;
}

export const listRoleDefinitions = async (scope: string): Promise<RoleDefinition[]> =>
  (
    await armListAll<{ id?: string; name?: string; properties?: { roleName?: string; description?: string; type?: string } }>(
      `${scope}/providers/Microsoft.Authorization/roleDefinitions?api-version=${AUTHORIZATION_API_VERSION}`,
    )
  ).map((definition) => ({
    id: definition.id ?? "",
    name: definition.name ?? "",
    roleName: definition.properties?.roleName ?? "",
    description: definition.properties?.description ?? "",
    roleType: definition.properties?.type ?? "",
  }));

export interface RoleAssignment {
  id: string;
  name: string;
  scope: string;
  roleDefinitionId: string;
  principalId: string;
  principalType: string;
}

export const listRoleAssignments = async (scope: string): Promise<RoleAssignment[]> =>
  (
    await armListAll<{
      id?: string;
      name?: string;
      properties?: { scope?: string; roleDefinitionId?: string; principalId?: string; principalType?: string };
    }>(`${scope}/providers/Microsoft.Authorization/roleAssignments?api-version=${AUTHORIZATION_API_VERSION}`)
  ).map((assignment) => ({
    id: assignment.id ?? "",
    name: assignment.name ?? "",
    scope: assignment.properties?.scope ?? "",
    roleDefinitionId: assignment.properties?.roleDefinitionId ?? "",
    principalId: assignment.properties?.principalId ?? "",
    principalType: assignment.properties?.principalType ?? "",
  }));

export async function createRoleAssignment(scope: string, roleDefinitionId: string, principalId: string, principalType: string): Promise<void> {
  await armWrite("PUT", `${scope}/providers/Microsoft.Authorization/roleAssignments/${crypto.randomUUID()}?api-version=${AUTHORIZATION_API_VERSION}`, {
    properties: { roleDefinitionId, principalId, principalType },
  });
}

export async function deleteRoleAssignment(id: string): Promise<void> {
  await armWrite("DELETE", `${id}?api-version=${AUTHORIZATION_API_VERSION}`);
}

export interface DirectoryPrincipal {
  id: string;
  displayName: string;
  /** The user principal name, or the application ID of a service principal. */
  detail: string;
  principalType: "User" | "Group" | "ServicePrincipal";
}

interface GraphObject {
  id?: string;
  displayName?: string;
  userPrincipalName?: string;
  appId?: string;
  mail?: string;
  "@odata.type"?: string;
}

function principalOf(object: GraphObject, fallback: DirectoryPrincipal["principalType"]): DirectoryPrincipal {
  const odata = object["@odata.type"] ?? "";
  const principalType = odata.endsWith("user") ? "User" : odata.endsWith("group") ? "Group" : odata.endsWith("servicePrincipal") ? "ServicePrincipal" : fallback;
  return {
    id: object.id ?? "",
    displayName: object.displayName ?? object.id ?? "",
    detail: object.userPrincipalName ?? object.appId ?? object.mail ?? "",
    principalType,
  };
}

export async function listPrincipals(): Promise<DirectoryPrincipal[]> {
  const [users, groups, servicePrincipals] = await Promise.all([
    armGet<{ value?: GraphObject[] }>("/v1.0/users", "graph"),
    armGet<{ value?: GraphObject[] }>("/v1.0/groups", "graph"),
    armGet<{ value?: GraphObject[] }>("/v1.0/servicePrincipals", "graph"),
  ]);
  return [
    ...(users.value ?? []).map((object) => principalOf(object, "User")),
    ...(groups.value ?? []).map((object) => principalOf(object, "Group")),
    ...(servicePrincipals.value ?? []).map((object) => principalOf(object, "ServicePrincipal")),
  ];
}

export async function getPrincipal(id: string, principalType: string): Promise<DirectoryPrincipal> {
  const fallback = principalType === "User" || principalType === "Group" ? principalType : "ServicePrincipal";
  return principalOf(await armGet<GraphObject>(`/v1.0/directoryObjects/${id}`, "graph"), fallback);
}


export const TAGS_API_VERSION = "2021-04-01";

export async function getScopeTags(scope: string): Promise<Record<string, string>> {
  const body = await armGet<{ properties?: { tags?: Record<string, string> } }>(`${scope}/providers/Microsoft.Resources/tags/default?api-version=${TAGS_API_VERSION}`);
  return body.properties?.tags ?? {};
}

export async function putScopeTags(scope: string, tags: Record<string, string>): Promise<void> {
  await armWrite("PUT", `${scope}/providers/Microsoft.Resources/tags/default?api-version=${TAGS_API_VERSION}`, { properties: { tags } });
}

export const ACTIVITY_LOG_API_VERSION = "2015-04-01";

export interface ActivityLogEvent {
  eventDataId: string;
  operationName: string;
  operationId: string;
  status: string;
  subStatus: string;
  eventTimestamp: string;
  caller: string;
  level: string;
  resourceId: string;
  resourceGroupName: string;
  correlationId: string;
  category: string;
}

interface Localized {
  value?: string;
  localizedValue?: string;
}

interface ActivityLogWire {
  eventDataId?: string;
  operationName?: Localized;
  operationId?: string;
  status?: Localized;
  subStatus?: Localized;
  eventTimestamp?: string;
  caller?: string;
  level?: string;
  resourceId?: string;
  resourceGroupName?: string;
  correlationId?: string;
  category?: Localized;
}

const text = (value?: Localized) => value?.localizedValue || value?.value || "";

/** The Activity Log's administrative events for a scope over a timespan:
 *  a subscription, a resource group, or one resource. */
export async function listActivityLog(scope: string, sinceMs: number): Promise<ActivityLogEvent[]> {
  const parsed = parseResourceId(scope);
  const terms = [`eventTimestamp ge '${new Date(Date.now() - sinceMs).toISOString()}'`, `eventTimestamp le '${new Date().toISOString()}'`];
  if (parsed.type === RESOURCE_GROUP_TYPE) terms.push(`resourceGroupName eq '${parsed.resourceGroup}'`);
  else if (parsed.type !== "Microsoft.Resources/subscriptions") terms.push(`resourceUri eq '${scope}'`);
  const events = await armListAll<ActivityLogWire>(
    `/subscriptions/${parsed.subscriptionId}/providers/Microsoft.Insights/eventtypes/management/values?api-version=${ACTIVITY_LOG_API_VERSION}&$filter=${encodeURIComponent(terms.join(" and "))}`,
  );
  return events.map((event) => ({
    eventDataId: event.eventDataId ?? "",
    operationName: text(event.operationName),
    operationId: event.operationId ?? "",
    status: text(event.status),
    subStatus: text(event.subStatus),
    eventTimestamp: event.eventTimestamp ?? "",
    caller: event.caller ?? "",
    level: event.level ?? "",
    resourceId: event.resourceId ?? "",
    resourceGroupName: event.resourceGroupName ?? "",
    correlationId: event.correlationId ?? "",
    category: text(event.category),
  }));
}

export interface CorsRule {
  allowedOrigins: string[];
  allowedMethods: string[];
  allowedHeaders: string[];
  exposedHeaders: string[];
  maxAgeInSeconds: number;
}

/** The Blob service's CORS rules, through ARM's blobServices/default. */
export async function getBlobCorsRules(accountId: string): Promise<CorsRule[]> {
  const body = await armGet<{ properties?: { cors?: { corsRules?: Partial<CorsRule>[] } } }>(`${accountId}/blobServices/default?api-version=${API_VERSION.storage}`);
  return (body.properties?.cors?.corsRules ?? []).map((rule) => ({
    allowedOrigins: rule.allowedOrigins ?? [],
    allowedMethods: rule.allowedMethods ?? [],
    allowedHeaders: rule.allowedHeaders ?? [],
    exposedHeaders: rule.exposedHeaders ?? [],
    maxAgeInSeconds: rule.maxAgeInSeconds ?? 0,
  }));
}

export async function setBlobCorsRules(accountId: string, corsRules: CorsRule[]): Promise<void> {
  await armWrite("PUT", `${accountId}/blobServices/default?api-version=${API_VERSION.storage}`, { properties: { cors: { corsRules } } });
}
