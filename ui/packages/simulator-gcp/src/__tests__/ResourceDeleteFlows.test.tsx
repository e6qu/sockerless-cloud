import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { GCSBucketsPage } from "../pages/GCSBucketsPage.js";
import { GCSBucketDetailPage } from "../pages/GCSBucketDetailPage.js";
import { ArtifactRegistryPage } from "../pages/ArtifactRegistryPage.js";
import { ARRepoDetailPage } from "../pages/ARRepoDetailPage.js";
import { CloudFunctionsPage } from "../pages/CloudFunctionsPage.js";
import { CloudFunctionDetailPage } from "../pages/CloudFunctionDetailPage.js";
import { CloudRunJobsPage } from "../pages/CloudRunJobsPage.js";
import { CloudRunJobDetailPage } from "../pages/CloudRunJobDetailPage.js";
import { ProjectProvider } from "../console/project.js";

/**
 * Cloud Storage buckets, Artifact Registry repositories, Cloud Run functions
 * and Cloud Run jobs used to be undeletable from this console — the real
 * Google Cloud console lets an operator delete every one of these, matching
 * the AWS console (which deletes every resource) and the ServiceAccounts /
 * Manage resources pages, which already had a real delete-confirm flow. Each
 * resource now offers a real per-row "Delete" action on its list page and a
 * "Delete" header action on its detail page, both opening the same GcpDialog
 * confirm wired to the real storage.buckets.delete /
 * projects.locations.repositories.delete / projects.locations.functions.delete
 * / projects.locations.jobs.delete operation. These tests drive the full
 * read → delete → list-refreshes (or detail → navigate-back) round trip
 * against a mocked fetch that answers unauthenticated (no `identityEndpoint`
 * in `/ui/config.json`, so `authorizedFetch` sends no Authorization header)
 * but otherwise succeeds — the same shape a console wired to a real identity
 * provider drives once federation succeeds — the way
 * ResourceCreateFlows.test.tsx drives the create flows. The Playwright suite
 * (e2e/simulator-gcp.spec.ts) covers each delete confirm dialog's own
 * structure, Escape/focus handling and accessibility; the authenticated
 * delete round trip against a live identity provider is covered by the
 * Shauth relying-party suite (ui/e2e/shauth-rps.mjs).
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

afterEach(() => {
  cleanup();
  mockFetch.mockReset();
  window.localStorage.clear();
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

function emptyResponse(status = 204): Response {
  return new Response(null, { status });
}

type Rule = { when: (url: string, init?: RequestInit) => boolean; respond: (init?: RequestInit) => Response };

function installFetch(rules: Rule[]) {
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    const rule = rules.find((candidate) => candidate.when(url, init));
    if (!rule) throw new Error(`unhandled fetch: ${init?.method ?? "GET"} ${url}`);
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

function renderDetailWithRouting(
  detailPath: string,
  detailRoute: string,
  detailElement: React.ReactElement,
  listRoute: string,
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[detailPath]}>
        <ProjectProvider>
          <Routes>
            <Route path={detailRoute} element={detailElement} />
            <Route path={listRoute} element={<div data-testid="reached-list-page">back on the list</div>} />
          </Routes>
        </ProjectProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("GCSBucketsPage delete", () => {
  const bucketName = "old-bucket";

  it("deletes a bucket through the real storage.buckets.delete operation and removes it once the list refreshes", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!deleteCalled) return jsonResponse({ items: [{ name: bucketName, location: "US", storageClass: "STANDARD" }] });
          return jsonResponse({ items: [] });
        },
      },
      {
        when: (url, init) => url === `/storage/v1/b/${bucketName}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return emptyResponse(204);
        },
      },
    ]);
    renderWithProviders(<GCSBucketsPage />);
    expect(await screen.findByText(bucketName)).toBeTruthy();

    fireEvent.click(screen.getByTestId(`gcs-delete-${bucketName}`));
    const dialog = await screen.findByTestId("gcs-delete-dialog");
    expect(within(dialog).getByText(bucketName, { exact: false })).toBeTruthy();
    fireEvent.click(within(dialog).getByTestId("gcs-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    await waitFor(() => expect(screen.queryByTestId("gcs-delete-dialog")).toBeNull());
    await waitFor(() => expect(screen.queryByText(bucketName)).toBeNull());
  });

  it("surfaces the real error when the delete fails, without closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ items: [{ name: bucketName }] }),
      },
      {
        when: (url, init) => url === `/storage/v1/b/${bucketName}` && init?.method === "DELETE",
        respond: () =>
          jsonResponse({ error: { code: 404, message: `bucket "${bucketName}" not found`, status: "NOT_FOUND" } }, 404),
      },
    ]);
    renderWithProviders(<GCSBucketsPage />);
    fireEvent.click(await screen.findByTestId(`gcs-delete-${bucketName}`));
    const dialog = await screen.findByTestId("gcs-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("gcs-delete-confirm"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(`bucket "${bucketName}" not found`);
    expect(screen.getByTestId("gcs-delete-dialog")).toBeTruthy();
  });

  it("offers Delete from the bucket detail page and returns to the list once deleted", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) => url === `/storage/v1/b/${bucketName}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return emptyResponse(204);
        },
      },
    ]);
    renderDetailWithRouting(`/ui/gcs/${bucketName}`, "/ui/gcs/:name", <GCSBucketDetailPage />, "/ui/gcs");

    fireEvent.click(await screen.findByTestId("gcs-bucket-delete"));
    const dialog = await screen.findByTestId("gcs-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("gcs-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    expect(await screen.findByTestId("reached-list-page")).toBeTruthy();
  });
});

describe("ArtifactRegistryPage delete", () => {
  const repoId = "old-repo";
  const repoName = `projects/sockerless/locations/us-central1/repositories/${repoId}`;

  it("deletes a repository through the real long-running delete operation and removes it once the list refreshes", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!deleteCalled) return jsonResponse({ repositories: [{ name: repoName, format: "DOCKER" }] });
          return jsonResponse({ repositories: [] });
        },
      },
      {
        when: (url, init) =>
          url === `/v1/projects/sockerless/locations/us-central1/repositories/${repoId}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return jsonResponse({
            name: "projects/sockerless/locations/us-central1/operations/op-delete-1",
            done: true,
            response: { "@type": "type.googleapis.com/google.devtools.artifactregistry.v1.Repository" },
          });
        },
      },
    ]);
    renderWithProviders(<ArtifactRegistryPage />);
    expect(await screen.findByText(repoId)).toBeTruthy();

    fireEvent.click(screen.getByTestId(`ar-delete-${repoId}`));
    const dialog = await screen.findByTestId("ar-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("ar-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    await waitFor(() => expect(screen.queryByTestId("ar-delete-dialog")).toBeNull());
    await waitFor(() => expect(screen.queryByText(repoId)).toBeNull());
  });

  it("surfaces the real error when the repository no longer exists, without closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ repositories: [{ name: repoName, format: "DOCKER" }] }),
      },
      {
        when: (url, init) =>
          url === `/v1/projects/sockerless/locations/us-central1/repositories/${repoId}` && init?.method === "DELETE",
        respond: () =>
          jsonResponse({ error: { code: 404, message: `repository "${repoName}" not found`, status: "NOT_FOUND" } }, 404),
      },
    ]);
    renderWithProviders(<ArtifactRegistryPage />);
    fireEvent.click(await screen.findByTestId(`ar-delete-${repoId}`));
    const dialog = await screen.findByTestId("ar-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("ar-delete-confirm"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(`repository "${repoName}" not found`);
    expect(screen.getByTestId("ar-delete-dialog")).toBeTruthy();
  });

  it("offers Delete from the repository detail page and returns to the list once deleted", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) =>
          url === `/v1/projects/sockerless/locations/us-central1/repositories/${repoId}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return jsonResponse({
            name: "projects/sockerless/locations/us-central1/operations/op-delete-2",
            done: true,
            response: {},
          });
        },
      },
    ]);
    renderDetailWithRouting(`/ui/ar/${repoId}`, "/ui/ar/:name", <ARRepoDetailPage />, "/ui/ar");

    fireEvent.click(await screen.findByTestId("ar-repo-delete"));
    const dialog = await screen.findByTestId("ar-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("ar-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    expect(await screen.findByTestId("reached-list-page")).toBeTruthy();
  });
});

describe("CloudFunctionsPage delete", () => {
  const functionId = "old-function";
  const functionName = `projects/sockerless/locations/us-central1/functions/${functionId}`;

  it("deletes a function through the real long-running delete operation and removes it once the list refreshes", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) =>
          url === "/v2/projects/sockerless/locations/us-central1/functions" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!deleteCalled) return jsonResponse({ functions: [{ name: functionName, state: "ACTIVE" }] });
          return jsonResponse({ functions: [] });
        },
      },
      {
        when: (url, init) =>
          url === `/v2/projects/sockerless/locations/us-central1/functions/${functionId}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return jsonResponse({
            name: "projects/sockerless/locations/us-central1/operations/op-delete-3",
            done: true,
            response: { "@type": "type.googleapis.com/google.cloud.functions.v2.Function" },
          });
        },
      },
    ]);
    renderWithProviders(<CloudFunctionsPage />);
    expect(await screen.findByText(functionId)).toBeTruthy();

    fireEvent.click(screen.getByTestId(`function-delete-${functionId}`));
    const dialog = await screen.findByTestId("function-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("function-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    await waitFor(() => expect(screen.queryByTestId("function-delete-dialog")).toBeNull());
    await waitFor(() => expect(screen.queryByText(functionId)).toBeNull());
  });

  it("surfaces the real error when the function no longer exists, without closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v2/projects/sockerless/locations/us-central1/functions" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ functions: [{ name: functionName, state: "ACTIVE" }] }),
      },
      {
        when: (url, init) =>
          url === `/v2/projects/sockerless/locations/us-central1/functions/${functionId}` && init?.method === "DELETE",
        respond: () =>
          jsonResponse({ error: { code: 404, message: `function "${functionName}" not found`, status: "NOT_FOUND" } }, 404),
      },
    ]);
    renderWithProviders(<CloudFunctionsPage />);
    fireEvent.click(await screen.findByTestId(`function-delete-${functionId}`));
    const dialog = await screen.findByTestId("function-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("function-delete-confirm"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(`function "${functionName}" not found`);
    expect(screen.getByTestId("function-delete-dialog")).toBeTruthy();
  });

  it("offers Delete from the function detail page and returns to the list once deleted", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) =>
          url === `/v2/projects/sockerless/locations/us-central1/functions/${functionId}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return jsonResponse({
            name: "projects/sockerless/locations/us-central1/operations/op-delete-4",
            done: true,
            response: {},
          });
        },
      },
    ]);
    renderDetailWithRouting(`/ui/functions/${functionId}`, "/ui/functions/:name", <CloudFunctionDetailPage />, "/ui/functions");

    fireEvent.click(await screen.findByTestId("function-delete"));
    const dialog = await screen.findByTestId("function-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("function-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    expect(await screen.findByTestId("reached-list-page")).toBeTruthy();
  });
});

describe("CloudRunJobsPage delete", () => {
  const jobId = "old-job";
  const jobName = `projects/sockerless/locations/us-central1/jobs/${jobId}`;

  it("deletes a job through the real long-running delete operation and removes it once the list refreshes", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) =>
          url === "/v2/projects/sockerless/locations/us-central1/jobs" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!deleteCalled) return jsonResponse({ jobs: [{ name: jobName }] });
          return jsonResponse({ jobs: [] });
        },
      },
      {
        when: (url, init) =>
          url === `/v2/projects/sockerless/locations/us-central1/jobs/${jobId}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return jsonResponse({
            name: "projects/sockerless/locations/us-central1/operations/op-delete-5",
            done: true,
            response: { "@type": "type.googleapis.com/google.cloud.run.v2.Job" },
          });
        },
      },
    ]);
    renderWithProviders(<CloudRunJobsPage />);
    expect(await screen.findByText(jobId)).toBeTruthy();

    fireEvent.click(screen.getByTestId(`cloudrun-delete-${jobId}`));
    const dialog = await screen.findByTestId("cloudrun-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("cloudrun-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    await waitFor(() => expect(screen.queryByTestId("cloudrun-delete-dialog")).toBeNull());
    await waitFor(() => expect(screen.queryByText(jobId)).toBeNull());
  });

  it("surfaces the real error when the job no longer exists, without closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v2/projects/sockerless/locations/us-central1/jobs" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ jobs: [{ name: jobName }] }),
      },
      {
        when: (url, init) =>
          url === `/v2/projects/sockerless/locations/us-central1/jobs/${jobId}` && init?.method === "DELETE",
        respond: () => jsonResponse({ error: { code: 404, message: `job "${jobName}" not found`, status: "NOT_FOUND" } }, 404),
      },
    ]);
    renderWithProviders(<CloudRunJobsPage />);
    fireEvent.click(await screen.findByTestId(`cloudrun-delete-${jobId}`));
    const dialog = await screen.findByTestId("cloudrun-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("cloudrun-delete-confirm"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(`job "${jobName}" not found`);
    expect(screen.getByTestId("cloudrun-delete-dialog")).toBeTruthy();
  });

  it("offers Delete from the job detail page and returns to the list once deleted", async () => {
    let deleteCalled = false;
    installFetch([
      {
        when: (url, init) =>
          url === `/v2/projects/sockerless/locations/us-central1/jobs/${jobId}` && init?.method === "DELETE",
        respond: () => {
          deleteCalled = true;
          return jsonResponse({
            name: "projects/sockerless/locations/us-central1/operations/op-delete-6",
            done: true,
            response: {},
          });
        },
      },
    ]);
    renderDetailWithRouting(`/ui/cloudrun/${jobId}`, "/ui/cloudrun/:name", <CloudRunJobDetailPage />, "/ui/cloudrun");

    fireEvent.click(await screen.findByTestId("cloudrun-job-delete"));
    const dialog = await screen.findByTestId("cloudrun-delete-dialog");
    fireEvent.click(within(dialog).getByTestId("cloudrun-delete-confirm"));

    await waitFor(() => expect(deleteCalled).toBe(true));
    expect(await screen.findByTestId("reached-list-page")).toBeTruthy();
  });
});
