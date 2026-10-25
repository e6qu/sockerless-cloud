// The coordinates of the suite's deployment. The console is served on its own
// origin, as the Azure portal is, and reads Azure Resource Manager, Microsoft
// Graph and Log Analytics at the cloud's coordinates: a second simulator-azure
// process, which is also the Microsoft Entra ID tenant the console signs its
// operator in with and federates that assertion through.
export const PORT = Number(process.env.AZURE_E2E_PORT ?? 19330);
export const CLOUD_PORT = Number(process.env.AZURE_E2E_CLOUD_PORT ?? 19331);
export const TENANT = "11111111-1111-1111-1111-111111111111";
export const CLOUD = `http://localhost:${CLOUD_PORT}`;

export const FEDERATION = {
  issuer: `${CLOUD}/${TENANT}/v2.0`,
  clientId: "azure-console",
  // The directory's default user, whom the tenant's authorize endpoint signs in.
  subject: "test-sub",
  operator: "Sockerless Test User",
  identity: "console-operator",
  subscription: "00000000-0000-0000-0000-000000000001",
};
