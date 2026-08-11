import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ECSTasksPage, isStoppable } from "../pages/ECSTasksPage.js";
import { LambdaFunctionsPage } from "../pages/LambdaFunctionsPage.js";
import { ECRReposPage } from "../pages/ECRReposPage.js";
import { S3BucketsPage } from "../pages/S3BucketsPage.js";
import { LogGroupsPage } from "../pages/LogGroupsPage.js";
import type { ECSTask } from "../api.js";

/**
 * The Amazon ECS, AWS Lambda, Amazon ECR, Amazon S3, and CloudWatch Logs
 * pages used to render an enabled "View details" and "Delete" header action
 * with no handler — a fake affordance (BUG-2637). Each page now passes
 * AwsTable its own `actions`, so the inert defaults never render. "View
 * details" is now a real, initially-disabled action that navigates to the
 * resource's detail route once exactly one row is selected, alongside the
 * real Stop/Delete mutation that goes out over the same federated wire the
 * reads use. These tests drive the full read → select → confirm → mutate
 * round trip against a mocked federated fetch, the way ContainersPage.test.tsx
 * drives admin's mutation flows, rather than reaching into the mutation
 * internals.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

afterEach(() => {
  cleanup();
  mockFetch.mockReset();
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

function noContent(status = 204): Response {
  return new Response(null, { status });
}

function xmlResponse(xml: string, status = 200): Response {
  return new Response(xml, { status, headers: { "content-type": "application/xml" } });
}

function xmlError(code: string, message: string, status: number): Response {
  return xmlResponse(`<Error><Code>${code}</Code><Message>${message}</Message></Error>`, status);
}

function targetOf(init: RequestInit | undefined): string {
  return new Headers(init?.headers).get("x-amz-target") ?? "";
}

async function bodyOf(init: RequestInit | undefined): Promise<string> {
  return typeof init?.body === "string" ? init.body : "";
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

function renderWithQuery(ui: React.ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

/** Proves "View details" actually navigates, not just that it's enabled: the
 * list page and its detail route both mount under a real `<Routes>`, and the
 * marker only appears once the click has landed on the detail route. */
