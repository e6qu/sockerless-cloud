import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { signInToConsole, uniqueName } from "@sockerless/ui-core/testing";
import { LambdaFunctionsPage } from "../pages/LambdaFunctionsPage.js";
import { LambdaFunctionDetailPage } from "../pages/LambdaFunctionDetailPage.js";
import { S3BucketDetailPage } from "../pages/S3BucketDetailPage.js";
import {
  createCWLogGroup,
  createECRRepository,
  createS3Bucket,
  fetchCWLogGroup,
  fetchECRRepo,
  fetchECRTags,
  fetchECSTaskDefinition,
  fetchLambdaFunctionDetail,
  fetchS3BucketTagging,
  fetchS3BucketVersioning,
  putCWLogGroupRetention,
  putECRImageScanningConfiguration,
  putECRImageTagMutability,
  registerECSTaskDefinition,
  tagECRResource,
  tagLambdaResource,
  untagECRResource,
  untagLambdaResource,
} from "../api.js";
import {
  IMAGE_URI,
  createImageFunction,
  createLambdaExecutionRole,
  enabledButton,
  preferHundredRowPages,
  renderDetail,
  renderWithQuery,
} from "./fixtures.js";

// The console's update, create, and lifecycle flows — AWS Lambda
// CreateFunction / UpdateFunctionConfiguration / TagResource, CloudWatch Logs
// retention, Amazon S3 versioning and tagging, Amazon ECR settings and tags,
// and Amazon ECS RegisterTaskDefinition — each driven against the real
// simulator and read back through the service's own read operation.

beforeAll(async () => {
  await signInToConsole();
  preferHundredRowPages("Functions");
});
afterEach(cleanup);

