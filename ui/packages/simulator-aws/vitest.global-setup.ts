import { fileURLToPath } from "node:url";
import type { TestProject } from "vitest/node";
import { startConsoleDeployment } from "../core/e2e/simulators";

declare module "vitest" {
  export interface ProvidedContext {
    consoleOrigin: string;
  }
}

// Serves the console bundle this package's build produced from the real
// simulator, for every test file in the run.
export default async function setup(project: TestProject) {
  const deployment = await startConsoleDeployment("aws", fileURLToPath(new URL("./dist", import.meta.url)));
  project.provide("consoleOrigin", deployment.origin);
  return deployment.stop;
}