function renderWithNavigation(listPath: string, list: React.ReactElement, detailPath: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[listPath]}>
        <Routes>
          <Route path={listPath} element={list} />
          <Route path={detailPath} element={<div data-testid="landed-on-detail">landed</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("isStoppable", () => {
  const base: ECSTask = {
    taskArn: "arn:aws:ecs:us-east-1:123456789012:task/default/abc",
    status: "RUNNING",
    clusterArn: "arn:aws:ecs:us-east-1:123456789012:cluster/default",
    launchType: "FARGATE",
    cpu: "256",
    memory: "512",
  };

  it("allows stopping a running or pending task", () => {
    expect(isStoppable({ ...base, status: "RUNNING" })).toBe(true);
    expect(isStoppable({ ...base, status: "PENDING" })).toBe(true);
    expect(isStoppable({ ...base, status: "PROVISIONING" })).toBe(true);
  });

  it("refuses a task that is already stopped or tearing down", () => {
    expect(isStoppable({ ...base, status: "STOPPED" })).toBe(false);
    expect(isStoppable({ ...base, status: "DEPROVISIONING" })).toBe(false);
  });
});

describe("ECSTasksPage", () => {
  const clusterArn = "arn:aws:ecs:us-east-1:123456789012:cluster/default";
  const taskArn = "arn:aws:ecs:us-east-1:123456789012:task/default/abc123";

  function installList() {
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.ListClusters",
        respond: () => jsonResponse({ clusterArns: [clusterArn] }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.ListTasks",
        respond: () => jsonResponse({ taskArns: [taskArn] }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.DescribeTasks",
        respond: () =>
          jsonResponse({
            tasks: [{ taskArn, lastStatus: "RUNNING", clusterArn, launchType: "FARGATE", cpu: "256", memory: "512" }],
          }),
      },
    ]);
  }

  it("renders no default Delete, and disables View details and Stop with nothing selected", async () => {
    installList();
    renderWithQuery(<ECSTasksPage />);
    await screen.findByText(taskArn);
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    expect((screen.getByTestId("ecs-view-task") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("ecs-stop-task") as HTMLButtonElement).disabled).toBe(true);
  });

  it("enables View details once a task is selected and navigates to its detail route", async () => {
    installList();
    renderWithNavigation("/ui/ecs", <ECSTasksPage />, "/ui/ecs/:taskArn");
    await screen.findByText(taskArn);

    const view = screen.getByTestId("ecs-view-task") as HTMLButtonElement;
    expect(view.disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${taskArn}` }));
    expect(view.disabled).toBe(false);
    fireEvent.click(view);
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });

  it("stops the selected task through the real StopTask operation", async () => {
    let stopCalled: { url: string; init?: RequestInit } | null = null;
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.ListClusters",
        respond: () => jsonResponse({ clusterArns: [clusterArn] }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.ListTasks",
        respond: () => jsonResponse({ taskArns: [taskArn] }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.DescribeTasks",
        respond: () =>
          jsonResponse({
            tasks: [{ taskArn, lastStatus: "RUNNING", clusterArn, launchType: "FARGATE", cpu: "256", memory: "512" }],
          }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.StopTask",
        respond: (init) => {
          stopCalled = { url: "/", init };
          return jsonResponse({ task: { taskArn, lastStatus: "STOPPED", clusterArn } });
        },
      },
    ]);
    renderWithQuery(<ECSTasksPage />);
    await screen.findByText(taskArn);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${taskArn}` }));
    fireEvent.click(screen.getByTestId("ecs-stop-task"));
    const dialog = await screen.findByRole("dialog", { name: "Stop this task?" });
    fireEvent.click(within(dialog).getByTestId("ecs-stop-task-confirm"));

    await waitFor(() => expect(stopCalled).not.toBeNull());
    const sent = JSON.parse(await bodyOf(stopCalled!.init)) as { cluster: string; task: string };
    expect(sent.cluster).toBe(clusterArn);
    expect(sent.task).toBe(taskArn);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});

