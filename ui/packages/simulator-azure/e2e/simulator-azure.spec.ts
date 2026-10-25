import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { CLOUD, FEDERATION, PORT, TENANT } from "./federation";

// Every test signs in the way an operator does: the console redirects to
// Microsoft Entra ID and back, its federation broker exchanges that assertion
// for Azure tokens through the managed identity's federated credential, and
// the console reads Azure Resource Manager, Microsoft Graph and the data
// planes at the cloud's coordinates. Nothing here reaches the cloud any other
// way, except the bootstrap token tests use to arrange many resources at once.

const SUBSCRIPTION = FEDERATION.subscription;

function unique(prefix: string): string {
  return `${prefix}${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

// Fluent UI's focus manager, tabster, places aria-hidden focusable sentinels
// around focus traps and arrow-key groups by design.
const TABSTER_DUMMY = "i[data-tabster-dummy]";

async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page }).exclude(TABSTER_DUMMY).analyze();
  expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
}

/** Switches the theme through the console's own Settings pane. */
async function setTheme(page: Page, theme: "Light" | "Dark") {
  await page.evaluate(() => {
    const id = "e2e-disable-transitions";
    if (document.getElementById(id)) return;
    const style = document.createElement("style");
    style.id = id;
    style.textContent = "*, *::before, *::after { transition: none !important; animation: none !important; }";
    document.head.append(style);
  });
  await page.getByRole("button", { name: "Settings" }).click();
  await page.getByRole("radio", { name: theme }).check();
  await expect.poll(() => page.evaluate(() => document.documentElement.classList.contains("dark"))).toBe(theme === "Dark");
  await page.getByRole("button", { name: "Close Portal settings" }).click();
  await expect(page.getByRole("dialog", { name: "Portal settings" })).toBeHidden();
}

const heading = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });

/** An ARM token for the seeded bootstrap application, to arrange resources. */
async function bootstrapToken(page: Page): Promise<string> {
  const response = await page.request.post(`${CLOUD}/${TENANT}/oauth2/v2.0/token`, {
    form: { grant_type: "client_credentials", client_id: "test-client-id", client_secret: "test-client-secret", scope: "https://management.azure.com/.default" },
  });
  expect(response.ok()).toBeTruthy();
  return ((await response.json()) as { access_token: string }).access_token;
}

async function armPut(page: Page, token: string, path: string, body: unknown) {
  const response = await page.request.put(`${CLOUD}${path}`, { headers: { Authorization: `Bearer ${token}` }, data: body });
  expect(response.ok(), await response.text()).toBeTruthy();
}

/** Runs a create wizard to the deployment page and waits for completion. */
async function deploy(page: Page) {
  await page.getByRole("tab", { name: "Review + create" }).click();
  await expect(page.getByTestId("validation-passed")).toBeVisible();
  await page.getByTestId("wizard-create").click();
  await expect(page).toHaveURL(/\/ui\/deployment\//);
  await expect(page.getByTestId("deployment-headline")).toHaveText("Your deployment is complete");
}

async function pickResourceGroup(page: Page, group: string) {
  await page.getByRole("combobox", { name: "Resource group" }).click();
  await page.getByRole("option", { name: group, exact: true }).click();
}

async function createGroup(page: Page, name: string) {
  await page.goto("/ui/create/Microsoft.Resources/resourceGroups");
  await page.getByRole("textbox", { name: "Resource group" }).fill(name);
  await deploy(page);
}

test.describe("Sign-in and the portal shell", () => {
  test("signs the operator in through Microsoft Entra ID and names them and the directory", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page).toHaveURL(/\/ui\/home$/);
    await expect(page).toHaveTitle("Home - Microsoft Azure");
    const account = page.getByRole("button", { name: new RegExp(`^Account manager for ${FEDERATION.operator}`) });
    await expect(account).toContainText("sockerless-test@example.com");
    await expect(account).toContainText("SIMULATOR TENANT");
    await account.click();
    const card = page.getByRole("dialog", { name: "Account" });
    await expect(card).toContainText(FEDERATION.operator);
    await expect(card).toContainText(`Directory ID ${TENANT}`);
    await expect(card.getByRole("button", { name: "Sign out" })).toBeVisible();
  });

  test("reaches Azure as the managed identity the console federates through", async ({ page }) => {
    await page.goto(`/ui/resource/subscriptions/${SUBSCRIPTION}/users`);
    const assignments = page.getByTestId("role-assignments");
    await expect(assignments).toContainText("Owner");
    await expect(assignments).toContainText(FEDERATION.identity);
    await page.goto(`/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/providers/Microsoft.ManagedIdentity/userAssignedIdentities/${FEDERATION.identity}/federated-credentials`);
    await expect(page.getByTestId("child-federated-credentials-table")).toContainText(FEDERATION.issuer);
    await expect(page.getByTestId("child-federated-credentials-table")).toContainText(FEDERATION.subject);
  });

  test("sends a visitor at the bare origin to Home", async ({ page }) => {
    await page.goto("/");
    await expect(page).toHaveURL(`http://localhost:${PORT}/ui/home`);
  });

  test("offers the portal's header controls, each inside the header", async ({ page }) => {
    await page.goto("/ui/home");
    const header = page.locator(".az-header");
    await expect(header.getByRole("button", { name: "Show portal menu" })).toBeVisible();
    await expect(header.getByRole("link", { name: "Microsoft Azure" })).toBeVisible();
    await expect(header.getByRole("combobox", { name: "Search resources, services, and docs" })).toBeVisible();
    for (const name of ["Notifications", "Settings", "Help + support"]) await expect(header.getByRole("button", { name })).toBeVisible();
    await expect(header.getByRole("button", { name: /Cloud Shell|Copilot/ })).toHaveCount(0);
    const escaped = await page.evaluate(() => {
      const box = document.querySelector(".az-header")!.getBoundingClientRect();
      return Array.from(document.querySelectorAll(".az-header button, .az-header a, .az-header input"))
        .map((control) => ({ html: control.outerHTML.slice(0, 80), rect: control.getBoundingClientRect() }))
        .filter(({ rect }) => rect.width > 0 && (rect.top < box.top - 0.5 || rect.bottom > box.bottom + 0.5))
        .map(({ html }) => html);
    });
    expect(escaped).toEqual([]);
  });

  test("paints the header in the Azure brand blue", async ({ page }) => {
    await page.goto("/ui/home");
    expect(await page.locator(".az-header").evaluate((node) => getComputedStyle(node).backgroundColor)).toBe("rgb(0, 120, 212)");
    expect(await page.locator(".azure").evaluate((node) => getComputedStyle(node).fontFamily)).toContain("Segoe UI");
  });

  test("exposes a skip link ahead of the content", async ({ page }) => {
    await page.goto("/ui/home");
    await page.locator("body").focus();
    await page.keyboard.press("Tab");
    await expect(page.locator(".sl-skip-link")).toBeFocused();
  });

  test("opens the portal menu with Create a resource, Home, All services and Favorites", async ({ page }) => {
    await page.goto("/ui/home");
    await page.getByRole("button", { name: "Show portal menu" }).click();
    const menu = page.getByRole("navigation", { name: "Portal menu" });
    await expect(menu.getByRole("button", { name: "Create a resource" })).toBeVisible();
    await expect(menu.getByRole("list", { name: "Favorites" }).getByRole("button", { name: "Storage accounts" })).toBeVisible();
    await menu.getByRole("button", { name: "All services" }).click();
    await expect(page).toHaveURL(/\/ui\/allservices$/);
    await expect(heading(page, "All services")).toBeVisible();
  });

  test("adds a service to Favorites from All services", async ({ page }) => {
    await page.goto("/ui/allservices?category=Integration");
    await expect(page.getByRole("heading", { name: "Integration" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Compute" })).toHaveCount(0);
    await page.getByRole("button", { name: "Add Service Bus to favorites" }).click();
    await page.getByRole("button", { name: "Show portal menu" }).click();
    await expect(page.getByRole("list", { name: "Favorites" }).getByRole("button", { name: "Service Bus" })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Favorites", exact: true }).click();
    await expect(page.getByRole("link", { name: /Service Bus/ })).toBeVisible();
  });

  test("filters All services by name", async ({ page }) => {
    await page.goto("/ui/allservices");
    await page.getByRole("textbox", { name: "Filter services" }).fill("vault");
    await expect(page.getByRole("link", { name: /Key vaults/ })).toBeVisible();
    await expect(page.getByRole("link", { name: /Virtual machines/ })).toHaveCount(0);
  });

  test("switches between the light and dark themes from Settings", async ({ page }) => {
    await page.goto("/ui/home");
    await setTheme(page, "Dark");
    await setTheme(page, "Light");
  });

  test("lists documentation under Help + support", async ({ page }) => {
    await page.goto("/ui/home");
    await page.getByRole("button", { name: "Help + support" }).click();
    await expect(page.getByRole("link", { name: /Azure portal documentation/ })).toHaveAttribute("href", "https://learn.microsoft.com/azure/azure-portal/");
  });

  test("searches services, resource groups and resources from the header", async ({ page }) => {
    const group = unique("search-rg-");
    await createGroup(page, group);
    const search = page.getByRole("combobox", { name: "Search resources, services, and docs" });
    await search.fill("storage");
    await expect(page.getByRole("group", { name: "Services" }).getByRole("option", { name: "Storage accounts" })).toBeVisible();
    await search.fill(group);
    const option = page.getByRole("group", { name: "Resource Groups" }).getByRole("option", { name: new RegExp(group) });
    await expect(option).toBeVisible();
    await search.press("ArrowDown");
    await search.press("Enter");
    await expect(heading(page, group)).toBeVisible();
    await page.goto("/ui/home");
    await search.fill("console-op");
    await expect(page.getByRole("group", { name: "Resources" }).getByRole("option", { name: /console-operator/ })).toBeVisible();
  });
});

// Each header panel opens on its button and closes on Escape, its open state
// and its rendered pane one and the same.
test.describe("Header panels", () => {
  for (const [button, pane] of [
    ["Notifications", "Notifications"],
    ["Settings", "Portal settings"],
    ["Help + support", "Help + support"],
  ] as const) {
    test(`${button} opens its pane and closes on Escape`, async ({ page }) => {
      await page.goto("/ui/home");
      await page.getByRole("button", { name: button, exact: true }).click();
      const dialog = page.getByRole("dialog", { name: pane });
      await expect(dialog).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();
    });
  }

  test("the account card opens on the account button and closes on Escape", async ({ page }) => {
    await page.goto("/ui/home");
    const account = page.getByRole("button", { name: /^Account manager for/ });
    await account.click();
    const card = page.getByRole("dialog", { name: "Account" });
    await expect(card).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(card).toBeHidden();
    await expect(account).toBeFocused();
  });
});

test.describe("Resource groups", () => {
  test("creates a resource group through the wizard and follows the deployment", async ({ page }) => {
    const group = unique("wizard-rg-");
    await page.goto("/ui/browse/resourcegroups");
    await expect(heading(page, "Resource groups")).toBeVisible();
    await page.getByTestId("rg-create").click();
    await expect(heading(page, "Create a resource group")).toBeVisible();
    await page.getByRole("textbox", { name: "Resource group" }).fill(group);
    await page.getByRole("tab", { name: "Tags" }).click();
    await page.getByRole("textbox", { name: "Tag 1 name" }).fill("team");
    await page.getByRole("textbox", { name: "Tag 1 value" }).fill("console");
    await deploy(page);
    await expect(page.getByTestId("deployment-operations")).toContainText(group);
    await page.getByRole("button", { name: "Notifications" }).click();
    await expect(page.getByTestId("notification").first()).toContainText("Deployment succeeded");
    await page.getByRole("button", { name: "Close Notifications" }).click();
    await page.getByTestId("deployment-go-to-resource").click();
    await expect(heading(page, group)).toBeVisible();
    await expect(page.getByRole("region", { name: "Essentials" })).toContainText("team : console");
  });

  test("refuses an invalid name and an empty one at Review + create", async ({ page }) => {
    await page.goto("/ui/create/Microsoft.Resources/resourceGroups");
    await page.getByRole("textbox", { name: "Resource group" }).fill("bad name!");
    await expect(page.getByText(/Resource group names can be up to 90 characters/)).toBeVisible();
    await page.getByRole("textbox", { name: "Resource group" }).fill("");
    await page.getByTestId("wizard-review").click();
    await expect(page.getByTestId("validation-failed")).toContainText("Basics");
    await expect(page.getByTestId("wizard-create")).toBeVisible();
  });

  test("edits tags, reads the activity log, and deletes the group after typing its name", async ({ page }) => {
    const group = unique("lifecycle-rg-");
    await createGroup(page, group);
    const base = `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/${group}`;
    await page.goto(`${base}/tags`);
    await page.getByRole("textbox", { name: "Tag 1 name" }).fill("env");
    await page.getByRole("textbox", { name: "Tag 1 value" }).fill("e2e");
    await page.getByTestId("tags-apply").click();
    await page.getByRole("navigation", { name: `${group} menu` }).getByRole("link", { name: "Overview" }).click();
    await expect(page.getByRole("region", { name: "Essentials" })).toContainText("env : e2e");

    await page.goto(`${base}/activitylog`);
    const log = page.getByTestId("activity-log");
    await expect(log).toContainText("Create or Update Resource Group");
    await expect(log).toContainText("Succeeded");

    await page.goto(`${base}/overview`);
    await page.getByTestId("resource-delete").click();
    const dialog = page.getByTestId("resource-delete-dialog");
    await expect(dialog.getByTestId("resource-delete-dialog-confirm")).toBeDisabled();
    await dialog.getByTestId("resource-delete-dialog-confirm-text").fill(group);
    await dialog.getByTestId("resource-delete-dialog-confirm").click();
    await expect(page).toHaveURL(/\/ui\/browse\/resourcegroups$/i);
    await expect(page.getByTestId("rg-table")).not.toContainText(group);
  });

  test("pages the resource group list along its nextLink", async ({ page }) => {
    const token = await bootstrapToken(page);
    const prefix = unique("paging-");
    for (let index = 0; index < 12; index += 1) {
      await armPut(page, token, `/subscriptions/${SUBSCRIPTION}/resourceGroups/${prefix}-${String(index).padStart(2, "0")}?api-version=2021-04-01`, { location: "eastus" });
    }
    await page.goto("/ui/browse/resourcegroups");
    await page.getByRole("combobox", { name: "Page size" }).click();
    await page.getByRole("option", { name: "10", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "Showing 1 to 10 records" })).toBeVisible();
    await expect(page.getByTestId("rg-table").locator("tbody tr")).toHaveCount(10);
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByText("Page 2")).toBeVisible();
    await expect(page.getByRole("status").filter({ hasText: "Showing 11 to" })).toBeVisible();
    await page.getByRole("button", { name: "Previous" }).click();
    await expect(page.getByText("Page 1")).toBeVisible();
  });

  test("selects several groups and deletes them after typing delete", async ({ page }) => {
    const token = await bootstrapToken(page);
    const prefix = unique("bulk-");
    for (const suffix of ["a", "b"]) {
      await armPut(page, token, `/subscriptions/${SUBSCRIPTION}/resourceGroups/${prefix}-${suffix}?api-version=2021-04-01`, { location: "eastus" });
    }
    await page.goto("/ui/browse/resourcegroups");
    await page.getByRole("textbox", { name: "Filter for any field" }).fill(prefix);
    await expect(page.getByTestId("rg-table").locator("tbody tr")).toHaveCount(2);
    await page.getByRole("checkbox", { name: "Select all" }).check();
    await page.getByTestId("rg-delete").click();
    const dialog = page.getByTestId("rg-delete-dialog");
    await dialog.getByTestId("rg-delete-dialog-confirm-text").fill("delete");
    await dialog.getByTestId("rg-delete-dialog-confirm").click();
    await expect(dialog).toBeHidden();
    await expect(page.getByTestId("rg-table").locator("tbody tr")).toHaveCount(0);
  });

  test("chooses the columns the list shows and resets them", async ({ page }) => {
    await page.goto("/ui/browse/resourcegroups");
    const table = page.getByTestId("rg-table");
    await expect(table.getByRole("columnheader", { name: "Location" })).toBeVisible();
    await page.getByRole("button", { name: "Manage view" }).click();
    await page.getByRole("menuitem", { name: "Edit columns" }).click();
    await page.getByRole("checkbox", { name: "Location" }).uncheck();
    await page.getByRole("button", { name: "Save" }).click();
    await expect(table.getByRole("columnheader", { name: "Location" })).toHaveCount(0);
    await page.getByRole("button", { name: "Manage view" }).click();
    await page.getByRole("menuitem", { name: "Reset to default view" }).click();
    await expect(table.getByRole("columnheader", { name: "Location" })).toBeVisible();
  });
});

