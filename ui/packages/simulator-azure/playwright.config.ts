import { defineConfig } from "@playwright/test";
import { chromiumLaunchOptions } from "../core/e2e/chromium";

const PORT = 19330;
const BIN = process.env.SERVER_BIN;
const HEALTH = `http://localhost:${PORT}/health`;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  fullyParallel: true,
  timeout: 30_000,
  retries: 0,
  use: {
    baseURL: `http://localhost:${PORT}`,
    headless: true,
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium", launchOptions: chromiumLaunchOptions([PORT]) } },
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
      ...(BIN ? { SERVER_BIN: BIN } : {}),
    },
    port: PORT,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