describe("LambdaFunctionsPage", () => {
  const functionName = "my-function";

  it("deletes the selected function through the real DELETE /2015-03-31/functions/{name}", async () => {
    let deleteCalled: string | null = null;
    installFetch([
      {
        when: (url, init) => url === "/2015-03-31/functions" && (init?.method ?? "GET") === "GET",
        respond: () =>
          jsonResponse({
            Functions: [
              {
                FunctionName: functionName,
                Runtime: "nodejs20.x",
                State: "Active",
                MemorySize: 128,
                Timeout: 3,
                LastModified: "2026-01-01T00:00:00.000+0000",
              },
            ],
          }),
      },
      {
        when: (url, init) => url === `/2015-03-31/functions/${functionName}` && init?.method === "DELETE",
        respond: (init) => {
          deleteCalled = init?.method ?? "";
          return noContent();
        },
      },
    ]);
    renderWithQuery(<LambdaFunctionsPage />);
    await screen.findByText(functionName);
    expect((screen.getByTestId("lambda-view-function") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${functionName}` }));
    fireEvent.click(screen.getByTestId("lambda-delete-function"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${functionName}?` });
    fireEvent.click(within(dialog).getByTestId("lambda-delete-function-confirm"));

    await waitFor(() => expect(deleteCalled).toBe("DELETE"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("enables View details once a function is selected and navigates to its detail route", async () => {
    installFetch([
      {
        when: (url, init) => url === "/2015-03-31/functions" && (init?.method ?? "GET") === "GET",
        respond: () =>
          jsonResponse({
            Functions: [
              {
                FunctionName: functionName,
                Runtime: "nodejs20.x",
                State: "Active",
                MemorySize: 128,
                Timeout: 3,
                LastModified: "2026-01-01T00:00:00.000+0000",
              },
            ],
          }),
      },
    ]);
    renderWithNavigation("/ui/lambda", <LambdaFunctionsPage />, "/ui/lambda/:name");
    await screen.findByText(functionName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${functionName}` }));
    fireEvent.click(screen.getByTestId("lambda-view-function"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});

describe("ECRReposPage", () => {
  const repoName = "my-repo";

  it("creates a repository through the real CreateRepository operation and shows it once the list refreshes", async () => {
    let sentBody: string | null = null;
    let listedAfterCreate = false;
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.DescribeRepositories",
        respond: () => {
          if (!sentBody) return jsonResponse({ repositories: [] });
          listedAfterCreate = true;
          return jsonResponse({
            repositories: [
              {
                repositoryName: repoName,
                repositoryUri: `123456789012.dkr.ecr.us-east-1.amazonaws.com/${repoName}`,
                createdAt: 1750000000,
              },
            ],
          });
        },
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.CreateRepository",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          return jsonResponse({
            repository: {
              repositoryName: repoName,
              repositoryUri: `123456789012.dkr.ecr.us-east-1.amazonaws.com/${repoName}`,
              createdAt: 1750000000,
            },
          });
        },
      },
    ]);
    renderWithQuery(<ECRReposPage />);
    await screen.findByText("No repositories");

    fireEvent.click(screen.getByTestId("ecr-create-repo"));
    const dialog = await screen.findByRole("dialog", { name: "Create repository" });
    expect((within(dialog).getByTestId("ecr-create-repo-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("ecr-repo-name-input"), { target: { value: repoName } });
    expect((within(dialog).getByTestId("ecr-create-repo-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("ecr-create-repo-submit"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(JSON.parse(sentBody!)).toEqual({ repositoryName: repoName });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(listedAfterCreate).toBe(true));
    expect(await screen.findByText(repoName)).toBeTruthy();
  });

  it("surfaces ECR's RepositoryAlreadyExistsException rather than closing the create dialog", async () => {
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.DescribeRepositories",
        respond: () => jsonResponse({ repositories: [] }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.CreateRepository",
        respond: () =>
          jsonResponse(
            { __type: "RepositoryAlreadyExistsException", message: `The repository with name '${repoName}' already exists` },
            400,
          ),
      },
    ]);
    renderWithQuery(<ECRReposPage />);
    await screen.findByText("No repositories");

    fireEvent.click(screen.getByTestId("ecr-create-repo"));
    const dialog = await screen.findByRole("dialog", { name: "Create repository" });
    fireEvent.change(within(dialog).getByTestId("ecr-repo-name-input"), { target: { value: repoName } });
    fireEvent.click(within(dialog).getByTestId("ecr-create-repo-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("RepositoryAlreadyExistsException");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("deletes the selected repository through DeleteRepository with force", async () => {
    let sentBody: string | null = null;
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.DescribeRepositories",
        respond: () =>
          jsonResponse({
            repositories: [
              {
                repositoryName: repoName,
                repositoryUri: `123456789012.dkr.ecr.us-east-1.amazonaws.com/${repoName}`,
                createdAt: 1750000000,
              },
            ],
          }),
      },
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.DeleteRepository",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          return jsonResponse({ repository: { repositoryName: repoName } });
        },
      },
    ]);
    renderWithQuery(<ECRReposPage />);
    await screen.findByText(repoName);
    expect((screen.getByTestId("ecr-view-repo") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${repoName}` }));
    fireEvent.click(screen.getByTestId("ecr-delete-repo"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${repoName}?` });
    fireEvent.click(within(dialog).getByTestId("ecr-delete-repo-confirm"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    const sent = JSON.parse(sentBody!) as { repositoryName: string; force: boolean };
    expect(sent.repositoryName).toBe(repoName);
    expect(sent.force).toBe(true);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("enables View details once a repository is selected and navigates to its detail route", async () => {
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "AmazonEC2ContainerRegistry_V20150921.DescribeRepositories",
        respond: () =>
          jsonResponse({
            repositories: [
              {
                repositoryName: repoName,
                repositoryUri: `123456789012.dkr.ecr.us-east-1.amazonaws.com/${repoName}`,
                createdAt: 1750000000,
              },
            ],
          }),
      },
    ]);
    renderWithNavigation("/ui/ecr", <ECRReposPage />, "/ui/ecr/:name");
    await screen.findByText(repoName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${repoName}` }));
    fireEvent.click(screen.getByTestId("ecr-view-repo"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});

describe("S3BucketsPage", () => {
  const bucketName = "my-bucket";

  it("creates a bucket through the real CreateBucket operation and shows it once the list refreshes", async () => {
    let created: { method?: string; url: string } | null = null;
    let listedAfterCreate = false;
    installFetch([
      {
        when: (url, init) => url === "/" && (init?.method ?? "GET") === "GET",
        respond: () => {
          if (!created) return xmlResponse(`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`);
          listedAfterCreate = true;
          return xmlResponse(
            `<ListAllMyBucketsResult><Buckets><Bucket><Name>${bucketName}</Name><CreationDate>2026-01-01T00:00:00.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`,
          );
        },
      },
      {
        when: (url, init) => url === `/${bucketName}` && init?.method === "PUT",
        respond: (init) => {
          created = { method: init?.method, url: `/${bucketName}` };
          return new Response(null, { status: 200, headers: { Location: `/${bucketName}` } });
        },
      },
    ]);
    renderWithQuery(<S3BucketsPage />);
    await screen.findByText("No buckets");

    fireEvent.click(screen.getByTestId("s3-create-bucket"));
    const dialog = await screen.findByRole("dialog", { name: "Create bucket" });
    expect((within(dialog).getByTestId("s3-create-bucket-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("s3-bucket-name-input"), { target: { value: bucketName } });
    expect((within(dialog).getByTestId("s3-create-bucket-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("s3-create-bucket-submit"));

    await waitFor(() => expect(created).not.toBeNull());
    expect(created!.method).toBe("PUT");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(listedAfterCreate).toBe(true));
    expect(await screen.findByText(bucketName)).toBeTruthy();
  });

  it("surfaces S3's BucketAlreadyOwnedByYou error rather than closing the create dialog", async () => {
    installFetch([
      {
        when: (url, init) => url === "/" && (init?.method ?? "GET") === "GET",
        respond: () => xmlResponse(`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`),
      },
      {
        when: (url, init) => url === `/${bucketName}` && init?.method === "PUT",
        respond: () =>
          xmlError("BucketAlreadyOwnedByYou", "Your previous request to create the named bucket succeeded", 409),
      },
    ]);
    renderWithQuery(<S3BucketsPage />);
    await screen.findByText("No buckets");

    fireEvent.click(screen.getByTestId("s3-create-bucket"));
    const dialog = await screen.findByRole("dialog", { name: "Create bucket" });
    fireEvent.change(within(dialog).getByTestId("s3-bucket-name-input"), { target: { value: bucketName } });
    fireEvent.click(within(dialog).getByTestId("s3-create-bucket-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("BucketAlreadyOwnedByYou");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("surfaces S3's BucketNotEmpty error rather than closing the dialog", async () => {
    installFetch([
      {
        when: (url, init) => url === "/" && (init?.method ?? "GET") === "GET",
        respond: () =>
          xmlResponse(
            `<ListAllMyBucketsResult><Buckets><Bucket><Name>${bucketName}</Name><CreationDate>2026-01-01T00:00:00.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`,
          ),
      },
      {
        when: (url, init) => url === `/${bucketName}` && init?.method === "DELETE",
        respond: () => xmlError("BucketNotEmpty", "The bucket you tried to delete is not empty", 409),
      },
    ]);
    renderWithQuery(<S3BucketsPage />);
    await screen.findByText(bucketName);
    expect((screen.getByTestId("s3-view-bucket") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${bucketName}` }));
    fireEvent.click(screen.getByTestId("s3-delete-bucket"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${bucketName}?` });
    fireEvent.click(within(dialog).getByTestId("s3-delete-bucket-confirm"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("BucketNotEmpty");
    // The failed delete never dismisses the confirmation.
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("enables View details once a bucket is selected and navigates to its detail route", async () => {
    installFetch([
      {
        when: (url, init) => url === "/" && (init?.method ?? "GET") === "GET",
        respond: () =>
          xmlResponse(
            `<ListAllMyBucketsResult><Buckets><Bucket><Name>${bucketName}</Name><CreationDate>2026-01-01T00:00:00.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`,
          ),
      },
    ]);
    renderWithNavigation("/ui/s3", <S3BucketsPage />, "/ui/s3/:name");
    await screen.findByText(bucketName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${bucketName}` }));
    fireEvent.click(screen.getByTestId("s3-view-bucket"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});

describe("LogGroupsPage", () => {
  const logGroupName = "/ecs/my-task";

  it("creates a log group through the real CreateLogGroup operation and shows it once the list refreshes", async () => {
    let sentBody: string | null = null;
    let listedAfterCreate = false;
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.DescribeLogGroups",
        respond: () => {
          if (!sentBody) return jsonResponse({ logGroups: [] });
          listedAfterCreate = true;
          return jsonResponse({
            logGroups: [{ logGroupName, creationTime: 1750000000000, retentionInDays: 0, storedBytes: 0 }],
          });
        },
      },
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.CreateLogGroup",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          return jsonResponse({});
        },
      },
    ]);
    renderWithQuery(<LogGroupsPage />);
    await screen.findByText("No log groups");

    fireEvent.click(screen.getByTestId("logs-create-log-group"));
    const dialog = await screen.findByRole("dialog", { name: "Create log group" });
    expect((within(dialog).getByTestId("logs-create-log-group-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("logs-log-group-name-input"), { target: { value: logGroupName } });
    expect((within(dialog).getByTestId("logs-create-log-group-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("logs-create-log-group-submit"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(JSON.parse(sentBody!)).toEqual({ logGroupName });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(listedAfterCreate).toBe(true));
    expect(await screen.findByText(logGroupName)).toBeTruthy();
  });

  it("surfaces CloudWatch Logs' ResourceAlreadyExistsException rather than closing the create dialog", async () => {
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.DescribeLogGroups",
        respond: () => jsonResponse({ logGroups: [] }),
      },
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.CreateLogGroup",
        respond: () =>
          jsonResponse(
            { __type: "ResourceAlreadyExistsException", message: `The specified log group already exists: ${logGroupName}` },
            400,
          ),
      },
    ]);
    renderWithQuery(<LogGroupsPage />);
    await screen.findByText("No log groups");

    fireEvent.click(screen.getByTestId("logs-create-log-group"));
    const dialog = await screen.findByRole("dialog", { name: "Create log group" });
    fireEvent.change(within(dialog).getByTestId("logs-log-group-name-input"), { target: { value: logGroupName } });
    fireEvent.click(within(dialog).getByTestId("logs-create-log-group-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("ResourceAlreadyExistsException");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("deletes the selected log group through the real DeleteLogGroup operation", async () => {
    let sentBody: string | null = null;
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.DescribeLogGroups",
        respond: () =>
          jsonResponse({
            logGroups: [{ logGroupName, creationTime: 1750000000000, retentionInDays: 14, storedBytes: 2048 }],
          }),
      },
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.DeleteLogGroup",
        respond: (init) => {
          sentBody = typeof init?.body === "string" ? init.body : null;
          return jsonResponse({});
        },
      },
    ]);
    renderWithQuery(<LogGroupsPage />);
    await screen.findByText(logGroupName);
    expect((screen.getByTestId("logs-view-log-group") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${logGroupName}` }));
    fireEvent.click(screen.getByTestId("logs-delete-log-group"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${logGroupName}?` });
    fireEvent.click(within(dialog).getByTestId("logs-delete-log-group-confirm"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(JSON.parse(sentBody!)).toEqual({ logGroupName });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("enables View details once a log group is selected and navigates to its detail route", async () => {
    installFetch([
      {
        when: (_url, init) => targetOf(init) === "Logs_20140328.DescribeLogGroups",
        respond: () =>
          jsonResponse({
            logGroups: [{ logGroupName, creationTime: 1750000000000, retentionInDays: 14, storedBytes: 2048 }],
          }),
      },
    ]);
    renderWithNavigation("/ui/logs", <LogGroupsPage />, "/ui/logs/:name");
    await screen.findByText(logGroupName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${logGroupName}` }));
    fireEvent.click(screen.getByTestId("logs-view-log-group"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});