describe("Lambda CreateFunction", () => {
  it("creates a container-image function through POST /2015-03-31/functions", async () => {
    const functionName = uniqueName("img-fn");
    const role = await createLambdaExecutionRole();
    renderWithQuery(<LambdaFunctionsPage />);

    fireEvent.click(await screen.findByTestId("lambda-create-function"));
    const dialog = await screen.findByRole("dialog", { name: "Create function" });
    expect((within(dialog).getByTestId("lambda-create-function-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("lambda-function-name-input"), { target: { value: functionName } });
    fireEvent.change(within(dialog).getByTestId("lambda-image-uri-input"), { target: { value: IMAGE_URI } });
    fireEvent.change(within(dialog).getByTestId("lambda-role-input"), { target: { value: role } });
    expect((within(dialog).getByTestId("lambda-create-function-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("lambda-create-function-submit"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText(functionName)).toBeTruthy();
    const created = await fetchLambdaFunctionDetail(functionName);
    expect(created.configuration).toMatchObject({ packageType: "Image", role, memorySize: 128, timeout: 3 });
    expect(created.code.imageUri).toBe(IMAGE_URI);
  });
});

describe("Lambda function detail edit/tags", () => {
  it("updates configuration through PUT /2015-03-31/functions/{name}/configuration", async () => {
    const functionName = await createImageFunction();
    renderDetail(<LambdaFunctionDetailPage />, "name", functionName);
    await screen.findByTestId("lambda-function-summary");

    fireEvent.click(await enabledButton("lambda-function-edit"));
    const dialog = await screen.findByRole("dialog", { name: "Edit basic settings" });
    fireEvent.change(within(dialog).getByTestId("lambda-edit-config-memory"), { target: { value: "512" } });
    fireEvent.change(within(dialog).getByTestId("lambda-edit-config-timeout"), { target: { value: "30" } });
    fireEvent.change(within(dialog).getByTestId("lambda-edit-config-description"), { target: { value: "updated" } });
    fireEvent.click(within(dialog).getByTestId("lambda-edit-config-save"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const updated = await fetchLambdaFunctionDetail(functionName);
    expect(updated.configuration).toMatchObject({ memorySize: 512, timeout: 30, description: "updated", environment: [] });
  });

  it("adds a tag through TagResource on the function ARN, keeping the existing ones", async () => {
    const functionName = await createImageFunction();
    await tagLambdaResource((await fetchLambdaFunctionDetail(functionName)).configuration.arn, { team: "platform" });
    renderDetail(<LambdaFunctionDetailPage />, "name", functionName);
    await screen.findByTestId("lambda-function-summary");

    fireEvent.click(screen.getByRole("tab", { name: "Tags" }));
    fireEvent.click(await enabledButton("lambda-function-manage-tags"));
    const dialog = await screen.findByRole("dialog", { name: "Manage tags" });
    // The existing tag pre-populates row 0; add a second row.
    fireEvent.click(within(dialog).getByRole("button", { name: "Add tag" }));
    fireEvent.change(within(dialog).getByTestId("lambda-function-tag-key-1"), { target: { value: "env" } });
    fireEvent.change(within(dialog).getByTestId("lambda-function-tag-value-1"), { target: { value: "prod" } });
    fireEvent.click(within(dialog).getByTestId("lambda-function-tags-save"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect((await fetchLambdaFunctionDetail(functionName)).tags).toEqual({ team: "platform", env: "prod" });
  });
});

describe("S3 bucket versioning and tagging", () => {
  it("enables versioning through PUT /{bucket}?versioning", async () => {
    const bucketName = uniqueName("bucket");
    await createS3Bucket(bucketName);
    renderDetail(<S3BucketDetailPage />, "name", bucketName);
    fireEvent.click(await screen.findByRole("tab", { name: "Properties" }));
    await screen.findByTestId("s3-bucket-summary");

    fireEvent.click(await enabledButton("s3-bucket-edit-versioning"));
    const dialog = await screen.findByRole("dialog", { name: "Edit Bucket Versioning" });
    fireEvent.click(within(dialog).getByRole("checkbox"));
    fireEvent.click(within(dialog).getByTestId("s3-versioning-save"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await fetchS3BucketVersioning(bucketName)).toBe("Enabled");
  });

  it("replaces the tag set through PUT /{bucket}?tagging", async () => {
    const bucketName = uniqueName("bucket");
    await createS3Bucket(bucketName);
    renderDetail(<S3BucketDetailPage />, "name", bucketName);
    fireEvent.click(await screen.findByRole("tab", { name: "Properties" }));
    await screen.findByTestId("s3-bucket-summary");

    fireEvent.click(await enabledButton("s3-bucket-manage-tags"));
    const dialog = await screen.findByRole("dialog", { name: "Edit bucket tags" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add tag" }));
    fireEvent.change(within(dialog).getByTestId("s3-bucket-tag-key-0"), { target: { value: "owner" } });
    fireEvent.change(within(dialog).getByTestId("s3-bucket-tag-value-0"), { target: { value: "team" } });
    fireEvent.click(within(dialog).getByTestId("s3-bucket-tags-save"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await fetchS3BucketTagging(bucketName)).toEqual({ owner: "team" });
  });
});

// The retention, ECR settings, and ECS task-definition flows drive their
// operations through a Cloudscape Select, which does not open its option
// listbox under jsdom's synthetic events, so these tests call the api.ts
// functions the dialogs call. The Playwright suite drives the dialogs
// themselves in a real browser.
describe("api.ts operations", () => {
  it("PutRetentionPolicy sets a fixed retention; DeleteRetentionPolicy clears it", async () => {
    const logGroupName = `/ecs/${uniqueName("task")}`;
    await createCWLogGroup(logGroupName);
    await putCWLogGroupRetention(logGroupName, 30);
    expect((await fetchCWLogGroup(logGroupName)).retentionInDays).toBe(30);
    await putCWLogGroupRetention(logGroupName, 0);
    expect((await fetchCWLogGroup(logGroupName)).retentionInDays).toBe(0);
  });

  it("ECR PutImageTagMutability and PutImageScanningConfiguration change the repository's settings", async () => {
    const repoName = uniqueName("repo");
    await createECRRepository(repoName);
    await putECRImageTagMutability(repoName, "IMMUTABLE");
    await putECRImageScanningConfiguration(repoName, true);
    expect(await fetchECRRepo(repoName)).toMatchObject({ imageTagMutability: "IMMUTABLE", scanOnPush: true });
  });

  it("ECR TagResource/UntagResource set and remove tags on the repository ARN", async () => {
    const repo = await createECRRepository(uniqueName("repo"));
    await tagECRResource(repo.arn, { team: "platform", stale: "yes" });
    expect(await fetchECRTags(repo.arn)).toEqual({ team: "platform", stale: "yes" });
    await untagECRResource(repo.arn, ["stale"]);
    expect(await fetchECRTags(repo.arn)).toEqual({ team: "platform" });
  });

  it("RegisterTaskDefinition registers a minimal EC2 task definition", async () => {
    const family = uniqueName("web");
    const ref = await registerECSTaskDefinition({ family, image: "nginx:latest", cpu: "", memory: "", launchType: "EC2" });
    expect(ref).toBe(`${family}:1`);
    const registered = await fetchECSTaskDefinition(ref);
    expect(registered).toMatchObject({ family, revision: 1, networkMode: "bridge", requiresCompatibilities: ["EC2"] });
    expect(registered.containerDefinitions).toMatchObject([{ name: family, image: "nginx:latest", essential: true }]);
  });

  it("Lambda UntagResource removes every listed tag key", async () => {
    const functionName = await createImageFunction();
    const { arn } = (await fetchLambdaFunctionDetail(functionName)).configuration;
    await tagLambdaResource(arn, { a: "1", b: "2", c: "3" });
    await untagLambdaResource(arn, ["a", "b"]);
    expect((await fetchLambdaFunctionDetail(functionName)).tags).toEqual({ c: "3" });
  });

  it("GetBucketTagging reads NoSuchTagSet as an empty tag set", async () => {
    const bucketName = uniqueName("bucket");
    await createS3Bucket(bucketName);
    await expect(fetchS3BucketTagging(bucketName)).resolves.toEqual({});
  });
});
