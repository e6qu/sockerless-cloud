import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CloudRunJobsPage } from "../pages/CloudRunJobsPage.js";
import { CloudFunctionsPage } from "../pages/CloudFunctionsPage.js";
import { ProjectProvider } from "../console/project.js";

/**
 * Cloud Run jobs and Cloud Run functions were deferred as read-only-plus-delete
 * — the real console creates both, and Cloud Run jobs are executed on demand.
 * The jobs list now offers a real "Create job" action and a per-row "Run"
 * action, and the functions list a real "Create function" action, each wired to
 * the real projects.locations.jobs.create / jobs.run / functions.create
 * long-running operation. These tests drive the round trips against a mocked
 * fetch that answers unauthenticated but otherwise succeeds, asserting the
 * exact request each sends. Dialog structure/accessibility live in the
 * Playwright suite; the authenticated create-appears loop in the relying-party
 * suite.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;
const seenBodies: Record<string, unknown> = {};

afterEach(() => {
  cleanup();
  mockFetch.mockReset();
  window.localStorage.clear();
  for (const key of Object.keys(seenBodies)) delete seenBodies[key];
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

type Rule = { when: (url: string, init?: RequestInit) => boolean; respond: (init?: RequestInit) => Response };

function installFetch(rules: Rule[]) {
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    const rule = rules.find((candidate) => candidate.when(url, init));
    if (!rule) throw new Error(`unhandled fetch: ${init?.method ?? "GET"} ${url}`);
    if (init?.method && init.method !== "GET" && typeof init.body === "string") {
      seenBodies[`${init.method} ${url}`] = JSON.parse(init.body);
    }
    return rule.respond(init);
  });
}

function renderWithProviders(ui: React.ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ProjectProvider>{ui}</ProjectProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const JOBS_LIST = "/v2/projects/sockerless/locations/us-central1/jobs";

describe("Cloud Run job create", () => {
  it("creates a job through jobs.create with the nested template and shows it once the list refreshes", async () => {
    let created = false;
    installFetch([
      {
        when: (url, init) => url === JOBS_LIST && (init?.method ?? "GET") === "GET",
        respond: () =>
          created
            ? jsonResponse({ jobs: [{ name: `${JOBS_LIST}/new-job`, terminalCondition: { state: "CONDITION_SUCCEEDED" } }] })
            : jsonResponse({ jobs: [] }),
      },
      {
        when: (url, init) => url === `${JOBS_LIST}?jobId=new-job` && init?.method === "POST",
        respond: () => {
          created = true;
          return jsonResponse({ name: "op", done: true, response: {} });
        },
      },
    ]);
    renderWithProviders(<CloudRunJobsPage />);
    await screen.findByText("Execute jobs on a fully managed platform");

    fireEvent.click(screen.getByTestId("cloudrun-create-job"));
    const dialog = await screen.findByTestId("cloudrun-create-dialog");
    expect((within(dialog).getByTestId("cloudrun-create-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("cloudrun-create-id"), { target: { value: "new-job" } });
    fireEvent.change(within(dialog).getByTestId("cloudrun-create-image"), { target: { value: "gcr.io/p/i" } });
    fireEvent.change(within(dialog).getByTestId("cloudrun-create-tasks"), { target: { value: "2" } });
    expect((within(dialog).getByTestId("cloudrun-create-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("cloudrun-create-submit"));

    await waitFor(() => expect(seenBodies[`POST ${JOBS_LIST}?jobId=new-job`]).toBeDefined());
    expect(seenBodies[`POST ${JOBS_LIST}?jobId=new-job`]).toEqual({
      name: "",
      template: { taskCount: 2, template: { timeout: "600s", containers: [{ image: "gcr.io/p/i" }] } },
    });
    await waitFor(() => expect(screen.queryByTestId("cloudrun-create-dialog")).toBeNull());
    expect(await screen.findByText("new-job")).toBeTruthy();
  });
});

describe("Cloud Run job run", () => {
  it("executes a job from the list row through the jobs.run verb", async () => {
    installFetch([
      {
        when: (url, init) => url === JOBS_LIST && (init?.method ?? "GET") === "GET",
        respond: () =>
          jsonResponse({ jobs: [{ name: `${JOBS_LIST}/runme`, terminalCondition: { state: "CONDITION_SUCCEEDED" } }] }),
      },
      {
        when: (url, init) => url === `${JOBS_LIST}/runme:run` && init?.method === "POST",
        respond: () => jsonResponse({ name: "op", done: true, response: {} }),
      },
    ]);
    renderWithProviders(<CloudRunJobsPage />);
    expect(await screen.findByText("runme")).toBeTruthy();

    fireEvent.click(screen.getByTestId("cloudrun-run-runme"));
    const dialog = await screen.findByTestId("cloudrun-run-dialog");
    fireEvent.click(within(dialog).getByTestId("cloudrun-run-confirm"));

    await waitFor(() => expect(seenBodies[`POST ${JOBS_LIST}/runme:run`]).toBeDefined());
    await waitFor(() => expect(screen.queryByTestId("cloudrun-run-dialog")).toBeNull());
  });
});

const FUNCTIONS_LIST = "/v2/projects/sockerless/locations/us-central1/functions";

describe("Cloud Function create", () => {
  it("creates a function through functions.create with the runtime and entry point", async () => {
    let created = false;
    installFetch([
      {
        when: (url, init) => url === FUNCTIONS_LIST && (init?.method ?? "GET") === "GET",
        respond: () =>
          created
            ? jsonResponse({ functions: [{ name: `${FUNCTIONS_LIST}/new-fn`, state: "ACTIVE" }] })
            : jsonResponse({ functions: [] }),
      },
      {
        when: (url, init) => url === `${FUNCTIONS_LIST}?functionId=new-fn` && init?.method === "POST",
        respond: () => {
          created = true;
          return jsonResponse({ name: "op", done: true, response: {} });
        },
      },
    ]);
    renderWithProviders(<CloudFunctionsPage />);
    await screen.findByText("Write and deploy your first function");

    fireEvent.click(screen.getByTestId("function-create"));
    const dialog = await screen.findByTestId("function-create-dialog");
    expect((within(dialog).getByTestId("function-create-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("function-create-id"), { target: { value: "new-fn" } });
    fireEvent.change(within(dialog).getByTestId("function-create-runtime"), { target: { value: "python312" } });
    fireEvent.change(within(dialog).getByTestId("function-create-entrypoint"), { target: { value: "handler" } });
    expect((within(dialog).getByTestId("function-create-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("function-create-submit"));

    await waitFor(() => expect(seenBodies[`POST ${FUNCTIONS_LIST}?functionId=new-fn`]).toBeDefined());
    expect(seenBodies[`POST ${FUNCTIONS_LIST}?functionId=new-fn`]).toEqual({
      buildConfig: { runtime: "python312", entryPoint: "handler" },
      environment: "GEN_2",
    });
    await waitFor(() => expect(screen.queryByTestId("function-create-dialog")).toBeNull());
    expect(await screen.findByText("new-fn")).toBeTruthy();
  });
});