test.describe("Storage accounts", () => {
  test("creates an account in a new group, then manages containers, CORS and blobs", async ({ page }) => {
    const account = unique("e2esa").slice(0, 24);
    const group = unique("storage-rg-");
    await page.goto("/ui/browse/Microsoft.Storage/storageAccounts");
    await page.getByTestId("storage-create").click();
    await expect(heading(page, "Create a storage account")).toBeVisible();
    await page.getByRole("button", { name: "Create new" }).click();
    await page.getByRole("textbox", { name: "New resource group name" }).fill(group);
    await page.getByRole("textbox", { name: "Storage account name" }).fill(account);
    await page.getByRole("tab", { name: "Advanced" }).click();
    await expect(page.getByRole("radio", { name: /Hot: Optimized/ })).toBeChecked();
    await deploy(page);
    await expect(page.getByTestId("deployment-operations")).toContainText(group);
    await expect(page.getByTestId("deployment-operations")).toContainText(account);
    await page.getByTestId("deployment-go-to-resource").click();
    await expect(heading(page, account)).toBeVisible();
    await expect(page.getByRole("region", { name: "Essentials" })).toContainText("StorageV2");

    const menu = page.getByRole("navigation", { name: `${account} menu` });
    await menu.getByRole("link", { name: "Containers" }).click();
    await page.getByTestId("container-create").click();
    await page.getByRole("textbox", { name: "Name" }).fill("reports");
    await page.getByTestId("container-submit").click();
    await expect(page.getByTestId("containers-table")).toContainText("reports");

    await page.getByRole("link", { name: "reports" }).click();
    await expect(page.getByTestId("blobs-error")).toContainText("CORS");

    await menu.getByRole("link", { name: "Resource sharing (CORS)" }).click();
    await page.getByRole("textbox", { name: "Rule 1 allowed origins" }).fill(`http://localhost:${PORT}`);
    await page.getByRole("combobox", { name: "Rule 1 allowed methods" }).click();
    for (const method of ["DELETE", "GET", "PUT"]) await page.getByRole("menuitemcheckbox", { name: method, exact: true }).click();
    await page.keyboard.press("Escape");
    await page.getByRole("textbox", { name: "Rule 1 allowed headers" }).fill("*");
    await page.getByRole("textbox", { name: "Rule 1 exposed headers" }).fill("*");
    await page.getByRole("textbox", { name: "Rule 1 max age" }).fill("300");
    await page.getByTestId("cors-save").click();
    await expect(page.getByRole("textbox", { name: "Rule 1 allowed origins" })).toHaveValue(`http://localhost:${PORT}`);

    await menu.getByRole("link", { name: "Containers" }).click();
    await page.getByRole("link", { name: "reports" }).click();
    await expect(page.getByText("No blobs found.")).toBeVisible();
    await page.getByTestId("blob-upload").click();
    await page.getByTestId("blob-file").setInputFiles({ name: "q3.csv", mimeType: "text/csv", buffer: Buffer.from("region,total\nwest,42\n") });
    await page.getByTestId("blob-upload-submit").click();
    const blobs = page.getByTestId("blobs-table");
    await expect(blobs).toContainText("q3.csv");
    await expect(blobs).toContainText("BlockBlob");

    await page.getByRole("checkbox", { name: "Select q3.csv" }).check();
    const downloadPromise = page.waitForEvent("download");
    await page.getByTestId("blob-download").click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe("q3.csv");
    const stream = await download.createReadStream();
    const chunks: Buffer[] = [];
    for await (const chunk of stream) chunks.push(chunk as Buffer);
    expect(Buffer.concat(chunks).toString()).toBe("region,total\nwest,42\n");

    await page.getByTestId("blob-delete").click();
    await page.getByTestId("blob-delete-dialog-confirm").click();
    await expect(page.getByText("No blobs found.")).toBeVisible();
  });
});

