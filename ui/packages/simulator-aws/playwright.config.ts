import { defineConfig } from "@playwright/test";
import { chromiumLaunchOptions } from "../core/e2e/chromium";
import { FEDERATION, IDENTITY_PORT, PORT } from "./e2e/federation";

const BIN = process.env.SERVER_BIN;
const HEALTH = `http://localhost:${PORT}/health`;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  timeout: 30_000,
  retries: 0,
  use: {
    baseURL: `http://localhost:${PORT}`,
    headless: true,
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium", launchOptions: chromiumLaunchOptions([PORT, IDENTITY_PORT]) } },
  ],
  webServer: {
    command: `bash ../core/e2e/start-backend.sh`,
    env: {
      SERVER_PORT: String(PORT),
      HEALTH_URL: HEALTH,
      SERVER_PACKAGE: "simulator-aws",
      SERVER_NAME: "simulator-aws",
      SIM_MODE: "1",
      SIM_LOG_LEVEL: "warn",
      SIMULATOR_PACKAGE: "simulator-azure",
      SIMULATOR_NAME: "simulator-azure",
      SIMULATOR_PORT: String(IDENTITY_PORT),
      SIM_UI_OIDC_ISSUER: FEDERATION.issuer,
      SIM_UI_OIDC_CLIENT_ID: FEDERATION.clientId,
      SIM_UI_OIDC_CLIENT_SECRET: "aws-console-secret",
      SIM_UI_PUBLIC_URL: `http://localhost:${PORT}`,
      SIM_UI_SESSION_SECRET: "e2e-session-secret-0123456789abcdef",
      SIM_UI_INSECURE_COOKIES: "true",
      APPLICATION_RELEASE_REVISION: "0123456789abcdef0123456789abcdef01234567",
      SOCKERLESS_CONSOLE_FEDERATION_AUDIENCE: `arn:aws:iam::123456789012:role/${FEDERATION.roleName}`,
      // Route 53's production default is the mDNS port, which macOS reserves.
      // This browser suite does not consume the DNS data plane, so give its
      // isolated simulator an OS-selected UDP/TCP coordinate.
      SIM_DNS_PORT: "0",
      ...(BIN ? { SERVER_BIN: BIN } : {}),
    },
    port: PORT,
    reuseExistingServer: false,
    timeout: 300_000,
  },
});
