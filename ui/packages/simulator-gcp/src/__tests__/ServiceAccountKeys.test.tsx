import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ServiceAccountKeysTable } from "../pages/ServiceAccountDetailPage.js";
import { ServiceAccountKeyMintedDialog, gcloudUsage } from "../pages/ServiceAccountKeyMintedDialog.js";
import { CreateServiceAccountDialog } from "../pages/ServiceAccountsPage.js";
import { ProjectProvider } from "../console/project.js";

// Without vitest globals, Testing Library's automatic between-test cleanup
// never registers; unmount explicitly so screen queries see one render.
afterEach(cleanup);

const SA_NAME = "projects/sockerless/serviceAccounts/runner@sockerless.iam.gserviceaccount.com";

const CREDENTIALS = JSON.stringify({
  type: "service_account",
  project_id: "sockerless",
  private_key_id: "aaaa1111-bbbb-2222-cccc-333344445555",
  client_email: "runner@sockerless.iam.gserviceaccount.com",
});

describe("ServiceAccountKeysTable", () => {
  it("lists each key's id and dates and wires per-key deletion", () => {
    const onDelete = vi.fn();
    render(
      <ServiceAccountKeysTable
        keys={[
          {
            name: `${SA_NAME}/keys/key-one`,
            validAfterTime: "2026-07-24T00:00:00Z",
            validBeforeTime: "2036-07-24T00:00:00Z",
          },
          { name: `${SA_NAME}/keys/key-two` },
        ]}
        onDelete={onDelete}
      />,
    );
    expect(screen.getByText("key-one")).toBeDefined();
    expect(screen.getByText("key-two")).toBeDefined();
    fireEvent.click(screen.getByTestId("sa-key-delete-key-two"));
    expect(onDelete).toHaveBeenCalledWith("key-two");
  });

  it("explains an empty key list instead of rendering a blank", () => {
    render(<ServiceAccountKeysTable keys={[]} onDelete={() => {}} />);
    expect(screen.getByText("No keys")).toBeDefined();
  });
});

describe("ServiceAccountKeyMintedDialog", () => {
  const mintedKey = {
    name: `${SA_NAME}/keys/aaaa1111-bbbb-2222-cccc-333344445555`,
    privateKeyData: btoa(CREDENTIALS),
  };

  it("offers the decoded credential file once, under the console's filename", () => {
    render(
      <ServiceAccountKeyMintedDialog
        project="sockerless"
        saKey={mintedKey}
        endpoint="http://localhost:4567"
        onClose={() => {}}
        autoDownload={false}
      />,
    );
    const download = screen.getByTestId("sa-key-download");
    expect(download.getAttribute("download")).toBe("sockerless-aaaa1111bbbb.json");
    const href = download.getAttribute("href")!;
    expect(decodeURIComponent(href.slice(href.indexOf(",") + 1))).toBe(CREDENTIALS);
  });

  it("warns that the key can't be downloaded again", () => {
    render(
      <ServiceAccountKeyMintedDialog
        project="sockerless"
        saKey={mintedKey}
        endpoint="http://localhost:4567"
        onClose={() => {}}
        autoDownload={false}
      />,
    );
    expect(screen.getByText(/the key can't be downloaded again/i)).toBeDefined();
  });

  it("prints the proven gcloud usage against the console's cloud coordinate", () => {
    render(
      <ServiceAccountKeyMintedDialog
        project="sockerless"
        saKey={mintedKey}
        endpoint="http://localhost:4567"
        onClose={() => {}}
        autoDownload={false}
      />,
    );
    const cli = screen.getByTestId("sa-key-cli").textContent!;
    expect(cli).toContain("gcloud config set api_endpoint_overrides/iam http://localhost:4567/");
    expect(cli).toContain("gcloud config set auth/token_host http://localhost:4567/token");
    expect(cli).toContain("gcloud auth activate-service-account --key-file=sockerless-aaaa1111bbbb.json");
    expect(cli).toContain("gcloud iam service-accounts list");
  });

  it("has nothing to offer for a key without private material", () => {
    render(
      <ServiceAccountKeyMintedDialog
        project="sockerless"
        saKey={{ name: `${SA_NAME}/keys/later` }}
        endpoint="http://localhost:4567"
        onClose={() => {}}
        autoDownload={false}
      />,
    );
    expect(screen.queryByTestId("sa-key-download")).toBeNull();
    expect(screen.getByText(/can't be downloaded again/i)).toBeDefined();
  });
});

describe("gcloudUsage", () => {
  it("substitutes the endpoint coordinate into every line that needs it", () => {
    const usage = gcloudUsage("https://gcp.example.test", "sockerless-abc.json");
    expect(usage.split("\n")).toEqual([
      "gcloud config set api_endpoint_overrides/iam https://gcp.example.test/",
      "gcloud config set auth/token_host https://gcp.example.test/token",
      "gcloud auth activate-service-account --key-file=sockerless-abc.json",
      "gcloud iam service-accounts list",
    ]);
  });
});

describe("CreateServiceAccountDialog", () => {
  function renderDialog() {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    return render(
      <QueryClientProvider client={queryClient}>
        <ProjectProvider>
          <CreateServiceAccountDialog onClose={() => {}} onCreated={() => {}} />
        </ProjectProvider>
      </QueryClientProvider>,
    );
  }

  it("derives the account id and email from the typed name, like the real console", () => {
    renderDialog();
    fireEvent.change(screen.getByTestId("sa-create-name"), { target: { value: "My Runner SA" } });
    expect((screen.getByTestId("sa-create-id") as HTMLInputElement).value).toBe("my-runner-sa");
    expect(screen.getByTestId("sa-create-email").textContent).toContain(
      "my-runner-sa@sockerless.iam.gserviceaccount.com",
    );
  });

  it("blocks creation until the account id satisfies the IAM contract", () => {
    renderDialog();
    const submit = screen.getByTestId("sa-create-submit") as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByTestId("sa-create-id"), { target: { value: "ok-id" } });
    expect(submit.disabled).toBe(true); // 5 characters — one short of the minimum
    fireEvent.change(screen.getByTestId("sa-create-id"), { target: { value: "valid-runner" } });
    expect(submit.disabled).toBe(false);
  });
});
