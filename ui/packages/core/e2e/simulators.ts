import { execFile, spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

// Builds and runs the real simulator binaries for a console's unit tests, the
// way the Go suites' TestMain does: a fresh build of the working tree, a free
// port, a private data directory, and a wait on the banner the simulator prints
// once its listeners are bound.

const run = promisify(execFile);
const repoRoot = fileURLToPath(new URL("../../../../", import.meta.url));
const setupCloud = fileURLToPath(new URL("./setup-cloud.sh", import.meta.url));

// The Microsoft Entra ID tenant the Azure simulator seeds, whose authorize
// endpoint signs in the directory's default user.
const TENANT = "11111111-1111-1111-1111-111111111111";

// Bounds the wait for the banner; registration creates every persistent store
// table first, which a loaded disk has taken tens of seconds to do.
const STARTUP_BOUND_MS = 120_000;
// Bounds the orderly shutdown SIGTERM starts before the process is killed.
const SHUTDOWN_BOUND_MS = 15_000;

type Cloud = "aws" | "gcp" | "azure";

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, () => {
      const address = server.address();
      server.close(() => {
        if (address && typeof address === "object") resolve(address.port);
        else reject(new Error("the loopback listener reported no port"));
      });
    });
  });
}

async function buildSimulator(cloud: Cloud, outDir: string): Promise<string> {
  const binary = join(outDir, `simulator-${cloud}`);
  await run("go", ["build", "-o", binary, "."], {
    cwd: join(repoRoot, `simulator-${cloud}`),
    env: { ...process.env, CGO_ENABLED: "0" },
    maxBuffer: 16 * 1024 * 1024,
  });
  return binary;
}

// The environment a simulator starts with: the caller's, minus every
// coordinate that configures a simulator or its console, so a variable an
// operator's shell carries never reaches the process under test.
function baseEnvironment(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [name, value] of Object.entries(process.env)) {
    if (name.startsWith("SIM_") || name.startsWith("SOCKERLESS_CONSOLE_")) continue;
    env[name] = value;
  }
  return env;
}

interface Process {
  child: ChildProcess;
  exited: Promise<void>;
}

async function startSimulator(binary: string, env: Record<string, string>): Promise<Process> {
  const child = spawn(binary, [], {
    env: {
      ...baseEnvironment(),
      SIM_RUNTIME: "process",
      SIM_LOG_LEVEL: "warn",
      // The simulator exits when this process does, so it never outlives the run.
      SOCKERLESS_PARENT_PID: String(process.pid),
      ...env,
    },
    stdio: ["ignore", "inherit", "pipe"],
  });
  const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
  const lines = createInterface({ input: child.stderr! });
  await new Promise<void>((resolve, reject) => {
    let announced = false;
    const timer = setTimeout(() => {
      reject(new Error(`${binary} did not start listening within ${STARTUP_BOUND_MS / 1000}s`));
    }, STARTUP_BOUND_MS);
    lines.on("line", (line) => {
      process.stderr.write(`${line}\n`);
      if (!announced && line.trim().startsWith("Listening on ")) {
        announced = true;
        clearTimeout(timer);
        resolve();
      }
    });
    lines.once("close", () => {
      clearTimeout(timer);
      if (!announced) reject(new Error(`${binary} exited before it was listening`));
    });
  });
  return { child, exited };
}

async function stopSimulator({ child, exited }: Process): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), SHUTDOWN_BOUND_MS);
  await exited;
  clearTimeout(timer);
}

async function provision(setup: string, simulatorUrl: string, env: Record<string, string>): Promise<void> {
  await run("bash", [setupCloud], {
    env: { ...process.env, SIMULATOR_URL: simulatorUrl, SIMULATOR_SETUP: setup, ...env },
  });
}

export interface ConsoleDeployment {
  /** The origin the console and its cloud API are served on. */
  origin: string;
  stop(): Promise<void>;
}

// startConsoleDeployment builds and starts the cloud's simulator serving the
// console bundle in `consoleDir`, with operator sign-in federated through a
// second, Azure, simulator acting as the Microsoft Entra ID issuer, and
// provisions the federation the console exchanges that sign-in through: the
// same deployment the Playwright suites run, on free ports and private data
// directories.
export async function startConsoleDeployment(cloud: "aws" | "gcp", consoleDir: string): Promise<ConsoleDeployment> {
  const workDir = await mkdtemp(join(tmpdir(), `sockerless-console-${cloud}-`));
  const running: Process[] = [];
  const stop = async () => {
    await Promise.all(running.map(stopSimulator));
    await rm(workDir, { recursive: true, force: true });
  };
  try {
    const identity = await buildSimulator("azure", workDir);
    const simulator = await buildSimulator(cloud, workDir);
    const identityPort = await freePort();
    const consolePort = await freePort();
    const issuer = `http://127.0.0.1:${identityPort}/${TENANT}/v2.0`;
    const clientId = `${cloud}-console`;
    const origin = `http://localhost:${consolePort}`;

    running.push(
      await startSimulator(identity, {
        SIM_LISTEN_ADDR: `:${identityPort}`,
        SIM_DATA_DIR: join(workDir, "identity-data"),
      }),
    );

    const federation =
      cloud === "aws"
        ? {
            audience: "arn:aws:iam::123456789012:role/ConsoleOperator",
            setup: "aws-console-federation",
            setupEnv: { FEDERATION_ROLE_NAME: "ConsoleOperator" },
            simulatorEnv: { SIM_DNS_PORT: "0" },
          }
        : {
            audience:
              "//iam.googleapis.com/locations/global/workforcePools/console-operators/providers/console-sign-in",
            setup: "gcp-console-federation",
            setupEnv: {
              FEDERATION_POOL: "console-operators",
              FEDERATION_PROVIDER: "console-sign-in",
              FEDERATION_PROJECT: "sockerless",
            },
            simulatorEnv: { SIM_GCP_GRPC_PORT: String(await freePort()) },
          };

    running.push(
      await startSimulator(simulator, {
        SIM_LISTEN_ADDR: `:${consolePort}`,
        SIM_DATA_DIR: join(workDir, "simulator-data"),
        SIM_CONSOLE_DIR: consoleDir,
        SIM_UI_OIDC_ISSUER: issuer,
        SIM_UI_OIDC_CLIENT_ID: clientId,
        SIM_UI_OIDC_CLIENT_SECRET: `${cloud}-console-secret`,
        SIM_UI_PUBLIC_URL: origin,
        SIM_UI_SESSION_SECRET: "console-tests-session-secret-0123456789",
        SIM_UI_INSECURE_COOKIES: "true",
        APPLICATION_RELEASE_REVISION: "0123456789abcdef0123456789abcdef01234567",
        SOCKERLESS_CONSOLE_FEDERATION_AUDIENCE: federation.audience,
        ...federation.simulatorEnv,
      }),
    );

    await provision(federation.setup, `http://127.0.0.1:${consolePort}`, {
      FEDERATION_ISSUER: issuer,
      FEDERATION_CLIENT_ID: clientId,
      ...federation.setupEnv,
    });
    return { origin, stop };
  } catch (error) {
    await stop();
    throw error;
  }
}
