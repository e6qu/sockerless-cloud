import { chromium, type LaunchOptions } from "@playwright/test";
import { fileURLToPath } from "node:url";

// Launch options for a browser that reaches the suite's listeners on the
// host's loopback `hostPorts`. On Linux Chromium runs in a network namespace of
// its own (see isolated-chromium.sh), because there the container engine's
// interfaces live on the browser's host and Chromium aborts requests on every
// address change they make. On macOS the engine runs in a virtual machine whose
// interfaces the browser never sees.
export function chromiumLaunchOptions(hostPorts: readonly number[]): LaunchOptions {
  const executable = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
  if (process.platform !== "linux") {
    return executable ? { executablePath: executable } : {};
  }
  const env: Record<string, string> = {};
  for (const [name, value] of Object.entries(process.env)) {
    if (value !== undefined) env[name] = value;
  }
  env.SOCKERLESS_E2E_CHROMIUM = executable ?? chromium.executablePath();
  env.SOCKERLESS_E2E_HOST_PORTS = hostPorts.join(",");
  return {
    executablePath: fileURLToPath(new URL("./isolated-chromium.sh", import.meta.url)),
    env,
  };
}
