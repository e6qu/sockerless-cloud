import { defineConfig } from "@playwright/test";

const PORT = 19310;
const BIN = process.env.SERVER_BIN;
const HEALTH = `http://localhost:${PORT}/health`;
const chromiumExecutable = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  timeout: 30_000,
  retries: 0,
  use: {
    baseURL: `http://localhost:${PORT}`,
    headless: true,
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium", launchOptions: chromiumExecutable ? { executablePath: chromiumExecutable } : {} } },
  ],
  webServer: {
    command: `bash ../core/e2e/start-backend.sh`,
    env: {
      SERVER_PORT: String(PORT),
      HEALTH_URL: HEALTH,
      SERVER_PACKAGE: "simulator-aws",
      SERVER_NAME: "simulator-aws",
      SIM_MODE: "1",
      // Route 53's production default is the mDNS port, which macOS reserves.
      // This browser suite does not consume the DNS data plane, so give its
      // isolated simulator an OS-selected UDP/TCP coordinate.
      SIM_DNS_PORT: "0",
      ...(BIN ? { SERVER_BIN: BIN } : {}),
    },
    port: PORT,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
