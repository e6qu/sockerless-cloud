import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { TagsEditor } from "../portal/index.js";
import { ACRRegistryUpdateForm } from "../pages/ACRRegistryDetailPage.js";
import { StorageAccountConfigForm } from "../pages/StorageAccountDetailPage.js";
import { FunctionAppCreateForm, isValidFunctionAppName } from "../pages/AzureFunctionsPage.js";
import {
  ContainerAppJobCreateForm,
  ContainerAppJobEditForm,
  isValidContainerAppJobName,
} from "../pages/ContainerAppJobForms.js";
import type { ACRRegistryDetail, ContainerAppJobDetail, StorageAccountDetail, Subscription } from "../api.js";

/**
 * Behaviour tests for every new UPDATE/CREATE form surface — the reusable tags
 * editor, the storage/ACR config forms, and the Container Apps job + Function
 * App create/edit forms. They run the real Fluent components under jsdom (see
 * test-setup.ts's tabster/NodeFilter polyfill), the same way
 * ACRRegistryCreateForm.test.tsx exercises the create form.
 */

afterEach(cleanup);

const SUBSCRIPTIONS: Subscription[] = [
  { id: "/subscriptions/sub-1", subscriptionId: "sub-1", displayName: "Simulator", state: "Enabled" },
];

describe("TagsEditor", () => {
  it("seeds a row per existing tag and collects the edited map on save, dropping empty names", () => {
    const onSave = vi.fn();
    render(<TagsEditor tags={{ env: "prod" }} busy={false} testidPrefix="t" onSave={onSave} onDismiss={() => {}} />);
    expect((screen.getByTestId("t-key-0") as HTMLInputElement).value).toBe("env");
    expect((screen.getByTestId("t-value-0") as HTMLInputElement).value).toBe("prod");

    fireEvent.click(screen.getByTestId("t-add"));
    fireEvent.change(screen.getByTestId("t-key-1"), { target: { value: "team" } });
    fireEvent.change(screen.getByTestId("t-value-1"), { target: { value: "core" } });
    // A blank-name row must be dropped, not sent as "".
    fireEvent.click(screen.getByTestId("t-add"));

    fireEvent.submit(screen.getByTestId("t-form"));
    expect(onSave).toHaveBeenCalledWith({ env: "prod", team: "core" });
  });

  it("removes a tag row", () => {
    const onSave = vi.fn();
    render(<TagsEditor tags={{ a: "1", b: "2" }} busy={false} testidPrefix="t" onSave={onSave} onDismiss={() => {}} />);
    fireEvent.click(screen.getByTestId("t-remove-0"));
    fireEvent.submit(screen.getByTestId("t-form"));
    expect(onSave).toHaveBeenCalledWith({ b: "2" });
  });

  it("seeds one empty row for a resource with no tags", () => {
    render(<TagsEditor tags={{}} busy={false} testidPrefix="t" onSave={() => {}} onDismiss={() => {}} />);
    expect((screen.getByTestId("t-key-0") as HTMLInputElement).value).toBe("");
  });
});

describe("ACRRegistryUpdateForm", () => {
  const registry: ACRRegistryDetail = {
    id: "/reg",
    name: "reg",
    location: "eastus",
    tags: {},
    loginServer: "reg.azurecr.io",
    skuName: "Basic",
    skuTier: "Basic",
    adminUserEnabled: false,
    provisioningState: "Succeeded",
  };

  it("submits the edited SKU and admin-user toggle", () => {
    const onSave = vi.fn();
    render(<ACRRegistryUpdateForm registry={registry} busy={false} onSave={onSave} onDismiss={() => {}} />);
    fireEvent.change(screen.getByTestId("acr-update-sku"), { target: { value: "Premium" } });
    fireEvent.click(screen.getByRole("switch"));
    fireEvent.submit(screen.getByTestId("acr-update-form"));
    expect(onSave).toHaveBeenCalledWith("Premium", true);
  });
});

describe("StorageAccountConfigForm", () => {
  const account: StorageAccountDetail = {
    id: "/acct",
    name: "acct",
    location: "eastus",
    kind: "StorageV2",
    tags: {},
    skuName: "Standard_LRS",
    provisioningState: "Succeeded",
    accessTier: "Hot",
    blobEndpoint: "",
  };

  it("submits the edited redundancy and access tier", () => {
    const onSave = vi.fn();
    render(<StorageAccountConfigForm account={account} busy={false} onSave={onSave} onDismiss={() => {}} />);
    fireEvent.change(screen.getByTestId("storage-config-sku"), { target: { value: "Standard_GRS" } });
    fireEvent.change(screen.getByTestId("storage-config-tier"), { target: { value: "Cool" } });
    fireEvent.submit(screen.getByTestId("storage-config-form"));
    expect(onSave).toHaveBeenCalledWith("Standard_GRS", "Cool");
  });
});

describe("Function App name validation", () => {
  it("accepts a valid name and rejects out-of-range or bad-character names", () => {
    expect(isValidFunctionAppName("my-func-app1")).toBe(true);
    expect(isValidFunctionAppName("a")).toBe(false);
    expect(isValidFunctionAppName("-leading")).toBe(false);
    expect(isValidFunctionAppName("has space")).toBe(false);
  });
});

