import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { FEDERATION, PORT } from "./federation";

// Registers the suite's OpenID Connect issuer with IAM and creates the role the
// console assumes, through the real IAM API, once the simulator is serving.
export default function globalSetup() {
  execFileSync("bash", [fileURLToPath(new URL("../../core/e2e/setup-cloud.sh", import.meta.url))], {
    stdio: "inherit",
    env: {
      ...process.env,
      SIMULATOR_URL: `http://127.0.0.1:${PORT}`,
      SIMULATOR_SETUP: "aws-console-federation",
      FEDERATION_ISSUER: FEDERATION.issuer,
      FEDERATION_CLIENT_ID: FEDERATION.clientId,
      FEDERATION_ROLE_NAME: FEDERATION.roleName,
    },
  });
}
