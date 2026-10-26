import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Vitest's 5s default is sized for a unit test, not for a jsdom render of
    // a page built on a full component library: on a shared CI runner the first
    // test in the heaviest file absorbs tens of seconds of transform and import
    // time. This budget fits that shape of test; it is not a hang being waited
    // out.
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