describe("FunctionAppCreateForm", () => {
  it("keeps Create disabled until the name is valid, then submits the collected input", () => {
    const onCreate = vi.fn();
    render(<FunctionAppCreateForm subscriptions={SUBSCRIPTIONS} busy={false} onCreate={onCreate} onDismiss={() => {}} />);
    const submit = () => screen.getByTestId("fn-create-submit") as HTMLButtonElement;
    expect(submit().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("fn-create-name"), { target: { value: "myfuncapp1" } });
    fireEvent.change(screen.getByTestId("fn-create-runtime"), { target: { value: "python" } });
    expect(submit().disabled).toBe(false);
    fireEvent.submit(screen.getByTestId("fn-create-form"));
    expect(onCreate).toHaveBeenCalledWith({
      subscriptionId: "sub-1",
      resourceGroup: "sockerless-console",
      name: "myfuncapp1",
      location: "eastus",
      runtime: "python",
      planName: "sockerless-plan",
    });
  });
});

describe("Container Apps job name validation", () => {
  it("accepts a valid name and rejects uppercase, too-short, or bad-start names", () => {
    expect(isValidContainerAppJobName("my-job1")).toBe(true);
    expect(isValidContainerAppJobName("MyJob")).toBe(false);
    expect(isValidContainerAppJobName("1job")).toBe(false);
    expect(isValidContainerAppJobName("a")).toBe(false);
  });
});

describe("ContainerAppJobCreateForm", () => {
  it("keeps Create disabled until name and image are valid, then submits the whole job input", () => {
    const onCreate = vi.fn();
    render(<ContainerAppJobCreateForm subscriptions={SUBSCRIPTIONS} busy={false} onCreate={onCreate} onDismiss={() => {}} />);
    const submit = () => screen.getByTestId("ca-job-create-submit") as HTMLButtonElement;
    expect(submit().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("ca-job-create-name"), { target: { value: "myjob1" } });
    // Name alone is not enough — the container image is required too.
    expect(submit().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("ca-job-create-image"), { target: { value: "alpine:3.20" } });
    fireEvent.change(screen.getByTestId("ca-job-create-timeout"), { target: { value: "600" } });
    fireEvent.change(screen.getByTestId("ca-job-create-parallelism"), { target: { value: "2" } });
    fireEvent.change(screen.getByTestId("ca-job-create-command"), { target: { value: "/bin/run now" } });
    // Add one environment variable.
    fireEvent.click(screen.getByTestId("ca-job-create-env-add"));
    fireEvent.change(screen.getByTestId("ca-job-create-env-name-0"), { target: { value: "MODE" } });
    fireEvent.change(screen.getByTestId("ca-job-create-env-value-0"), { target: { value: "batch" } });
    expect(submit().disabled).toBe(false);
    fireEvent.submit(screen.getByTestId("ca-job-create-form"));
    expect(onCreate).toHaveBeenCalledWith({
      subscriptionId: "sub-1",
      resourceGroup: "sockerless-console",
      name: "myjob1",
      location: "eastus",
      environmentName: "sockerless-env",
      config: {
        triggerType: "Manual",
        replicaTimeout: 600,
        replicaRetryLimit: 0,
        parallelism: 2,
        cronExpression: "",
        containers: [
          {
            name: "myjob1",
            image: "alpine:3.20",
            command: ["/bin/run", "now"],
            args: [],
            env: [{ name: "MODE", value: "batch" }],
          },
        ],
      },
    });
  });
});

describe("ContainerAppJobEditForm", () => {
  const job: ContainerAppJobDetail = {
    id: "/job",
    name: "job1",
    location: "eastus",
    tags: {},
    provisioningState: "Succeeded",
    environmentId: "/env",
    triggerType: "Schedule",
    replicaTimeout: 300,
    replicaRetryLimit: 3,
    parallelism: 1,
    cronExpression: "0 * * * *",
    containers: [{ name: "worker", image: "old:1", command: ["run"], args: ["--x"], env: [{ name: "A", value: "1" }] }],
  };

  it("preserves trigger type, retry limit, cron, and container name/command while editing timeout/parallelism/image/env", () => {
    const onSave = vi.fn();
    render(<ContainerAppJobEditForm job={job} busy={false} onSave={onSave} onDismiss={() => {}} />);
    expect((screen.getByTestId("ca-job-edit-timeout") as HTMLInputElement).value).toBe("300");
    expect((screen.getByTestId("ca-job-edit-image-0") as HTMLInputElement).value).toBe("old:1");
    fireEvent.change(screen.getByTestId("ca-job-edit-image-0"), { target: { value: "new:2" } });
    fireEvent.change(screen.getByTestId("ca-job-edit-parallelism"), { target: { value: "5" } });
    fireEvent.change(screen.getByTestId("ca-job-edit-0-env-value-0"), { target: { value: "2" } });
    fireEvent.submit(screen.getByTestId("ca-job-edit-form"));
    expect(onSave).toHaveBeenCalledWith({
      triggerType: "Schedule",
      replicaTimeout: 300,
      replicaRetryLimit: 3,
      parallelism: 5,
      cronExpression: "0 * * * *",
      containers: [{ name: "worker", image: "new:2", command: ["run"], args: ["--x"], env: [{ name: "A", value: "2" }] }],
    });
  });
});