test.describe("Service Bus", () => {
  test("creates a namespace, then creates and deletes a queue", async ({ page }) => {
    const namespace = unique("e2e-sb-");
    const group = unique("sb-rg-");
    await createGroup(page, group);
    await page.goto("/ui/create/Microsoft.ServiceBus/namespaces");
    await pickResourceGroup(page, group);
    await page.getByRole("textbox", { name: "Namespace name" }).fill(namespace);
    await deploy(page);
    await page.getByTestId("deployment-go-to-resource").click();
    await expect(heading(page, namespace)).toBeVisible();
    await page.getByRole("navigation", { name: `${namespace} menu` }).getByRole("link", { name: "Queues" }).click();
    await page.getByTestId("child-queues-create").click();
    await page.getByRole("textbox", { name: "Name" }).fill("orders");
    await page.getByTestId("child-queues-submit").click();
    const table = page.getByTestId("child-queues-table");
    await expect(table).toContainText("orders");
    await expect(table).toContainText("1024");
    await page.getByRole("checkbox", { name: "Select orders" }).check();
    await page.getByTestId("child-queues-delete").click();
    await page.getByTestId("child-queues-delete-dialog-confirm").click();
    await expect(page.getByText("No queues to display")).toBeVisible();
  });
});

test.describe("Virtual networks", () => {
  test("creates a network with its subnet through the Networking tab and adds a subnet", async ({ page }) => {
    const group = unique("vnet-rg-");
    await createGroup(page, group);
    await page.goto("/ui/create/Microsoft.Network/virtualNetworks");
    await pickResourceGroup(page, group);
    await page.getByRole("textbox", { name: "Virtual network name" }).fill("e2e-vnet");
    await page.getByRole("tab", { name: "Networking" }).click();
    await page.getByRole("textbox", { name: "IPv4 address space" }).fill("10.40.0.0/16");
    await page.getByRole("textbox", { name: "Subnet address range" }).fill("10.40.0.0/24");
    await deploy(page);
    await page.getByTestId("deployment-go-to-resource").click();
    await page.getByRole("navigation", { name: "e2e-vnet menu" }).getByRole("link", { name: "Subnets" }).click();
    await expect(page.getByTestId("child-subnets-table")).toContainText("10.40.0.0/24");
    await page.getByTestId("child-subnets-create").click();
    await page.getByRole("textbox", { name: "Name" }).fill("apps");
    await page.getByRole("textbox", { name: "Subnet address range" }).fill("10.40.1.0/24");
    await page.getByTestId("child-subnets-submit").click();
    await expect(page.getByTestId("child-subnets-table")).toContainText("10.40.1.0/24");
  });
});

