import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { FEDERATION, PORT } from "./federation";

// Creates the workforce pool and provider the console federates its operator
// through, and grants the pool the project, through the real IAM and Resource
// Manager APIs once the simulator is serving.
export default function globalSetup() {
  execFileSync("bash", [fileURLToPath(new URL("../../core/e2e/setup-cloud.sh", import.meta.url))], {
    stdio: "inherit",
    env: {
      ...process.env,
      SIMULATOR_URL: `http://127.0.0.1:${PORT}`,
      SIMULATOR_SETUP: "gcp-console-federation",
      FEDERATION_ISSUER: FEDERATION.issuer,
      FEDERATION_CLIENT_ID: FEDERATION.clientId,
      FEDERATION_POOL: FEDERATION.pool,
      FEDERATION_PROVIDER: FEDERATION.provider,
      FEDERATION_PROJECT: FEDERATION.project,
    },
  });
}
