import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExecutionsTable, executionState } from "../pages/CloudRunJobDetailPage.js";
import { ImagesTable } from "../pages/ARRepoDetailPage.js";
import { ObjectsTable } from "../pages/GCSBucketDetailPage.js";
import { LogEntryDetailDialog } from "../pages/LoggingPage.js";
import { formatBytes, splitImageDigest } from "../console/format.js";

afterEach(cleanup);

const REPO = "projects/sockerless/locations/us-central1/repositories/runner-images";

describe("executionState", () => {
  it("reads the execution's own Completed condition, like the job's terminalCondition", () => {
    expect(
      executionState({
        name: "e",
        conditions: [{ type: "Completed", state: "CONDITION_SUCCEEDED" }],
      }),
    ).toBe("Succeeded");
    expect(
      executionState({
        name: "e",
        conditions: [{ type: "Completed", state: "CONDITION_FAILED", message: "task 0 exited 1" }],
      }),
    ).toBe("Failed: task 0 exited 1");
  });

  it("falls back to task counts when no Completed condition has landed yet", () => {
    expect(executionState({ name: "e", runningCount: 1, taskCount: 1 })).toBe("Running");
    expect(executionState({ name: "e", failedCount: 1, taskCount: 1 })).toBe("Failed");
    expect(executionState({ name: "e", succeededCount: 1, taskCount: 1 })).toBe("Succeeded");
  });
});

describe("ExecutionsTable", () => {
  it("lists each execution's status, task tally and timestamps", () => {
    render(
      <ExecutionsTable
        executions={[
          {
            name: `${REPO}/executions/run-abc12`,
            createTime: "2026-07-20T00:00:00Z",
            completionTime: "2026-07-20T00:05:00Z",
            succeededCount: 3,
            taskCount: 3,
            conditions: [{ type: "Completed", state: "CONDITION_SUCCEEDED" }],
          },
        ]}
      />,
    );
    expect(screen.getByText("run-abc12")).toBeDefined();
    expect(screen.getByText("Succeeded")).toBeDefined();
    expect(screen.getByText(/3\/3 succeeded/)).toBeDefined();
  });

  it("explains an empty execution list instead of rendering a blank", () => {
    render(<ExecutionsTable executions={[]} />);
    expect(screen.getByText("No executions yet")).toBeDefined();
  });
});

describe("splitImageDigest", () => {
  it("splits the repository-relative image path from its content digest", () => {
    expect(splitImageDigest(`${REPO}/dockerImages/runner@sha256:abcd1234`)).toEqual({
      path: "runner",
      digest: "sha256:abcd1234",
    });
  });

  it("keeps multi-segment image paths intact", () => {
    expect(splitImageDigest(`${REPO}/dockerImages/org/runner@sha256:abcd1234`)).toEqual({
      path: "org/runner",
      digest: "sha256:abcd1234",
    });
  });
});

describe("ImagesTable", () => {
  it("lists each image's path, digest and tags", () => {
    render(
      <ImagesTable
        images={[
          {
            name: `${REPO}/dockerImages/runner@sha256:${"a".repeat(64)}`,
            tags: ["latest", "v1"],
            uploadTime: "2026-07-20T00:00:00Z",
          },
        ]}
      />,
    );
    expect(screen.getByText("runner")).toBeDefined();
    expect(screen.getByText("latest, v1")).toBeDefined();
  });

  it("explains an empty image list instead of rendering a blank", () => {
    render(<ImagesTable images={[]} />);
    expect(screen.getByText("No images")).toBeDefined();
  });
});

describe("formatBytes", () => {
  it("renders a human-readable size from the API's decimal-string byte count", () => {
    expect(formatBytes("512")).toBe("512 B");
    expect(formatBytes("2048")).toBe("2.0 KB");
    expect(formatBytes("5242880")).toBe("5.0 MB");
  });

  it("reports missing sizes as an em dash rather than blank or NaN", () => {
    expect(formatBytes(undefined)).toBe("—");
    expect(formatBytes("")).toBe("—");
  });
});

describe("ObjectsTable", () => {
  it("lists each object's size and content type", () => {
    render(
      <ObjectsTable
        objects={[
          {
            name: "artifacts/build.tar.gz",
            bucket: "runner-artifacts",
            size: "1048576",
            contentType: "application/gzip",
            timeCreated: "2026-07-20T00:00:00Z",
          },
        ]}
      />,
    );
    expect(screen.getByText("artifacts/build.tar.gz")).toBeDefined();
    expect(screen.getByText("1.0 MB")).toBeDefined();
    expect(screen.getByText("application/gzip")).toBeDefined();
  });

  it("explains an empty bucket instead of rendering a blank", () => {
    render(<ObjectsTable objects={[]} />);
    expect(screen.getByText("This bucket has no objects")).toBeDefined();
  });
});

describe("LogEntryDetailDialog", () => {
  it("expands a text-payload entry's full detail", () => {
    const onClose = vi.fn();
    render(
      <LogEntryDetailDialog
        entry={{
          logName: "projects/sockerless/logs/run.googleapis.com%2Fstdout",
          timestamp: "2026-07-20T00:00:00Z",
          severity: "ERROR",
          textPayload: "container exited with code 1",
          insertId: "abc123",
          resource: { type: "cloud_run_job", labels: { job_name: "runner" } },
        }}
        onClose={onClose}
      />,
    );
    expect(screen.getByText("container exited with code 1")).toBeDefined();
    expect(screen.getByText("abc123")).toBeDefined();
    expect(screen.getByText("cloud_run_job")).toBeDefined();
    expect(screen.getByText("runner")).toBeDefined();
    fireEvent.click(screen.getByText("Close"));
    expect(onClose).toHaveBeenCalled();
  });

  it("pretty-prints a JSON-payload entry", () => {
    render(
      <LogEntryDetailDialog
        entry={{
          logName: "projects/sockerless/logs/structured",
          timestamp: "2026-07-20T00:00:00Z",
          jsonPayload: { message: "build finished", exitCode: 0 },
        }}
        onClose={() => {}}
      />,
    );
    const payload = screen.getByTestId("log-entry-payload");
    expect(payload.textContent).toContain("\"message\": \"build finished\"");
  });
});