test.describe("All resources", () => {
  test("lists resources across types and narrows them with the Type filter", async ({ page }) => {
    await page.goto("/ui/browse/all");
    await expect(heading(page, "All resources")).toBeVisible();
    const table = page.getByTestId("all-table");
    await expect(table).toContainText("console-operator");
    await page.getByRole("button", { name: /^Type == / }).click();
    await page.getByRole("checkbox", { name: "User-assigned managed identity" }).check();
    await page.keyboard.press("Escape");
    await expect(table.locator("tbody tr")).not.toHaveCount(0);
    for (const row of await table.locator("tbody tr").all()) await expect(row).toContainText("User-assigned managed identity");
  });

  test("records a viewed resource under Home's Recent", async ({ page }) => {
    await page.goto(`/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/overview`);
    await expect(page.getByRole("region", { name: "Essentials" })).toContainText("eastus");
    await page.goto("/ui/home");
    await expect(page.getByTestId("home-recent")).toContainText("console-identity");
  });
});

test.describe("Access control (IAM)", () => {
  test("adds a role assignment for a service principal and removes it", async ({ page }) => {
    const group = unique("iam-rg-");
    await createGroup(page, group);
    await page.goto(`/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/${group}/users`);
    const assignments = page.getByTestId("role-assignments");
    await expect(assignments).toContainText("Subscription (Inherited)");
    await page.getByTestId("iam-add").click();
    await page.getByRole("combobox", { name: "Role" }).click();
    await page.getByRole("option", { name: "Reader", exact: true }).click();
    await page.getByRole("textbox", { name: "Search members" }).fill(FEDERATION.identity);
    await page.getByRole("checkbox", { name: new RegExp(FEDERATION.identity) }).check();
    await page.getByTestId("iam-assign").click();
    const reader = assignments.locator("tr", { hasText: "Reader" });
    await expect(reader).toContainText("This resource");
    await reader.getByRole("checkbox").check();
    await page.getByTestId("iam-remove").click();
    await page.getByTestId("iam-remove-dialog-confirm").click();
    await expect(assignments.locator("tr", { hasText: "Reader" })).toHaveCount(0);
  });
});

