import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { signInToConsole } from "@sockerless/ui-core/testing";
import { CloudRunJobsPage } from "../pages/CloudRunJobsPage.js";
import { CloudFunctionsPage } from "../pages/CloudFunctionsPage.js";
import { ProjectProvider } from "../console/project.js";
import { fetchCloudFunction, fetchCloudRunJob, fetchCloudRunJobExecutions } from "../api.js";
import { PROJECT, createJob, resourceId } from "./fixtures.js";

// The Cloud Run jobs list's "Create job" and per-row "Run" actions and the
// Cloud Run functions list's "Create function" action, each driven against the
// real projects.locations.jobs.create / jobs.run / functions.create
// long-running operation and read back from the simulator. Dialog structure and
// accessibility live in the Playwright suite.

beforeAll(signInToConsole);

afterEach(() => {
  cleanup();
  window.localStorage.clear();
});

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

describe("Cloud Run job create", () => {
  it("creates a job through jobs.create with the nested template and shows it once the list refreshes", async () => {
    const jobId = resourceId("new-job");
    renderWithProviders(<CloudRunJobsPage />);

    fireEvent.click(await screen.findByTestId("cloudrun-create-job"));
    const dialog = await screen.findByTestId("cloudrun-create-dialog");
    expect((within(dialog).getByTestId("cloudrun-create-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("cloudrun-create-id"), { target: { value: jobId } });
    fireEvent.change(within(dialog).getByTestId("cloudrun-create-image"), {
      target: { value: "us-docker.pkg.dev/cloudrun/container/job" },
    });
    fireEvent.change(within(dialog).getByTestId("cloudrun-create-tasks"), { target: { value: "2" } });
    expect((within(dialog).getByTestId("cloudrun-create-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("cloudrun-create-submit"));

    await waitFor(() => expect(screen.queryByTestId("cloudrun-create-dialog")).toBeNull());
    expect(await screen.findByText(jobId)).toBeTruthy();
    expect((await fetchCloudRunJob(PROJECT, jobId)).template).toMatchObject({
      taskCount: 2,
      template: { timeout: "600s", containers: [{ image: "us-docker.pkg.dev/cloudrun/container/job" }] },
    });
  });
});

describe("Cloud Run job run", () => {
  // The job's image does not exist, so its execution fails whichever engine
  // runs it; the dialog reports the failure the jobs.run operation settles with.
  it("executes a job from the list row through the jobs.run verb and reports how the execution ended", async () => {
    const jobId = await createJob(`us-docker.pkg.dev/${PROJECT}/missing/${resourceId("image")}`);
    renderWithProviders(<CloudRunJobsPage />);
    expect(await screen.findByText(jobId)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: `Actions for ${jobId}` }));
    fireEvent.click(screen.getByTestId(`cloudrun-run-${jobId}`));
    const dialog = await screen.findByTestId("cloudrun-run-dialog");
    fireEvent.click(within(dialog).getByTestId("cloudrun-run-confirm"));

    expect(await within(dialog).findByText("Couldn't execute the job.")).toBeTruthy();
    expect(dialog.textContent).toMatch(/Execution \S+ has failed to complete, 0\/1 tasks were a success\./);
    const executions = await fetchCloudRunJobExecutions(PROJECT, jobId);
    expect(executions).toHaveLength(1);
    expect(executions[0].failedCount).toBe(1);
  });
});

describe("Cloud Function create", () => {
  it("creates a function through functions.create with the runtime and entry point", async () => {
    const functionId = resourceId("new-fn");
    renderWithProviders(<CloudFunctionsPage />);

    fireEvent.click(await screen.findByTestId("function-create"));
    const dialog = await screen.findByTestId("function-create-dialog");
    expect((within(dialog).getByTestId("function-create-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("function-create-id"), { target: { value: functionId } });
    fireEvent.change(within(dialog).getByTestId("function-create-runtime"), { target: { value: "python312" } });
    fireEvent.change(within(dialog).getByTestId("function-create-entrypoint"), { target: { value: "handler" } });
    expect((within(dialog).getByTestId("function-create-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("function-create-submit"));

    await waitFor(() => expect(screen.queryByTestId("function-create-dialog")).toBeNull());
    expect(await screen.findByText(functionId)).toBeTruthy();
    expect(await fetchCloudFunction(PROJECT, functionId)).toMatchObject({
      environment: "GEN_2",
      buildConfig: { runtime: "python312", entryPoint: "handler" },
    });
  });
});
