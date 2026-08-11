import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { GCSBucketDetailPage } from "../pages/GCSBucketDetailPage.js";
import { ARRepoDetailPage } from "../pages/ARRepoDetailPage.js";
import { CloudFunctionDetailPage } from "../pages/CloudFunctionDetailPage.js";
import { CloudRunJobDetailPage } from "../pages/CloudRunJobDetailPage.js";
import { ProjectProvider } from "../console/project.js";

/**
 * Cloud Storage buckets, Artifact Registry repositories, Cloud Run functions
 * and Cloud Run jobs were read-only-plus-delete in this console — the real
 * Google Cloud console lets an operator edit each one's mutable fields. Every
 * detail page now offers a real "Edit" header action (rendered once the read
 * settles, since it prefills from the loaded resource) that opens a GcpDialog
 * wired to the real storage.buckets.patch / repositories.patch / functions.patch
 * / jobs.patch operation. These tests drive the read → edit → save round trip
 * against a mocked fetch that answers unauthenticated but otherwise succeeds,
 * asserting the exact request each flow sends. The dialog structure and
 * accessibility are covered by the Playwright suite; the authenticated
 * end-to-end loop by the Shauth relying-party suite.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

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
const seenBodies: Record<string, unknown> = {};

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

function renderDetail(detailPath: string, detailRoute: string, element: React.ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[detailPath]}>
        <ProjectProvider>
          <Routes>
            <Route path={detailRoute} element={element} />
          </Routes>
        </ProjectProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("GCS bucket edit", () => {
  const bucket = "edit-bucket";
  it("edits the default storage class and labels through storage.buckets.patch", async () => {
    installFetch([
      {
        when: (url, init) => url === `/storage/v1/b/${bucket}` && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ name: bucket, storageClass: "STANDARD", labels: { keep: "yes" } }),
      },
      {
        when: (url) => url === `/storage/v1/b/${bucket}/o`,
        respond: () => jsonResponse({ items: [] }),
      },
      {
        when: (url, init) => url === `/storage/v1/b/${bucket}` && init?.method === "PATCH",
        respond: () => jsonResponse({ name: bucket, storageClass: "NEARLINE", labels: { keep: "yes" } }),
      },
    ]);
    renderDetail(`/ui/gcs/${bucket}`, "/ui/gcs/:name", <GCSBucketDetailPage />);

    fireEvent.click(await screen.findByTestId("gcs-bucket-edit"));
    const dialog = await screen.findByTestId("gcs-edit-dialog");
    fireEvent.change(within(dialog).getByTestId("gcs-edit-storage-class"), { target: { value: "NEARLINE" } });
    // The loaded "keep" label occupies row 0; the added label lands at row 1.
    fireEvent.click(within(dialog).getByTestId("gcs-edit-label-add"));
    fireEvent.change(within(dialog).getByTestId("gcs-edit-label-key-1"), { target: { value: "team" } });
    fireEvent.change(within(dialog).getByTestId("gcs-edit-label-value-1"), { target: { value: "core" } });
    fireEvent.click(within(dialog).getByTestId("gcs-edit-submit"));

    await waitFor(() => expect(seenBodies[`PATCH /storage/v1/b/${bucket}`]).toBeDefined());
    expect(seenBodies[`PATCH /storage/v1/b/${bucket}`]).toEqual({
      storageClass: "NEARLINE",
      labels: { keep: "yes", team: "core" },
    });
    await waitFor(() => expect(screen.queryByTestId("gcs-edit-dialog")).toBeNull());
  });

  it("sends a null for a removed label so the deep-merge deletes it", async () => {
    installFetch([
      {
        when: (url, init) => url === `/storage/v1/b/${bucket}` && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ name: bucket, storageClass: "STANDARD", labels: { drop: "me" } }),
      },
      { when: (url) => url === `/storage/v1/b/${bucket}/o`, respond: () => jsonResponse({ items: [] }) },
      {
        when: (url, init) => url === `/storage/v1/b/${bucket}` && init?.method === "PATCH",
        respond: () => jsonResponse({ name: bucket, storageClass: "STANDARD" }),
      },
    ]);
    renderDetail(`/ui/gcs/${bucket}`, "/ui/gcs/:name", <GCSBucketDetailPage />);
    fireEvent.click(await screen.findByTestId("gcs-bucket-edit"));
    const dialog = await screen.findByTestId("gcs-edit-dialog");
    fireEvent.click(within(dialog).getByTestId("gcs-edit-label-remove-0"));
    fireEvent.click(within(dialog).getByTestId("gcs-edit-submit"));
    await waitFor(() => expect(seenBodies[`PATCH /storage/v1/b/${bucket}`]).toBeDefined());
    expect(seenBodies[`PATCH /storage/v1/b/${bucket}`]).toEqual({ storageClass: "STANDARD", labels: { drop: null } });
  });
});

describe("Artifact Registry repository edit", () => {
  const repo = "edit-repo";
  const name = `projects/sockerless/locations/us-central1/repositories/${repo}`;
  it("edits labels through the synchronous repositories.patch", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === `/v1/projects/sockerless/locations/us-central1/repositories/${repo}` && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ name, format: "DOCKER", labels: {} }),
      },
      {
        when: (url) => url === `/v1/projects/sockerless/locations/us-central1/repositories/${repo}/dockerImages`,
        respond: () => jsonResponse({ dockerImages: [] }),
      },
      {
        when: (url, init) =>
          url === `/v1/projects/sockerless/locations/us-central1/repositories/${repo}` && init?.method === "PATCH",
        respond: () => jsonResponse({ name, format: "DOCKER", labels: { env: "prod" } }),
      },
    ]);
    renderDetail(`/ui/ar/${repo}`, "/ui/ar/:name", <ARRepoDetailPage />);

    fireEvent.click(await screen.findByTestId("ar-repo-edit"));
    const dialog = await screen.findByTestId("ar-edit-dialog");
    fireEvent.click(within(dialog).getByTestId("ar-edit-label-add"));
    fireEvent.change(within(dialog).getByTestId("ar-edit-label-key-0"), { target: { value: "env" } });
    fireEvent.change(within(dialog).getByTestId("ar-edit-label-value-0"), { target: { value: "prod" } });
    fireEvent.click(within(dialog).getByTestId("ar-edit-submit"));

    const key = `PATCH /v1/projects/sockerless/locations/us-central1/repositories/${repo}`;
    await waitFor(() => expect(seenBodies[key]).toBeDefined());
    expect(seenBodies[key]).toEqual({ labels: { env: "prod" } });
    await waitFor(() => expect(screen.queryByTestId("ar-edit-dialog")).toBeNull());
  });
});

describe("Cloud Function edit", () => {
  const fn = "edit-fn";
  const name = `projects/sockerless/locations/us-central1/functions/${fn}`;
  it("edits the serviceConfig through functions.patch with the updateMask", async () => {
    installFetch([
      {
        when: (url, init) => url === `/v2/projects/sockerless/locations/us-central1/functions/${fn}` && (init?.method ?? "GET") === "GET",
        respond: () =>
          jsonResponse({
            name,
            state: "ACTIVE",
            serviceConfig: { availableMemory: "256Mi", timeoutSeconds: 60, minInstanceCount: 0, maxInstanceCount: 100 },
          }),
      },
      {
        when: (url, init) =>
          url.startsWith(`/v2/projects/sockerless/locations/us-central1/functions/${fn}?updateMask=`) && init?.method === "PATCH",
        respond: () => jsonResponse({ name: "op", done: true, response: {} }),
      },
    ]);
    renderDetail(`/ui/functions/${fn}`, "/ui/functions/:name", <CloudFunctionDetailPage />);

    fireEvent.click(await screen.findByTestId("function-edit"));
    const dialog = await screen.findByTestId("function-edit-dialog");
    fireEvent.change(within(dialog).getByTestId("function-edit-memory"), { target: { value: "512Mi" } });
    fireEvent.click(within(dialog).getByTestId("function-edit-submit"));

    await waitFor(() =>
      expect(
        Object.keys(seenBodies).find((k) => k.startsWith(`PATCH /v2/projects/sockerless/locations/us-central1/functions/${fn}?updateMask=`)),
      ).toBeDefined(),
    );
    const key = Object.keys(seenBodies).find((k) => k.includes(`functions/${fn}?updateMask=`))!;
    expect((seenBodies[key] as { serviceConfig: { availableMemory: string } }).serviceConfig.availableMemory).toBe("512Mi");
    await waitFor(() => expect(screen.queryByTestId("function-edit-dialog")).toBeNull());
  });
});

describe("Cloud Run job edit", () => {
  const job = "edit-job";
  const name = `projects/sockerless/locations/us-central1/jobs/${job}`;
  it("edits the container image through the full-replace jobs.patch", async () => {
    installFetch([
      {
        when: (url, init) => url === `/v2/projects/sockerless/locations/us-central1/jobs/${job}` && (init?.method ?? "GET") === "GET",
        respond: () =>
          jsonResponse({
            name,
            template: { taskCount: 1, template: { timeout: "600s", containers: [{ image: "img:1" }] } },
            terminalCondition: { type: "Ready", state: "CONDITION_SUCCEEDED" },
          }),
      },
      {
        when: (url) => url === `/v2/projects/sockerless/locations/us-central1/jobs/${job}/executions`,
        respond: () => jsonResponse({ executions: [] }),
      },
      {
        when: (url, init) => url === `/v2/projects/sockerless/locations/us-central1/jobs/${job}` && init?.method === "PATCH",
        respond: () => jsonResponse({ name: "op", done: true, response: {} }),
      },
    ]);
    renderDetail(`/ui/cloudrun/${job}`, "/ui/cloudrun/:name", <CloudRunJobDetailPage />);

    fireEvent.click(await screen.findByTestId("cloudrun-job-edit"));
    const dialog = await screen.findByTestId("cloudrun-edit-dialog");
    fireEvent.change(within(dialog).getByTestId("cloudrun-edit-image"), { target: { value: "img:2" } });
    fireEvent.change(within(dialog).getByTestId("cloudrun-edit-tasks"), { target: { value: "3" } });
    fireEvent.click(within(dialog).getByTestId("cloudrun-edit-submit"));

    const key = `PATCH /v2/projects/sockerless/locations/us-central1/jobs/${job}`;
    await waitFor(() => expect(seenBodies[key]).toBeDefined());
    const body = seenBodies[key] as { template: { taskCount: number; template: { timeout: string; containers: Array<{ image: string }> } } };
    expect(body.template.taskCount).toBe(3);
    expect(body.template.template.timeout).toBe("600s");
    expect(body.template.template.containers[0].image).toBe("img:2");
    await waitFor(() => expect(screen.queryByTestId("cloudrun-edit-dialog")).toBeNull());
  });
});