test.describe("Microsoft Entra ID", () => {
  test("shows the directory and registers an application with a client secret", async ({ page }) => {
    await page.goto("/ui/entra");
    await expect(page.getByRole("region", { name: "Essentials" })).toContainText(TENANT);
    const name = unique("e2e-app-");
    await page.goto("/ui/entra/app-registrations");
    await page.getByTestId("entra-new-registration").click();
    await page.getByTestId("entra-app-name-input").fill(name);
    await page.getByTestId("entra-register-submit").click();
    await expect(page.getByTestId("entra-app-name")).toHaveText(name);
    await expect(page.getByTestId("entra-cli-usage")).toContainText(`${CLOUD}/${TENANT}/oauth2/v2.0/token`);
    await page.getByRole("link", { name: "Certificates & secrets" }).click();
    await page.getByRole("button", { name: "New client secret" }).click();
    await page.getByLabel("Description").fill("e2e");
    await page.getByRole("button", { name: "Add" }).click();
    await expect(page.getByTestId("entra-secret-notice")).toBeVisible();
    await expect(page.getByTestId("entra-secret-row")).toHaveCount(1);
  });
});

test.describe("Monitor", () => {
  test("reads the subscription's activity log", async ({ page }) => {
    await page.goto("/ui/monitor/activitylog");
    await expect(page.getByTestId("activity-log")).toContainText("Create or Update");
  });

  test("runs a Kusto query against a Log Analytics workspace", async ({ page }) => {
    const group = unique("logs-rg-");
    await createGroup(page, group);
    await page.goto("/ui/create/Microsoft.OperationalInsights/workspaces");
    await pickResourceGroup(page, group);
    await page.getByRole("textbox", { name: "Name" }).fill(unique("e2e-law-"));
    await deploy(page);
    await page.goto("/ui/monitor/logs");
    await page.getByRole("textbox", { name: "Query" }).fill("AppTraces | count");
    await page.getByTestId("logs-run").click();
    await expect(page.getByTestId("logs-results").getByRole("columnheader", { name: "Count" })).toBeVisible();
    await page.getByRole("textbox", { name: "Query" }).fill("NoSuchTable | take 1");
    await page.getByTestId("logs-run").click();
    await expect(page.getByRole("alert")).toContainText("NoSuchTable");
  });
});

