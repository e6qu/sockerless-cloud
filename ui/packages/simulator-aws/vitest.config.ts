import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Vitest's 5s default is sized for a unit test, not for a jsdom render of
    // a page built on a full component library that makes several round trips
    // to the real simulator through the console's federated credentials. On a
    // shared CI runner the first test in the heaviest file also absorbs tens of
    // seconds of transform and import time.
    testTimeout: 60_000,
    environment: "jsdom",
    environmentOptions: { jsdom: { url: "http://localhost/" } },
    exclude: ["e2e/**", "node_modules/**"],
    globalSetup: ["./vitest.global-setup.ts"],
    setupFiles: ["./vitest.console-setup.ts"],
  },
});
