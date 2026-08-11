import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { GCSBucketsPage } from "../pages/GCSBucketsPage.js";
import { ArtifactRegistryPage } from "../pages/ArtifactRegistryPage.js";
import { ProjectProvider } from "../console/project.js";

/**
 * Cloud Storage buckets and Artifact Registry repositories used to be
 * read-only in this console — list and detail, with a disabled "Create …"
 * button — even though the real console lets an operator create a bucket or
 * repository from the same page. Each now offers a real "Create …" header
 * action and empty-state primary that open a GcpDialog wired to the real
 * storage.buckets.insert / artifactregistry projects.locations.repositories.create
 * operation. These tests drive the full read → create → list-refreshes round
 * trip against a mocked fetch that answers unauthenticated (no
 * `identityEndpoint` in `/ui/config.json`, so `authorizedFetch` sends no
 * Authorization header) but otherwise succeeds — the same shape a console
 * wired to a real identity provider drives once federation succeeds — the
 * way the AWS console's ResourceHeaderActions.test.tsx drives its Amazon S3
 * / Amazon ECR create flows. The Playwright suite (e2e/simulator-gcp.spec.ts)
 * covers the dialog's own structure, validation and accessibility against
 * the real (auth-enforcing) simulator; the authenticated create→appears loop
 * against a live identity provider is covered by the Shauth relying-party
 * suite (ui/e2e/shauth-rps.mjs).
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

describe("GCSBucketsPage", () => {
  const bucketName = "my-new-bucket";

  it("creates a bucket through the real storage.buckets.insert operation and shows it once the list refreshes", async () => {
    let sentBody: string | null = null;
    let listedAfterCreate = false;
    installFetch([
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!sentBody) return jsonResponse({ kind: "storage#buckets", items: [] });
          listedAfterCreate = true;
          return jsonResponse({
            kind: "storage#buckets",
            items: [{ name: bucketName, location: "US", storageClass: "STANDARD", timeCreated: "2026-07-25T00:00:00.000Z" }],
          });
        },
      },
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && init?.method === "POST",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          return jsonResponse({ name: bucketName, location: "US", storageClass: "STANDARD" });
        },
      },
    ]);
    renderWithProviders(<GCSBucketsPage />);
    await screen.findByText("Store any amount of data");

    fireEvent.click(screen.getByTestId("gcs-create-bucket"));
    const dialog = await screen.findByTestId("gcs-create-dialog");
    expect((within(dialog).getByTestId("gcs-create-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("gcs-create-name"), { target: { value: bucketName } });
    expect((within(dialog).getByTestId("gcs-create-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("gcs-create-submit"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(JSON.parse(sentBody!)).toEqual({ name: bucketName });
    await waitFor(() => expect(screen.queryByTestId("gcs-create-dialog")).toBeNull());
    await waitFor(() => expect(listedAfterCreate).toBe(true));
    expect(await screen.findByText(bucketName)).toBeTruthy();
  });

  it("surfaces the real 409 conflict when the bucket name is already taken, without closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ kind: "storage#buckets", items: [] }),
      },
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && init?.method === "POST",
        respond: () =>
          jsonResponse(
            { error: { code: 409, message: `bucket "${bucketName}" already exists`, status: "ALREADY_EXISTS", details: [] } },
            409,
          ),
      },
    ]);
    renderWithProviders(<GCSBucketsPage />);
    await screen.findByText("Store any amount of data");

    fireEvent.click(screen.getByTestId("gcs-create-bucket"));
    const dialog = await screen.findByTestId("gcs-create-dialog");
    fireEvent.change(within(dialog).getByTestId("gcs-create-name"), { target: { value: bucketName } });
    fireEvent.click(within(dialog).getByTestId("gcs-create-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(`bucket "${bucketName}" already exists`);
    expect(screen.getByTestId("gcs-create-dialog")).toBeTruthy();
  });

  it("also opens the dialog from the empty state's primary action, once the empty read succeeds", async () => {
    installFetch([
      {
        when: (url, init) => url === "/storage/v1/b?project=sockerless" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ kind: "storage#buckets", items: [] }),
      },
    ]);
    renderWithProviders(<GCSBucketsPage />);
    const empty = await screen.findByText("Store any amount of data");
    const primary = within(empty.closest(".gc-empty")!).getByRole("button", { name: "Create bucket" }) as HTMLButtonElement;
    expect(primary.disabled).toBe(false);
    fireEvent.click(primary);
    expect(await screen.findByTestId("gcs-create-dialog")).toBeTruthy();
  });

  it("blocks creation until the bucket name satisfies Cloud Storage's naming contract", () => {
    renderWithProviders(<GCSBucketsPage />);
    fireEvent.click(screen.getByTestId("gcs-create-bucket"));
    const submit = screen.getByTestId("gcs-create-submit") as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByTestId("gcs-create-name"), { target: { value: "ab" } }); // too short
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByTestId("gcs-create-name"), { target: { value: "Invalid_Upper" } }); // uppercase
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByTestId("gcs-create-name"), { target: { value: "a-valid-bucket-name" } });
    expect(submit.disabled).toBe(false);
  });
});

describe("ArtifactRegistryPage", () => {
  const repoId = "my-new-repo";
  const repoName = `projects/sockerless/locations/us-central1/repositories/${repoId}`;

  it("creates a repository through the real long-running create operation and shows it once the list refreshes", async () => {
    let sentBody: string | null = null;
    let listedAfterCreate = false;
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!sentBody) return jsonResponse({ repositories: [] });
          listedAfterCreate = true;
          return jsonResponse({ repositories: [{ name: repoName, format: "DOCKER", mode: "STANDARD_REPOSITORY" }] });
        },
      },
      {
        when: (url, init) =>
          url === `/v1/projects/sockerless/locations/us-central1/repositories?repositoryId=${repoId}` &&
          init?.method === "POST",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          // The simulator settles the LRO synchronously and returns it
          // already done — the console's waitArOperation loop must accept
          // that immediately-complete shape without polling operations.get.
          return jsonResponse({
            name: `projects/sockerless/locations/us-central1/operations/op-1`,
            done: true,
            response: { "@type": "type.googleapis.com/google.devtools.artifactregistry.v1.Repository", name: repoName, format: "DOCKER" },
          });
        },
      },
    ]);
    renderWithProviders(<ArtifactRegistryPage />);
    await screen.findByText("Store your build artifacts");

    fireEvent.click(screen.getByTestId("ar-create-repo"));
    const dialog = await screen.findByTestId("ar-create-dialog");
    expect((within(dialog).getByTestId("ar-create-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("ar-create-id"), { target: { value: repoId } });
    expect((within(dialog).getByTestId("ar-create-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("ar-create-submit"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(JSON.parse(sentBody!)).toEqual({ format: "DOCKER" });
    await waitFor(() => expect(screen.queryByTestId("ar-create-dialog")).toBeNull());
    await waitFor(() => expect(listedAfterCreate).toBe(true));
    expect(await screen.findByText(repoId)).toBeTruthy();
  });

  it("sends the format the operator picks", async () => {
    let sentBody: string | null = null;
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ repositories: [] }),
      },
      {
        when: (url, init) => url.startsWith("/v1/projects/sockerless/locations/us-central1/repositories?repositoryId=") && init?.method === "POST",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          return jsonResponse({ name: "projects/sockerless/locations/us-central1/operations/op-2", done: true, response: {} });
        },
      },
    ]);
    renderWithProviders(<ArtifactRegistryPage />);
    fireEvent.click(screen.getByTestId("ar-create-repo"));
    const dialog = await screen.findByTestId("ar-create-dialog");
    fireEvent.change(within(dialog).getByTestId("ar-create-id"), { target: { value: repoId } });
    fireEvent.change(within(dialog).getByTestId("ar-create-format"), { target: { value: "NPM" } });
    fireEvent.click(within(dialog).getByTestId("ar-create-submit"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(JSON.parse(sentBody!)).toEqual({ format: "NPM" });
  });

  it("surfaces the real 409 conflict when the repository already exists, without closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ repositories: [] }),
      },
      {
        when: (url, init) => url.startsWith("/v1/projects/sockerless/locations/us-central1/repositories?repositoryId=") && init?.method === "POST",
        respond: () =>
          jsonResponse(
            { error: { code: 409, message: `repository "${repoName}" already exists`, status: "ALREADY_EXISTS", details: [] } },
            409,
          ),
      },
    ]);
    renderWithProviders(<ArtifactRegistryPage />);
    await screen.findByText("Store your build artifacts");

    fireEvent.click(screen.getByTestId("ar-create-repo"));
    const dialog = await screen.findByTestId("ar-create-dialog");
    fireEvent.change(within(dialog).getByTestId("ar-create-id"), { target: { value: repoId } });
    fireEvent.click(within(dialog).getByTestId("ar-create-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(`repository "${repoName}" already exists`);
    expect(screen.getByTestId("ar-create-dialog")).toBeTruthy();
  });

  it("also opens the dialog from the empty state's primary action, once the empty read succeeds", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ repositories: [] }),
      },
    ]);
    renderWithProviders(<ArtifactRegistryPage />);
    const empty = await screen.findByText("Store your build artifacts");
    const primary = within(empty.closest(".gc-empty")!).getByRole("button", { name: "Create repository" }) as HTMLButtonElement;
    expect(primary.disabled).toBe(false);
    fireEvent.click(primary);
    expect(await screen.findByTestId("ar-create-dialog")).toBeTruthy();
  });

  it("blocks creation until the repository ID satisfies Artifact Registry's naming contract", () => {
    renderWithProviders(<ArtifactRegistryPage />);
    fireEvent.click(screen.getByTestId("ar-create-repo"));
    const submit = screen.getByTestId("ar-create-submit") as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByTestId("ar-create-id"), { target: { value: "1-starts-with-digit" } });
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByTestId("ar-create-id"), { target: { value: "a-valid-repo" } });
    expect(submit.disabled).toBe(false);
  });
});
