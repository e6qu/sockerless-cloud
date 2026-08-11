import { defineConfig } from "@playwright/test";

const PORT = 19330;
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
      SERVER_PACKAGE: "simulator-azure",
      SERVER_NAME: "simulator-azure",
      SIM_MODE: "1",
      ...(BIN ? { SERVER_BIN: BIN } : {}),
    },
    port: PORT,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
