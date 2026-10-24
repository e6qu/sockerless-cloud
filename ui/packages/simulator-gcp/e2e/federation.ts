// The coordinates of the suite's operator sign-in: the console signs its
// operator in with an OpenID Connect issuer and federates that assertion into
// a Google Cloud access token through Workforce Identity Federation, exactly
// as a deployment does. The issuer is Microsoft Entra ID as the Azure
// simulator serves it.
export const PORT = 19320;
export const IDENTITY_PORT = 19321;
const TENANT = "11111111-1111-1111-1111-111111111111";

export const FEDERATION = {
  issuer: `http://127.0.0.1:${IDENTITY_PORT}/${TENANT}/v2.0`,
  clientId: "gcp-console",
  pool: "console-operators",
  provider: "console-sign-in",
  project: "sockerless",
};

export const FEDERATION_AUDIENCE = `//iam.googleapis.com/locations/global/workforcePools/${FEDERATION.pool}/providers/${FEDERATION.provider}`;