// Each page the console serves, audited with axe-core in both themes.
const AUDITED = [
  "/ui/home",
  "/ui/allservices",
  "/ui/create",
  "/ui/browse/all",
  "/ui/browse/resourcegroups",
  "/ui/browse/subscriptions",
  "/ui/browse/Microsoft.Storage/storageAccounts",
  "/ui/browse/Microsoft.Compute/virtualMachines",
  "/ui/browse/Microsoft.App/containerApps",
  "/ui/browse/Microsoft.Web/sites/kind/functionapp",
  "/ui/create/Microsoft.Resources/resourceGroups",
  "/ui/create/Microsoft.Storage/storageAccounts",
  `/ui/resource/subscriptions/${SUBSCRIPTION}/overview`,
  `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/overview`,
  `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/users`,
  `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/tags`,
  `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/activitylog`,
  `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/providers/Microsoft.ManagedIdentity/userAssignedIdentities/${FEDERATION.identity}/overview`,
  `/ui/resource/subscriptions/${SUBSCRIPTION}/resourceGroups/console-identity/providers/Microsoft.ManagedIdentity/userAssignedIdentities/${FEDERATION.identity}/properties`,
  "/ui/entra",
  "/ui/entra/users",
  "/ui/entra/app-registrations",
  "/ui/monitor",
  "/ui/monitor/activitylog",
  "/ui/monitor/logs",
];

