import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { signInToConsole, uniqueName } from "@sockerless/ui-core/testing";
import { ECSTasksPage, isStoppable } from "../pages/ECSTasksPage.js";
import { LambdaFunctionsPage } from "../pages/LambdaFunctionsPage.js";
import { ECRReposPage } from "../pages/ECRReposPage.js";
import { S3BucketsPage } from "../pages/S3BucketsPage.js";
import { S3CreateBucketPage } from "../pages/S3CreateBucketPage.js";
import { LogGroupsPage } from "../pages/LogGroupsPage.js";
import {
  createCWLogGroup,
  createECRRepository,
  createS3Bucket,
  fetchCWLogGroups,
  fetchECRRepos,
  fetchLambdaFunctions,
  fetchS3Buckets,
  putS3Object,
  type ECSTask,
} from "../api.js";
import { createImageFunction, preferHundredRowPages, renderWithNavigation, renderWithQuery } from "./fixtures.js";

// Each page passes AwsTable its own header actions: "View details" navigates to
// the selected resource's detail route, and Stop / Create / Delete drive the
// real operation over the console's federated, signed fetch. Every test
// arranges its resources through the real API, drives the page, and reads the
// result back from the simulator.

beforeAll(async () => {
  await signInToConsole();
  preferHundredRowPages("Functions", "Repositories", "General purpose buckets", "Log groups");
});
afterEach(cleanup);

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
  it("renders no default Delete, and disables View details and Stop with nothing selected", async () => {
    renderWithQuery(<ECSTasksPage />);
    await screen.findByText("No tasks");
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    expect((screen.getByTestId("ecs-view-task") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("ecs-stop-task") as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("LambdaFunctionsPage", () => {
  it("deletes the selected function through DELETE /2015-03-31/functions/{name}", async () => {
    const functionName = await createImageFunction();
    renderWithQuery(<LambdaFunctionsPage />);
    await screen.findByText(functionName);
    expect((screen.getByTestId("lambda-view-function") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${functionName}` }));
    fireEvent.click(screen.getByTestId("lambda-delete-function"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${functionName}?` });
    fireEvent.change(within(dialog).getByTestId("lambda-delete-function-confirm-input"), { target: { value: "confirm" } });
    fireEvent.click(within(dialog).getByTestId("lambda-delete-function-confirm"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect((await fetchLambdaFunctions()).map((fn) => fn.name)).not.toContain(functionName);
  });

  it("enables View details once a function is selected and navigates to its detail route", async () => {
    const functionName = await createImageFunction();
    renderWithNavigation("/ui/lambda", <LambdaFunctionsPage />, "/ui/lambda/:name");
    await screen.findByText(functionName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${functionName}` }));
    fireEvent.click(screen.getByTestId("lambda-view-function"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});

describe("ECRReposPage", () => {
  it("creates a repository through CreateRepository and shows it once the list refreshes", async () => {
    const repoName = uniqueName("repo");
    renderWithQuery(<ECRReposPage />);

    fireEvent.click(await screen.findByTestId("ecr-create-repo"));
    const dialog = await screen.findByRole("dialog", { name: "Create repository" });
    expect((within(dialog).getByTestId("ecr-create-repo-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("ecr-repo-name-input"), { target: { value: repoName } });
    expect((within(dialog).getByTestId("ecr-create-repo-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("ecr-create-repo-submit"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText(repoName)).toBeTruthy();
    expect((await fetchECRRepos()).find((repo) => repo.name === repoName)).toMatchObject({
      imageTagMutability: "MUTABLE",
      scanOnPush: false,
    });
  });

  it("surfaces ECR's RepositoryAlreadyExistsException rather than closing the create dialog", async () => {
    const repoName = uniqueName("repo");
    await createECRRepository(repoName);
    renderWithQuery(<ECRReposPage />);

    fireEvent.click(await screen.findByTestId("ecr-create-repo"));
    const dialog = await screen.findByRole("dialog", { name: "Create repository" });
    fireEvent.change(within(dialog).getByTestId("ecr-repo-name-input"), { target: { value: repoName } });
    fireEvent.click(within(dialog).getByTestId("ecr-create-repo-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("RepositoryAlreadyExistsException");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("deletes the selected repository through DeleteRepository", async () => {
    const repoName = uniqueName("repo");
    await createECRRepository(repoName);
    renderWithQuery(<ECRReposPage />);
    await screen.findByText(repoName);
    expect((screen.getByTestId("ecr-view-repo") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${repoName}` }));
    fireEvent.click(screen.getByTestId("ecr-delete-repo"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${repoName}?` });
    fireEvent.change(within(dialog).getByTestId("ecr-delete-repo-confirm-input"), { target: { value: "delete" } });
    fireEvent.click(within(dialog).getByTestId("ecr-delete-repo-confirm"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect((await fetchECRRepos()).map((repo) => repo.name)).not.toContain(repoName);
  });

  it("enables View details once a repository is selected and navigates to its detail route", async () => {
    const repoName = uniqueName("repo");
    await createECRRepository(repoName);
    renderWithNavigation("/ui/ecr", <ECRReposPage />, "/ui/ecr/:name");
    await screen.findByText(repoName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${repoName}` }));
    fireEvent.click(screen.getByTestId("ecr-view-repo"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});

describe("S3BucketsPage", () => {
  it("opens the Create bucket page from the header action", async () => {
    renderWithNavigation("/ui/s3", <S3BucketsPage />, "/ui/s3/create");
    fireEvent.click(await screen.findByTestId("s3-create-bucket"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });

  it("creates a bucket through CreateBucket and returns to the list", async () => {
    const bucketName = uniqueName("bucket");
    renderWithNavigation("/ui/s3/create", <S3CreateBucketPage />, "/ui/s3");
    fireEvent.change(screen.getByTestId("s3-bucket-name-input"), { target: { value: bucketName } });
    fireEvent.click(screen.getByTestId("s3-create-bucket-submit"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
    expect((await fetchS3Buckets()).map((bucket) => bucket.name)).toContain(bucketName);
  });

  it("re-creates a bucket the caller already owns in US East (N. Virginia), where CreateBucket answers 200", async () => {
    const bucketName = uniqueName("bucket");
    await createS3Bucket(bucketName);
    renderWithNavigation("/ui/s3/create", <S3CreateBucketPage />, "/ui/s3");
    fireEvent.change(screen.getByTestId("s3-bucket-name-input"), { target: { value: bucketName } });
    fireEvent.click(screen.getByTestId("s3-create-bucket-submit"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });

  it("refuses an invalid bucket name before any request goes out", async () => {
    renderWithNavigation("/ui/s3/create", <S3CreateBucketPage />, "/ui/s3");
    fireEvent.change(screen.getByTestId("s3-bucket-name-input"), { target: { value: "Not_Valid" } });
    fireEvent.click(screen.getByTestId("s3-create-bucket-submit"));
    expect(await screen.findByText(/Bucket name must be 3 to 63 characters/)).toBeTruthy();
    expect(screen.queryByTestId("landed-on-detail")).toBeNull();
  });

  it("surfaces S3's BucketNotEmpty error rather than closing the dialog", async () => {
    const bucketName = uniqueName("bucket");
    await createS3Bucket(bucketName);
    await putS3Object(bucketName, "keep.txt", new TextEncoder().encode("kept"), "text/plain");
    renderWithQuery(<S3BucketsPage />);
    await screen.findByText(bucketName);
    expect((screen.getByTestId("s3-delete-bucket") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("radio", { name: `Select ${bucketName}` }));
    fireEvent.click(screen.getByTestId("s3-delete-bucket"));
    const dialog = await screen.findByRole("dialog", { name: "Delete bucket" });
    const confirm = within(dialog).getByTestId("s3-delete-bucket-confirm") as HTMLButtonElement;
    // Delete stays disabled until the operator types the bucket name.
    expect(confirm.disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("s3-delete-bucket-confirm-input"), { target: { value: bucketName } });
    expect(confirm.disabled).toBe(false);
    fireEvent.click(confirm);

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("BucketNotEmpty");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect((await fetchS3Buckets()).map((bucket) => bucket.name)).toContain(bucketName);
  });

  it("links each bucket name to its detail route", async () => {
    const bucketName = uniqueName("bucket");
    await createS3Bucket(bucketName);
    renderWithNavigation("/ui/s3", <S3BucketsPage />, "/ui/s3/:name");
    fireEvent.click(await screen.findByRole("link", { name: bucketName }));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});

describe("LogGroupsPage", () => {
  it("creates a log group through CreateLogGroup and shows it once the list refreshes", async () => {
    const logGroupName = `/ecs/${uniqueName("task")}`;
    renderWithQuery(<LogGroupsPage />);

    fireEvent.click(await screen.findByTestId("logs-create-log-group"));
    const dialog = await screen.findByRole("dialog", { name: "Create log group" });
    expect((within(dialog).getByTestId("logs-create-log-group-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("logs-log-group-name-input"), { target: { value: logGroupName } });
    expect((within(dialog).getByTestId("logs-create-log-group-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("logs-create-log-group-submit"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText(logGroupName)).toBeTruthy();
    expect((await fetchCWLogGroups()).find((group) => group.name === logGroupName)?.retentionInDays).toBe(0);
  });

  it("surfaces CloudWatch Logs' ResourceAlreadyExistsException rather than closing the create dialog", async () => {
    const logGroupName = `/ecs/${uniqueName("task")}`;
    await createCWLogGroup(logGroupName);
    renderWithQuery(<LogGroupsPage />);

    fireEvent.click(await screen.findByTestId("logs-create-log-group"));
    const dialog = await screen.findByRole("dialog", { name: "Create log group" });
    fireEvent.change(within(dialog).getByTestId("logs-log-group-name-input"), { target: { value: logGroupName } });
    fireEvent.click(within(dialog).getByTestId("logs-create-log-group-submit"));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("ResourceAlreadyExistsException");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("deletes the selected log group through DeleteLogGroup", async () => {
    const logGroupName = `/ecs/${uniqueName("task")}`;
    await createCWLogGroup(logGroupName);
    renderWithQuery(<LogGroupsPage />);
    await screen.findByText(logGroupName);
    expect((screen.getByTestId("logs-view-log-group") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${logGroupName}` }));
    fireEvent.click(screen.getByTestId("logs-delete-log-group"));
    const dialog = await screen.findByRole("dialog", { name: `Delete ${logGroupName}?` });
    fireEvent.click(within(dialog).getByTestId("logs-delete-log-group-confirm"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect((await fetchCWLogGroups()).map((group) => group.name)).not.toContain(logGroupName);
  });

  it("enables View details once a log group is selected and navigates to its detail route", async () => {
    const logGroupName = `/ecs/${uniqueName("task")}`;
    await createCWLogGroup(logGroupName);
    renderWithNavigation("/ui/logs", <LogGroupsPage />, "/ui/logs/:name");
    await screen.findByText(logGroupName);

    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${logGroupName}` }));
    fireEvent.click(screen.getByTestId("logs-view-log-group"));
    expect(await screen.findByTestId("landed-on-detail")).toBeTruthy();
  });
});
