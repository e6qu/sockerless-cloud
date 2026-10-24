// The coordinates of the suite's operator sign-in: the console signs its
// operator in with an OpenID Connect issuer and federates that assertion into
// AWS credentials with AssumeRoleWithWebIdentity, exactly as a deployment does.
// The issuer is Microsoft Entra ID as the Azure simulator serves it.
export const PORT = 19310;
export const IDENTITY_PORT = 19311;
const TENANT = "11111111-1111-1111-1111-111111111111";

export const FEDERATION = {
  issuer: `http://127.0.0.1:${IDENTITY_PORT}/${TENANT}/v2.0`,
  clientId: "aws-console",
  roleName: "ConsoleOperator",
};
