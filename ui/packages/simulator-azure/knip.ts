import type { KnipConfig } from "knip";

const config: KnipConfig = {
  // Scope analysis to source. Restricting the project universe to src/** keeps
  // build output (dist/) out of scope entirely, so it is never reported as an
  // "unused file" — without needing a dist/** ignore that would dangle (and emit
  // an unused-ignore hint) in a clean checkout where dist/ has not been built.
  project: ["src/**/*.{ts,tsx}"],
  ignoreExportsUsedInFile: true,
};

export default config;
