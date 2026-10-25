import { defineConfig } from "@playwright/test";
import { chromiumLaunchOptions } from "../core/e2e/chromium";
import { CLOUD, CLOUD_PORT, FEDERATION, PORT, TENANT } from "./e2e/federation";

const BIN = process.env.SERVER_BIN;
const HEALTH = `http://localhost:${PORT}/health`;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  fullyParallel: true,
  timeout: 60_000,
  retries: 0,
  use: {
    baseURL: `http://localhost:${PORT}`,
    headless: true,
  },
  projects: [
    // The browser reaches the console, and the cloud it reads (Azure Resource
    // Manager, Microsoft Graph, Microsoft Entra ID and the data planes).
    { name: "chromium", use: { browserName: "chromium", launchOptions: chromiumLaunchOptions([PORT, CLOUD_PORT]) } },
  ],
  webServer: {
    command: `bash ../core/e2e/start-backend.sh`,
    // Playwright SIGKILLs the process group by default, which skips the script's
    // cleanup and the simulators' own shutdown, leaking their workloads.
    gracefulShutdown: { signal: "SIGTERM", timeout: 60_000 },
    env: {
      SERVER_PORT: String(PORT),
      HEALTH_URL: HEALTH,
      SERVER_PACKAGE: "simulator-azure",
      SERVER_NAME: "simulator-azure",
      SIM_MODE: "1",
      SIM_LOG_LEVEL: "warn",
      SIMULATOR_PACKAGE: "simulator-azure",
      SIMULATOR_NAME: "simulator-azure",
      SIMULATOR_PORT: String(CLOUD_PORT),
      SIMULATOR_SETUP: "azure-console-federation",
      FEDERATION_ISSUER: FEDERATION.issuer,
      FEDERATION_CLIENT_ID: FEDERATION.clientId,
      FEDERATION_SUBJECT: FEDERATION.subject,
      SIM_UI_OIDC_ISSUER: FEDERATION.issuer,
      SIM_UI_OIDC_CLIENT_ID: FEDERATION.clientId,
      SIM_UI_OIDC_CLIENT_SECRET: "azure-console-secret",
      SIM_UI_PUBLIC_URL: `http://localhost:${PORT}`,
      SIM_UI_SESSION_SECRET: "e2e-session-secret-0123456789abcdef",
      SIM_UI_INSECURE_COOKIES: "true",
      APPLICATION_RELEASE_REVISION: "0123456789abcdef0123456789abcdef01234567",
      SOCKERLESS_CONSOLE_CLOUD_API_ENDPOINT: CLOUD,
      SOCKERLESS_CONSOLE_GRAPH_API_ENDPOINT: CLOUD,
      SOCKERLESS_CONSOLE_LOGS_API_ENDPOINT: CLOUD,
      SOCKERLESS_CONSOLE_FEDERATION_ENDPOINT: CLOUD,
      SOCKERLESS_CONSOLE_FEDERATION_TENANT: TENANT,
      ...(BIN ? { SERVER_BIN: BIN } : {}),
    },
    port: PORT,
    reuseExistingServer: false,
    timeout: 300_000,
  },
});
