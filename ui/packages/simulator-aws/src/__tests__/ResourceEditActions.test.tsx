import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { LambdaFunctionsPage } from "../pages/LambdaFunctionsPage.js";
import { LambdaFunctionDetailPage } from "../pages/LambdaFunctionDetailPage.js";
import { S3BucketDetailPage } from "../pages/S3BucketDetailPage.js";

/**
 * The AWS console's update, create, and lifecycle flows added in the console
 * full-parity pass — Lambda CreateFunction / UpdateFunctionConfiguration /
 * Invoke / TagResource, CloudWatch Logs PutRetentionPolicy /
 * DeleteRetentionPolicy, S3 PutBucketVersioning / PutBucketTagging, ECR
 * PutImageTagMutability / PutImageScanningConfiguration / TagResource, and ECS
 * RegisterTaskDefinition / RunTask — each drives the real operation over the
 * same federated fetch the reads use. These tests pin the request shaping of
 * every one of those operations against a mocked federated fetch, the same way
 * ResourceHeaderActions.test.tsx pins the delete/stop flows.
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

function xmlResponse(xml: string, status = 200): Response {
  return new Response(xml, { status, headers: { "content-type": "application/xml" } });
}

function targetOf(init: RequestInit | undefined): string {
  return new Headers(init?.headers).get("x-amz-target") ?? "";
}

function bodyOf(init: RequestInit | undefined): string {
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

// A router that binds the encoded identifier to the route param the detail
// pages read from useParams (name / taskArn), mirroring the real routes.
function renderDetail(ui: React.ReactElement, param: string, id: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/detail/${encodeURIComponent(id)}`]}>
        <Routes>
          <Route path={`/detail/:${param}`} element={ui} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Lambda CreateFunction", () => {
  it("creates a container-image function through POST /2015-03-31/functions", async () => {
    let sentBody: string | null = null;
    installFetch([
      {
        when: (url, init) => url === "/2015-03-31/functions" && (init?.method ?? "GET") === "GET",
        respond: () => jsonResponse({ Functions: [] }),
      },
      {
        when: (url, init) => url === "/2015-03-31/functions" && init?.method === "POST",
        respond: (init) => {
          sentBody = bodyOf(init);
          return jsonResponse({ FunctionName: "img-fn" }, 201);
        },
      },
    ]);
    renderWithQuery(<LambdaFunctionsPage />);
    await screen.findByText("No functions");

    fireEvent.click(screen.getByTestId("lambda-create-function"));
    const dialog = await screen.findByRole("dialog", { name: "Create function" });
    expect((within(dialog).getByTestId("lambda-create-function-submit") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByTestId("lambda-function-name-input"), { target: { value: "img-fn" } });
    fireEvent.change(within(dialog).getByTestId("lambda-image-uri-input"), {
      target: { value: "123456789012.dkr.ecr.us-east-1.amazonaws.com/app:latest" },
    });
    fireEvent.change(within(dialog).getByTestId("lambda-role-input"), {
      target: { value: "arn:aws:iam::123456789012:role/lambda-role" },
    });
    expect((within(dialog).getByTestId("lambda-create-function-submit") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(within(dialog).getByTestId("lambda-create-function-submit"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    const sent = JSON.parse(sentBody!) as Record<string, unknown>;
    expect(sent.FunctionName).toBe("img-fn");
    expect(sent.PackageType).toBe("Image");
    expect(sent.Code).toEqual({ ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/app:latest" });
    expect(sent.Role).toBe("arn:aws:iam::123456789012:role/lambda-role");
    expect(sent.MemorySize).toBe(128);
    expect(sent.Timeout).toBe(3);
  });
});

describe("Lambda function detail edit/test/tags", () => {
  const name = "my-fn";
  const arn = "arn:aws:lambda:us-east-1:123456789012:function:my-fn";
  function getFunction(overrides: Record<string, unknown> = {}) {
    return jsonResponse({
      Configuration: {
        FunctionName: name,
        FunctionArn: arn,
        Runtime: "nodejs20.x",
        Role: "arn:aws:iam::123456789012:role/lambda-role",
        Handler: "index.handler",
        MemorySize: 128,
        Timeout: 3,
        Description: "",
        State: "Active",
        LastUpdateStatus: "Successful",
        PackageType: "Zip",
        Architectures: ["x86_64"],
        ...overrides,
      },
      Code: { RepositoryType: "S3" },
      Tags: { team: "platform" },
    });
  }

  it("updates configuration through PUT /2015-03-31/functions/{name}/configuration", async () => {
    let sentBody: string | null = null;
    installFetch([
      {
        when: (url, init) => url === `/2015-03-31/functions/${name}` && (init?.method ?? "GET") === "GET",
        respond: () => getFunction(),
      },
      {
        when: (url, init) => url === `/2015-03-31/functions/${name}/configuration` && init?.method === "PUT",
        respond: (init) => {
          sentBody = bodyOf(init);
          return jsonResponse({ FunctionName: name });
        },
      },
    ]);
    renderDetail(<LambdaFunctionDetailPage />, "name", name);
    await screen.findByTestId("lambda-function-summary");

    fireEvent.click(screen.getByTestId("lambda-function-edit"));
    const dialog = await screen.findByRole("dialog", { name: "Edit basic settings" });
    fireEvent.change(within(dialog).getByTestId("lambda-edit-config-memory"), { target: { value: "512" } });
    fireEvent.change(within(dialog).getByTestId("lambda-edit-config-timeout"), { target: { value: "30" } });
    fireEvent.change(within(dialog).getByTestId("lambda-edit-config-description"), { target: { value: "updated" } });
    fireEvent.click(within(dialog).getByTestId("lambda-edit-config-save"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    const sent = JSON.parse(sentBody!) as Record<string, unknown>;
    expect(sent.MemorySize).toBe(512);
    expect(sent.Timeout).toBe(30);
    expect(sent.Description).toBe("updated");
    expect(sent.Environment).toEqual({ Variables: {} });
  });

  it("invokes the function through POST …/invocations and shows the response payload", async () => {
    installFetch([
      {
        when: (url, init) => url === `/2015-03-31/functions/${name}` && (init?.method ?? "GET") === "GET",
        respond: () => getFunction(),
      },
      {
        when: (url, init) => url === `/2015-03-31/functions/${name}/invocations` && init?.method === "POST",
        respond: () => new Response(JSON.stringify({ ok: true }), { status: 200 }),
      },
    ]);
    renderDetail(<LambdaFunctionDetailPage />, "name", name);
    await screen.findByTestId("lambda-function-summary");

    fireEvent.click(screen.getByTestId("lambda-function-test"));
    const dialog = await screen.findByRole("dialog", { name: `Test ${name}` });
    fireEvent.click(within(dialog).getByTestId("lambda-test-invoke"));
    const result = await within(dialog).findByTestId("lambda-test-result");
    expect(result.textContent).toContain("Succeeded");
    expect((within(dialog).getByLabelText("Response payload") as HTMLTextAreaElement).value).toContain("ok");
  });

  it("adds a tag through TagResource on the function ARN", async () => {
    let sentBody: string | null = null;
    installFetch([
      {
        when: (url, init) => url === `/2015-03-31/functions/${name}` && (init?.method ?? "GET") === "GET",
        respond: () => getFunction(),
      },
      {
        when: (url, init) => url.startsWith("/2017-03-31/tags/") && init?.method === "POST",
        respond: (init) => {
          sentBody = bodyOf(init);
          return new Response(null, { status: 204 });
        },
      },
    ]);
    renderDetail(<LambdaFunctionDetailPage />, "name", name);
    await screen.findByTestId("lambda-function-summary");

    fireEvent.click(screen.getByRole("tab", { name: "Tags" }));
    fireEvent.click(screen.getByTestId("lambda-function-manage-tags"));
    const dialog = await screen.findByRole("dialog", { name: "Manage tags" });
    // The existing tag pre-populates row 0; add a second row.
    fireEvent.click(within(dialog).getByRole("button", { name: "Add tag" }));
    fireEvent.change(within(dialog).getByTestId("lambda-function-tag-key-1"), { target: { value: "env" } });
    fireEvent.change(within(dialog).getByTestId("lambda-function-tag-value-1"), { target: { value: "prod" } });
    fireEvent.click(within(dialog).getByTestId("lambda-function-tags-save"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    const sent = JSON.parse(sentBody!) as { Tags: Record<string, string> };
    expect(sent.Tags).toEqual({ team: "platform", env: "prod" });
  });
});

describe("S3 bucket versioning and tagging", () => {
  const name = "my-bucket";
  function installReads(extra: Rule[]) {
    installFetch([
      {
        when: (url, init) => url === "/" && (init?.method ?? "GET") === "GET",
        respond: () =>
          xmlResponse(
            `<ListAllMyBucketsResult><Buckets><Bucket><Name>${name}</Name><CreationDate>2026-01-01T00:00:00.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`,
          ),
      },
      { when: (url) => url === `/${name}?location`, respond: () => xmlResponse(`<LocationConstraint/>`) },
      { when: (url) => url === `/${name}?list-type=2`, respond: () => xmlResponse(`<ListBucketResult></ListBucketResult>`) },
      ...extra,
    ]);
  }

  it("enables versioning through PUT /{bucket}?versioning", async () => {
    let sentBody: string | null = null;
    installReads([
      {
        when: (url, init) => url === `/${name}?versioning` && (init?.method ?? "GET") === "GET",
        respond: () => xmlResponse(`<VersioningConfiguration/>`),
      },
      { when: (url) => url === `/${name}?tagging`, respond: () => xmlResponse(`<Error><Code>NoSuchTagSet</Code></Error>`, 404) },
      {
        when: (url, init) => url === `/${name}?versioning` && init?.method === "PUT",
        respond: (init) => {
          sentBody = bodyOf(init);
          return new Response(null, { status: 200 });
        },
      },
    ]);
    renderDetail(<S3BucketDetailPage />, "name", name);
    await screen.findByTestId("s3-bucket-summary");

    fireEvent.click(screen.getByTestId("s3-bucket-edit-versioning"));
    const dialog = await screen.findByRole("dialog", { name: "Edit bucket versioning" });
    fireEvent.click(within(dialog).getByRole("checkbox"));
    fireEvent.click(within(dialog).getByTestId("s3-versioning-save"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(sentBody).toContain("<Status>Enabled</Status>");
  });

  it("replaces the tag set through PUT /{bucket}?tagging", async () => {
    let sentBody: string | null = null;
    installReads([
      {
        when: (url, init) => url === `/${name}?versioning` && (init?.method ?? "GET") === "GET",
        respond: () => xmlResponse(`<VersioningConfiguration/>`),
      },
      {
        when: (url, init) => url === `/${name}?tagging` && (init?.method ?? "GET") === "GET",
        respond: () => xmlResponse(`<Error><Code>NoSuchTagSet</Code></Error>`, 404),
      },
      {
        when: (url, init) => url === `/${name}?tagging` && init?.method === "PUT",
        respond: (init) => {
          sentBody = bodyOf(init);
          return new Response(null, { status: 204 });
        },
      },
    ]);
    renderDetail(<S3BucketDetailPage />, "name", name);
    await screen.findByTestId("s3-bucket-summary");

    fireEvent.click(screen.getByTestId("s3-bucket-manage-tags"));
    const dialog = await screen.findByRole("dialog", { name: "Manage bucket tags" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add tag" }));
    fireEvent.change(within(dialog).getByTestId("s3-bucket-tag-key-0"), { target: { value: "owner" } });
    fireEvent.change(within(dialog).getByTestId("s3-bucket-tag-value-0"), { target: { value: "team" } });
    fireEvent.click(within(dialog).getByTestId("s3-bucket-tags-save"));

    await waitFor(() => expect(sentBody).not.toBeNull());
    expect(sentBody).toContain("<Key>owner</Key><Value>team</Value>");
  });
});

// The retention, ECR settings, and ECS run-task flows drive their operations
// through a Cloudscape Select, which does not open its option listbox under
// jsdom's synthetic events. Their request shaping — the contract that actually
// matters — is pinned directly against the exported api.ts functions, the same
// mocked federated fetch the page tests use. The structural + focus + axe
// coverage of those dialogs lives in the Playwright e2e suite, which drives a
// real browser where the Select opens.
describe("api.ts request shaping", () => {
  it("PutRetentionPolicy sets a fixed retention; DeleteRetentionPolicy clears it", async () => {
    const calls: { target: string; body: string }[] = [];
    installFetch([
      {
        when: (_u, init) => targetOf(init).startsWith("Logs_20140328."),
        respond: (init) => {
          calls.push({ target: targetOf(init), body: bodyOf(init) });
          return jsonResponse({});
        },
      },
    ]);
    const { putCWLogGroupRetention } = await import("../api.js");
    await putCWLogGroupRetention("/ecs/my-task", 30);
    await putCWLogGroupRetention("/ecs/my-task", 0);
    expect(calls[0].target).toBe("Logs_20140328.PutRetentionPolicy");
    expect(JSON.parse(calls[0].body)).toEqual({ logGroupName: "/ecs/my-task", retentionInDays: 30 });
    expect(calls[1].target).toBe("Logs_20140328.DeleteRetentionPolicy");
    expect(JSON.parse(calls[1].body)).toEqual({ logGroupName: "/ecs/my-task" });
  });

  it("ECR PutImageTagMutability and PutImageScanningConfiguration send the real bodies", async () => {
    const calls: { target: string; body: string }[] = [];
    installFetch([
      {
        when: (_u, init) => targetOf(init).startsWith("AmazonEC2ContainerRegistry_V20150921."),
        respond: (init) => {
          calls.push({ target: targetOf(init), body: bodyOf(init) });
          return jsonResponse({});
        },
      },
    ]);
    const { putECRImageTagMutability, putECRImageScanningConfiguration } = await import("../api.js");
    await putECRImageTagMutability("my-repo", "IMMUTABLE");
    await putECRImageScanningConfiguration("my-repo", true);
    expect(JSON.parse(calls[0].body)).toEqual({ repositoryName: "my-repo", imageTagMutability: "IMMUTABLE" });
    expect(JSON.parse(calls[1].body)).toEqual({
      repositoryName: "my-repo",
      imageScanningConfiguration: { scanOnPush: true },
    });
  });

  it("ECR TagResource/UntagResource carry the ARN, tag pairs, and keys", async () => {
    const calls: { target: string; body: string }[] = [];
    installFetch([
      {
        when: (_u, init) => targetOf(init).startsWith("AmazonEC2ContainerRegistry_V20150921."),
        respond: (init) => {
          calls.push({ target: targetOf(init), body: bodyOf(init) });
          return jsonResponse({});
        },
      },
    ]);
    const arn = "arn:aws:ecr:us-east-1:123456789012:repository/my-repo";
    const { tagECRResource, untagECRResource } = await import("../api.js");
    await tagECRResource(arn, { team: "platform" });
    await untagECRResource(arn, ["stale"]);
    expect(JSON.parse(calls[0].body)).toEqual({ resourceArn: arn, tags: [{ Key: "team", Value: "platform" }] });
    expect(JSON.parse(calls[1].body)).toEqual({ resourceArn: arn, tagKeys: ["stale"] });
  });

  it("RegisterTaskDefinition + RunTask send a minimal EC2 task definition and run it", async () => {
    const calls: { target: string; body: string }[] = [];
    installFetch([
      {
        when: (_u, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition",
        respond: (init) => {
          calls.push({ target: targetOf(init), body: bodyOf(init) });
          return jsonResponse({ taskDefinition: { family: "web", revision: 2 } });
        },
      },
      {
        when: (_u, init) => targetOf(init) === "AmazonEC2ContainerServiceV20141113.RunTask",
        respond: (init) => {
          calls.push({ target: targetOf(init), body: bodyOf(init) });
          return jsonResponse({ tasks: [{ taskArn: "arn:aws:ecs:us-east-1:123456789012:task/default/abc" }] });
        },
      },
    ]);
    const { registerECSTaskDefinition, runECSTask } = await import("../api.js");
    const ref = await registerECSTaskDefinition({ family: "web", image: "nginx:latest", cpu: "", memory: "", launchType: "EC2" });
    expect(ref).toBe("web:2");
    const registered = JSON.parse(calls[0].body) as Record<string, unknown>;
    expect(registered.family).toBe("web");
    expect(registered.requiresCompatibilities).toEqual(["EC2"]);
    expect((registered.containerDefinitions as { image: string }[])[0].image).toBe("nginx:latest");

    const arns = await runECSTask({ cluster: "default", taskDefinition: ref, launchType: "EC2", count: 1 });
    expect(arns).toEqual(["arn:aws:ecs:us-east-1:123456789012:task/default/abc"]);
    expect(JSON.parse(calls[1].body)).toEqual({ cluster: "default", taskDefinition: "web:2", launchType: "EC2", count: 1 });
  });

  it("Lambda UntagResource lists the tag keys as query parameters", async () => {
    let calledUrl = "";
    installFetch([
      {
        when: (url, init) => url.startsWith("/2017-03-31/tags/") && init?.method === "DELETE",
        respond: () => new Response(null, { status: 204 }),
      },
    ]);
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calledUrl = url;
      expect(init?.method).toBe("DELETE");
      return new Response(null, { status: 204 });
    });
    const { untagLambdaResource } = await import("../api.js");
    await untagLambdaResource("arn:aws:lambda:us-east-1:123456789012:function:my-fn", ["a", "b"]);
    expect(calledUrl).toContain("tagKeys=a");
    expect(calledUrl).toContain("tagKeys=b");
  });

  it("GetBucketTagging reads NoSuchTagSet as an empty tag set", async () => {
    installFetch([
      {
        when: (url) => url === "/my-bucket?tagging",
        respond: () => xmlResponse(`<Error><Code>NoSuchTagSet</Code><Message>none</Message></Error>`, 404),
      },
    ]);
    const { fetchS3BucketTagging } = await import("../api.js");
    await expect(fetchS3BucketTagging("my-bucket")).resolves.toEqual({});
  });
});