for (const theme of ["Light", "Dark"] as const) {
  test.describe(`Accessibility (${theme})`, () => {
    for (const path of AUDITED) {
      test(`${path} has no axe violations`, async ({ page }) => {
        await page.goto("/ui/home");
        await setTheme(page, theme);
        await page.goto(path);
        await expect(page.locator(".az-blade, .az-home").first()).toBeVisible();
        await expect(page.getByRole("status").filter({ hasText: /^Loading/ })).toHaveCount(0);
        await expectNoAxeViolations(page);
      });
    }

    test("the open panes and menus have no axe violations", async ({ page }) => {
      await page.goto("/ui/home");
      await setTheme(page, theme);
      // Each pane is audited once it has finished opening, and the next opens
      // only after it has finished closing, so no audit sees a pane mid-motion.
      for (const [button, pane] of [
        ["Notifications", "Notifications"],
        ["Show portal menu", "Portal menu"],
      ] as const) {
        await page.getByRole("button", { name: button }).click();
        const dialog = page.getByRole("dialog", { name: pane });
        await expect(dialog).toBeVisible();
        await expectNoAxeViolations(page);
        await page.keyboard.press("Escape");
        await expect(dialog).toBeHidden();
      }
      await page.getByRole("button", { name: /^Account manager/ }).click();
      await expect(page.getByRole("dialog", { name: "Account" })).toBeVisible();
      await expectNoAxeViolations(page);
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog", { name: "Account" })).toBeHidden();
      await page.getByRole("combobox", { name: "Search resources, services, and docs" }).fill("stor");
      await expect(page.getByRole("option", { name: /Storage accounts/ })).toBeVisible();
      await expectNoAxeViolations(page);
    });
  });
}
