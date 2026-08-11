import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Vitest's 5s default is sized for a unit test, not for a jsdom render of
    // a page built on a full component library. On a shared CI runner these
    // files report tens of seconds of transform and import time, and the first
    // test in the heaviest file absorbs it — which is how a suite that takes
    // 17s locally times out one test at 5s there. The budget was never right
    // for this shape of test; it is not a hang being waited out.
    testTimeout: 20_000,
    environment: "jsdom",
    // jsdom's webstorage (localStorage / sessionStorage) is gated on an
    // origin — without `url` it defaults to about:blank and any access
    // throws. Pin to localhost so useTheme + any future storage-aware
    // hooks work in tests.
    environmentOptions: {
      jsdom: { url: "http://localhost/" },
    },
    setupFiles: ["./src/test-setup.ts"],
    exclude: ["e2e/**", "node_modules/**"],
  },
});
