import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { PRODUCTS } from "../src/console/catalog";

// The workforce pool provider global-setup.ts creates for the operator sign-in.
const FEDERATION = {
  issuer: "http://127.0.0.1:19321/11111111-1111-1111-1111-111111111111/v2.0",
  clientId: "gcp-console",
  pool: "console-operators",
  provider: "console-sign-in",
};

// Every test signs in the way an operator does: the console redirects to the
// OpenID Connect issuer and back, then exchanges that assertion for a Google
// Cloud access token at the Security Token Service through the workforce pool
// provider, and calls the real Google Cloud APIs with it. Nothing here reaches
// the simulator any other way.

/** A resource name no other test, run, or worker shares. */
function unique(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
}

async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
}

/** Switches the visual mode through the console's own Settings and utilities menu. */
async function setTheme(page: Page, theme: "Light" | "Dark") {
  await page.evaluate(() => {
    const id = "e2e-disable-transitions";
    if (document.getElementById(id)) return;
    const style = document.createElement("style");
    style.id = id;
    style.textContent = "*, *::before, *::after { transition: none !important; animation: none !important; }";
    document.head.append(style);
  });
  await page.getByRole("button", { name: "Settings and utilities" }).click();
  await page.getByRole("menuitemradio", { name: theme }).click();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(theme === "Dark");
}

const heading = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });
const snackbar = (page: Page) => page.getByTestId("snackbar");

test.describe("Sign-in and federation", () => {
  test("signs the operator in and names the workforce pool provider in the account card", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page).toHaveURL(/\/ui\/welcome\?project=sockerless$/);
    const account = page.getByRole("button", { name: /^Google Account: / });
    await expect(account).toBeVisible();
    await account.click();
    const card = page.getByRole("dialog", { name: "Account" });
    await expect(card).toContainText(`Signed in through Workforce Identity Federation (${FEDERATION.pool} / ${FEDERATION.provider})`);
    await expect(card.getByRole("button", { name: "Sign out" })).toBeVisible();
  });

  test("lists the console's own workforce pool and provider under Workforce Identity Federation", async ({ page }) => {
    await page.goto("/ui/iam-admin/workforce-identity-pools");
    await expect(heading(page, "Workforce Identity Federation")).toBeVisible();
    await expect(page.getByTestId("workforce-pools-table")).toContainText(FEDERATION.pool);
    const providers = page.getByTestId("workforce-providers-table");
    await expect(providers).toContainText(FEDERATION.provider);
    await expect(providers).toContainText(FEDERATION.issuer);
    await expect(providers).toContainText(FEDERATION.clientId);
  });

  test("shows the pool's grant on the project's IAM page", async ({ page }) => {
    await page.goto("/ui/iam-admin/iam");
    await expect(page.getByTestId("iam-grants-table")).toContainText(
      `principalSet://iam.googleapis.com/locations/global/workforcePools/${FEDERATION.pool}/*`,
    );
    await expect(page.getByTestId("iam-grants-table")).toContainText("Workforce identity");
  });
});

test.describe("Console shell", () => {
  test("carries the selected project on every URL", async ({ page }) => {
    await page.goto("/ui/storage/browser");
    await expect(page).toHaveURL(/\/ui\/storage\/browser\?project=sockerless$/);
    await expect(page.getByTestId("project-picker")).toHaveAccessibleName("Project: sockerless. Select a project");
  });

  test("titles the tab with the page, product and project, as the real console does", async ({ page }) => {
    await page.goto("/ui/storage/browser");
    await expect(page).toHaveTitle("Buckets – Cloud Storage – sockerless – Google Cloud console");
    await page.goto("/ui/home/dashboard");
    await expect(page).toHaveTitle("Dashboard – sockerless – Google Cloud console");
  });

  test("contains every top bar control within the top bar", async ({ page }) => {
    await page.goto("/ui/welcome");
    await expect(page.getByRole("button", { name: /^Google Account:/ })).toBeVisible();
    const escaped = await page.evaluate(() => {
      const header = document.querySelector(".gc-header")!.getBoundingClientRect();
      return Array.from(document.querySelectorAll(".gc-header button, .gc-header a, .gc-header input"))
        .map((control) => ({ html: control.outerHTML.slice(0, 80), box: control.getBoundingClientRect() }))
        .filter(({ box }) => box.width > 0 && (box.top < header.top - 0.5 || box.bottom > header.bottom + 0.5))
        .map(({ html }) => html);
    });
    expect(escaped).toEqual([]);
  });

  test("offers only working controls in the top bar", async ({ page }) => {
    await page.goto("/ui/welcome");
    const header = page.locator(".gc-header");
    await expect(header.getByRole("button", { name: "Navigation menu" })).toBeVisible();
    await expect(header.getByRole("link", { name: "Google Cloud home" })).toBeVisible();
    await expect(header.getByRole("combobox", { name: "Search" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Notifications" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Help" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Settings and utilities" })).toBeVisible();
    await expect(header.getByRole("button", { name: /Gemini|Cloud Shell/ })).toHaveCount(0);
  });

  test("exposes a skip link ahead of the console content", async ({ page }) => {
    await page.goto("/ui/storage/browser");
    await page.locator("body").focus();
    await page.keyboard.press("Tab");
    await expect(page.locator(".sl-skip-link")).toBeFocused();
  });

  test("shows the product's own navigation with the current page marked", async ({ page }) => {
    await page.goto("/ui/iam-admin/roles");
    const nav = page.getByRole("navigation", { name: "IAM & Admin" });
    await expect(nav.getByRole("link", { name: "Roles" })).toHaveAttribute("aria-current", "page");
    await nav.getByRole("link", { name: "Service accounts" }).click();
    await expect(page).toHaveURL(/\/ui\/iam-admin\/serviceaccounts\?project=sockerless$/);
    await expect(heading(page, "Service accounts")).toBeVisible();
    await expect(nav.getByRole("link", { name: "Service accounts" })).toHaveAttribute("aria-current", "page");
  });

  test("hides the product navigation on the Cloud overview pages", async ({ page }) => {
    await page.goto("/ui/home/dashboard");
    await expect(page.locator(".gc-product-nav")).toHaveCount(0);
  });

  test("switches between light and dark from Settings and utilities", async ({ page }) => {
    await page.goto("/ui/welcome");
    await setTheme(page, "Dark");
    await setTheme(page, "Light");
  });

  test("links the current product's documentation from the Help menu", async ({ page }) => {
    await page.goto("/ui/cloudpubsub/topic/list");
    await page.getByRole("button", { name: "Help" }).click();
    await expect(page.getByRole("menuitem", { name: "Pub/Sub documentation" })).toHaveAttribute(
      "href",
      "https://cloud.google.com/pubsub/docs",
    );
    await page.getByRole("menuitem", { name: "Keyboard shortcuts" }).click();
    await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toContainText("Search products and pages");
  });

  test("lands a stale deep link on Welcome", async ({ page }) => {
    await page.goto("/ui/no-such-page");
    await expect(page).toHaveURL(/\/ui\/welcome\?project=sockerless$/);
    await expect(heading(page, "Welcome")).toBeVisible();
  });
});

test.describe("Navigation menu", () => {
  test("lists the pinned products and every product by category", async ({ page }) => {
    await page.goto("/ui/welcome");
    await page.getByRole("button", { name: "Navigation menu" }).click();
    const drawer = page.getByRole("dialog", { name: "Navigation menu" });
    const pinned = drawer.getByTestId("drawer-pinned");
    await expect(pinned.getByRole("link", { name: "Compute Engine" })).toBeVisible();
    await expect(pinned.getByRole("link", { name: "Cloud Storage" })).toBeVisible();
    await drawer.getByRole("button", { name: "More products" }).click();
    await expect(drawer.getByRole("heading", { name: "Analytics" })).toBeVisible();
    await drawer.getByRole("list", { name: "Analytics" }).getByRole("link", { name: "Pub/Sub" }).click();
    await expect(page).toHaveURL(/\/ui\/cloudpubsub\/topic\/list\?project=sockerless$/);
    await expect(drawer).toHaveCount(0);
  });

  test("pins and unpins a product", async ({ page }) => {
    await page.goto("/ui/welcome");
    await page.getByRole("button", { name: "Navigation menu" }).click();
    const drawer = page.getByRole("dialog", { name: "Navigation menu" });
    await drawer.getByRole("button", { name: "More products" }).click();
    await drawer.getByRole("button", { name: "Pin Security" }).click();
    await expect(drawer.getByTestId("drawer-pinned").getByRole("link", { name: "Security" })).toBeVisible();
    await drawer.getByTestId("drawer-pinned").getByRole("button", { name: "Unpin Security" }).click();
    await expect(drawer.getByTestId("drawer-pinned").getByRole("link", { name: "Security" })).toHaveCount(0);
  });

  test("closes on Escape and returns focus to the menu button", async ({ page }) => {
    await page.goto("/ui/welcome");
    const trigger = page.getByRole("button", { name: "Navigation menu" });
    await trigger.click();
    await expect(page.getByRole("dialog", { name: "Navigation menu" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Navigation menu" })).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("lists every product on View all products", async ({ page }) => {
    await page.goto("/ui/products");
    for (const product of PRODUCTS) {
      await expect(page.getByRole("link", { name: product.name, exact: true })).toBeVisible();
    }
  });
});

test.describe("Search", () => {
  test("focuses on \"/\" and opens a product page", async ({ page }) => {
    await page.goto("/ui/welcome");
    await expect(heading(page, "Welcome")).toBeVisible();
    const search = page.getByRole("combobox", { name: "Search" });
    await page.keyboard.press("/");
    await expect(search).toBeFocused();
    await search.fill("pub");
    await expect(page.getByRole("option").first()).toContainText("Pub/Sub");
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/ui\/cloudpubsub\/topic\/list\?project=sockerless$/);
    await expect(heading(page, "Topics")).toBeVisible();
  });

  test("finds a product's page by name", async ({ page }) => {
    await page.goto("/ui/welcome");
    await page.getByRole("combobox", { name: "Search" }).fill("disks");
    await page.getByRole("option", { name: /Disks/ }).click();
    await expect(page).toHaveURL(/\/ui\/compute\/disks\?project=sockerless$/);
  });

  test("does not take a \"/\" typed into another field", async ({ page }) => {
    await page.goto("/ui/logs/query");
    const query = page.getByTestId("log-query-input");
    await query.fill('logName:"projects/sockerless/logs/run.googleapis.com/stdout"');
    await expect(query).toHaveValue('logName:"projects/sockerless/logs/run.googleapis.com/stdout"');
  });
});

test.describe("Projects", () => {
  test("lists the projects Resource Manager answers in Select a project", async ({ page }) => {
    await page.goto("/ui/welcome");
    await page.getByTestId("project-picker").click();
    const dialog = page.getByRole("dialog", { name: "Select a project" });
    await expect(dialog.getByTestId("project-row-sockerless")).toHaveAttribute("aria-current", "true");
    await dialog.getByRole("tab", { name: "All" }).click();
    await expect(dialog).toContainText("123456789012");
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  });

  test("creates a project through the long-running operation and switches to it", async ({ page }) => {
    const id = unique("e2e-proj").slice(0, 28);
    await page.goto("/ui/welcome");
    await page.getByTestId("project-picker").click();
    await page.getByTestId("project-create-open").click();
    await expect(heading(page, "New Project")).toBeVisible();
    await page.getByTestId("project-create-name").fill(id);
    await expect(page.getByTestId("project-create-id")).toHaveValue(id);
    await page.getByTestId("project-create-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/home/dashboard\\?project=${id}$`));
    await expect(page.getByTestId("project-picker")).toContainText(id);
    await page.getByRole("button", { name: /^Notifications/ }).click();
    await expect(page.getByRole("dialog", { name: "Notifications" })).toContainText(`Create project "${id}"`);

    await page.goto(`/ui/cloud-resource-manager?project=${id}`);
    const table = page.getByTestId("manage-projects-table");
    await expect(table).toContainText(id);
    await table.getByRole("button", { name: `Actions for ${id}` }).click();
    await page.getByRole("menuitem", { name: "Delete" }).click();
    await page.getByTestId("project-delete-confirm").click();
    await expect(table.getByRole("row", { name: new RegExp(id) })).toContainText("Pending deletion");
  });
});

test.describe("Cloud overview", () => {
  test("shows the project info and resource counts on the Dashboard", async ({ page }) => {
    await page.goto("/ui/home/dashboard");
    await expect(page.getByRole("region", { name: "Project info" })).toContainText("123456789012");
    const resources = page.getByRole("region", { name: "Resources" });
    await expect(resources.getByRole("link", { name: "Cloud Storage" })).toBeVisible();
    await expect(resources).toContainText(/\d+ (bucket|buckets)/);
  });

  test("lists the Admin Activity audit log on Activity", async ({ page }) => {
    await page.goto("/ui/home/activity");
    await expect(page.getByTestId("activity-table")).toContainText("SetIamPolicy");
  });

  test("quick-links the pinned products on Welcome", async ({ page }) => {
    await page.goto("/ui/welcome");
    const quick = page.getByRole("region", { name: "Quick access" });
    await quick.getByRole("link", { name: "Cloud Storage" }).click();
    await expect(heading(page, "Buckets")).toBeVisible();
  });
});

test.describe("Cloud Storage", () => {
  test("creates a bucket, uploads and deletes an object, and deletes the bucket", async ({ page }) => {
    const name = unique("e2e-bucket");
    await page.goto("/ui/storage/browser");
    await page.getByRole("link", { name: "Create", exact: true }).click();
    await expect(heading(page, "Create a bucket")).toBeVisible();
    await page.getByLabel("Name your bucket").fill(name);
    await page.getByRole("radio", { name: "Region", exact: true }).check();
    await page.getByLabel("Location", { exact: true }).selectOption("US-CENTRAL1");
    await page.getByRole("radio", { name: /^Nearline/ }).check();
    await page.getByRole("button", { name: "Equivalent code" }).click();
    await expect(page.getByRole("region", { name: "Equivalent REST request" })).toContainText('"storageClass": "NEARLINE"');
    await page.getByTestId("gcs-create-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/storage/browser/${name}\\?project=sockerless$`));
    await expect(snackbar(page)).toContainText(`Created bucket "${name}"`);

    await page.getByTestId("gcs-upload-input").setInputFiles({
      name: "hello.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("hello from the console"),
    });
    const objects = page.getByTestId("gcs-objects-table");
    await expect(objects).toContainText("hello.txt");
    await expect(objects).toContainText("text/plain");

    await page.getByRole("button", { name: "Create folder" }).click();
    await page.getByTestId("gcs-folder-name").fill("reports");
    await page.getByRole("dialog", { name: "Create folder" }).getByRole("button", { name: "Create" }).click();
    await objects.getByRole("button", { name: "reports/" }).click();
    await expect(page.getByRole("navigation", { name: "Folder path" })).toContainText("reports");
    await page.getByRole("navigation", { name: "Folder path" }).getByRole("button", { name }).click();

    await objects.getByRole("checkbox", { name: "Select hello.txt" }).check();
    await page.getByTestId("gcs-object-delete").click();
    await page.getByTestId("gcs-object-delete-confirm").click();
    await expect(objects).not.toContainText("hello.txt");

    await page.getByRole("tab", { name: "Configuration" }).click();
    await expect(page.getByRole("tabpanel")).toContainText("Region");
    await expect(page.getByRole("tabpanel")).toContainText("us-central1");
    await expect(page.getByRole("tabpanel")).toContainText("Nearline");
    await expect(page.getByRole("tabpanel")).toContainText(`gs://${name}`);
    await page.getByRole("tab", { name: "Permissions" }).click();
    await expect(page.getByTestId("gcs-permissions-table")).toContainText("roles/storage.legacyBucketOwner");
  });

  test("filters the bucket list with a property chip and deletes a bucket with the typed confirmation", async ({ page }) => {
    const name = unique("e2e-del");
    await page.goto("/ui/storage/create-bucket");
    await page.getByLabel("Name your bucket").fill(name);
    await page.getByTestId("gcs-create-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/storage/browser/${name}`));

    await page.goto("/ui/storage/browser");
    const table = page.getByTestId("gcs-buckets-table");
    const filter = page.getByRole("combobox", { name: "Filter" });
    await filter.fill(`Name: ${name}`);
    await filter.press("Enter");
    await expect(page.getByRole("list", { name: "Active filters" })).toContainText(`Name: ${name}`);
    await expect(table.getByRole("row")).toHaveCount(2);
    await table.getByRole("checkbox", { name: `Select ${name}` }).check();
    await page.getByTestId("gcs-delete-selected").click();
    const dialog = page.getByRole("dialog", { name: "Delete bucket?" });
    await expect(dialog.getByTestId("gcs-delete-confirm")).toBeDisabled();
    await dialog.getByTestId("gcs-delete-confirmation").fill("DELETE");
    await dialog.getByTestId("gcs-delete-confirm").click();
    await expect(table).toContainText("No rows to display");
    await expect(snackbar(page)).toContainText(`Deleted bucket "${name}"`);
  });

  test("remembers the columns chosen in Column display options", async ({ page }) => {
    await page.goto("/ui/storage/browser");
    const table = page.getByTestId("gcs-buckets-table");
    await expect(table.getByRole("columnheader", { name: /Labels/ })).toHaveCount(0);
    await page.getByRole("button", { name: "Column display options" }).click();
    const dialog = page.getByRole("dialog", { name: "Column display options" });
    await dialog.getByRole("checkbox", { name: "Labels" }).check();
    await dialog.getByRole("checkbox", { name: "Access control" }).uncheck();
    await dialog.getByRole("button", { name: "OK" }).click();
    await expect(table.getByRole("columnheader", { name: /Labels/ })).toHaveCount(1);
    await expect(table.getByRole("columnheader", { name: /Access control/ })).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("gcs-buckets-table").getByRole("columnheader", { name: /Labels/ })).toHaveCount(1);
  });
});

test.describe("Compute Engine", () => {
  test("creates a VPC network and a VM instance in it, stops it, and deletes both", async ({ page }) => {
    test.setTimeout(180_000);
    const network = unique("e2e-net");
    const vm = unique("e2e-vm");
    await page.goto("/ui/networking/networks/list");
    await page.getByRole("link", { name: "Create VPC network" }).click();
    await expect(heading(page, "Create a VPC network")).toBeVisible();
    await page.getByLabel("Name", { exact: true }).fill(network);
    await page.getByTestId("vpc-create-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/networking/networks/details/${network}`));
    await expect(snackbar(page)).toContainText(`Created VPC network "${network}"`);

    await page.goto("/ui/compute/instances");
    await page.getByRole("link", { name: "Create instance" }).click();
    await expect(heading(page, "Create an instance")).toBeVisible();
    await page.getByLabel("Name", { exact: true }).fill(vm);
    await page.getByLabel("Machine type").selectOption("e2-micro");
    await page.getByLabel("Network", { exact: true }).selectOption(network);
    await page.getByTestId("compute-create-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/compute/instancesDetail/zones/us-central1-a/instances/${vm}`), { timeout: 60_000 });
    await expect(page.getByRole("tabpanel")).toContainText("Running");
    await expect(page.getByRole("tabpanel")).toContainText("e2-micro");

    await page.goto("/ui/compute/instances");
    const table = page.getByTestId("compute-instances-table");
    await table.getByRole("checkbox", { name: `Select ${vm}` }).check();
    await page.getByRole("button", { name: "Stop" }).click();
    await page.getByTestId("compute-stop-confirm").click();
    await expect(table.getByRole("row", { name: new RegExp(vm) })).toContainText("Terminated", { timeout: 60_000 });

    await page.goto("/ui/compute/disks");
    await expect(page.getByTestId("compute-disks-list-table")).toContainText(vm);

    await page.goto("/ui/compute/instances");
    await page.getByTestId("compute-instances-table").getByRole("button", { name: `Actions for ${vm}` }).click();
    await page.getByRole("menuitem", { name: "Delete" }).click();
    await page.getByTestId("compute-delete-confirm").click();
    await expect(page.getByTestId("compute-instances-table")).not.toContainText(vm, { timeout: 60_000 });

    await page.goto(`/ui/networking/networks/details/${network}`);
    await page.getByTestId("vpc-delete").click();
    await page.getByTestId("vpc-delete-confirm").click();
    await expect(page).toHaveURL(/\/ui\/networking\/networks\/list/);
    await expect(page.getByTestId("vpc-networks-table")).not.toContainText(network);
  });

  test("validates the instance name before creating", async ({ page }) => {
    await page.goto("/ui/compute/instancesAdd");
    await page.getByLabel("Name", { exact: true }).fill("Bad_Name");
    await expect(page.getByText("Name must start with a lowercase letter")).toBeVisible();
    await expect(page.getByTestId("compute-create-form-submit")).toBeDisabled();
  });
});

test.describe("Pub/Sub", () => {
  test("creates a topic with its default subscription, publishes, pulls and deletes", async ({ page }) => {
    const topic = unique("e2e-topic");
    await page.goto("/ui/cloudpubsub/topic/list");
    await page.getByTestId("pubsub-create-topic").click();
    await page.getByTestId("pubsub-create-topic-id").fill(topic);
    await page.getByTestId("pubsub-create-topic-submit").click();
    await expect(snackbar(page)).toContainText(`Created topic "${topic}"`);
    await page.getByTestId("pubsub-topics-table").getByRole("link", { name: topic }).click();
    await expect(heading(page, topic)).toBeVisible();
    await expect(page.getByTestId("pubsub-subscriptions-table")).toContainText(`${topic}-sub`);

    await page.getByTestId("pubsub-topic-publish").click();
    await page.getByTestId("pubsub-publish-body").fill("hello subscribers");
    await page.getByTestId("pubsub-publish-submit").click();
    await expect(snackbar(page)).toContainText(`Published message to "${topic}"`);

    await page.getByTestId("pubsub-subscriptions-table").getByRole("link", { name: `${topic}-sub` }).click();
    await page.getByRole("tab", { name: "Messages" }).click();
    await page.getByRole("checkbox", { name: "Enable ack messages" }).check();
    await page.getByTestId("pubsub-pull").click();
    await expect(page.getByTestId("pubsub-messages-table")).toContainText("hello subscribers");

    await page.goto("/ui/cloudpubsub/topic/list");
    await page.getByTestId("pubsub-topics-table").getByRole("button", { name: `Actions for ${topic}` }).click();
    await page.getByRole("menuitem", { name: "Delete" }).click();
    await page.getByTestId("pubsub-delete-topic-confirm").click();
    await expect(page.getByTestId("pubsub-topics-table")).not.toContainText(topic);
  });

  test("creates a subscription on its own page and deletes it", async ({ page }) => {
    const topic = unique("e2e-subtopic");
    const subscription = unique("e2e-sub");
    await page.goto("/ui/cloudpubsub/topic/list");
    await page.getByTestId("pubsub-create-topic").click();
    await page.getByTestId("pubsub-create-topic-id").fill(topic);
    await page.getByRole("checkbox", { name: "Add a default subscription" }).uncheck();
    await page.getByTestId("pubsub-create-topic-submit").click();
    await expect(snackbar(page)).toContainText(`Created topic "${topic}"`);

    await page.goto("/ui/cloudpubsub/subscription/list");
    await page.getByRole("link", { name: "Create subscription" }).click();
    await page.getByTestId("pubsub-create-sub-id").fill(subscription);
    await page.getByTestId("pubsub-create-sub-topic").selectOption(topic);
    await page.getByTestId("pubsub-create-sub-ack").fill("30");
    await page.getByTestId("pubsub-create-sub-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/cloudpubsub/subscription/detail/${subscription}`));
    await expect(page.getByRole("tabpanel")).toContainText("30 s");
    await page.getByTestId("pubsub-sub-delete").click();
    await page.getByTestId("pubsub-delete-sub-confirm").click();
    await expect(page).toHaveURL(/\/ui\/cloudpubsub\/subscription\/list/);
    await expect(page.getByTestId("pubsub-subscriptions-list-table")).not.toContainText(subscription);
  });
});

test.describe("Cloud Scheduler", () => {
  test("creates a job that publishes to a topic, force-runs it, pauses it and deletes it", async ({ page }) => {
    const topic = unique("e2e-cron");
    const job = unique("e2e-job");
    await page.goto("/ui/cloudpubsub/topic/list");
    await page.getByTestId("pubsub-create-topic").click();
    await page.getByTestId("pubsub-create-topic-id").fill(topic);
    await page.getByTestId("pubsub-create-topic-submit").click();
    await expect(snackbar(page)).toContainText(`Created topic "${topic}"`);

    await page.goto("/ui/cloudscheduler");
    await page.getByRole("link", { name: "Create job" }).click();
    await page.getByLabel("Name", { exact: true }).fill(job);
    await page.getByLabel("Frequency").fill("0 3 * * *");
    await page.getByLabel("Select a Cloud Pub/Sub topic").selectOption(topic);
    await page.getByLabel("Message body").fill("nightly");
    await page.getByTestId("scheduler-create-form-submit").click();
    await expect(page).toHaveURL(/\/ui\/cloudscheduler\?project=sockerless$/);
    const table = page.getByTestId("scheduler-jobs-table");
    const row = table.getByRole("row", { name: new RegExp(job) });
    await expect(row).toContainText("Enabled");
    await expect(row).toContainText("Has not run yet");

    await table.getByRole("button", { name: `Actions for ${job}` }).click();
    await page.getByRole("menuitem", { name: "Force run" }).click();
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(row).toContainText("Success");
    await table.getByRole("button", { name: `Actions for ${job}` }).click();
    await page.getByRole("menuitem", { name: "Pause" }).click();
    await expect(row).toContainText("Paused");

    await table.getByRole("checkbox", { name: `Select ${job}` }).check();
    await page.getByRole("button", { name: "Delete" }).click();
    await page.getByTestId("scheduler-delete-confirm").click();
    await expect(table).not.toContainText(job);
  });
});

test.describe("Secret Manager", () => {
  test("creates a secret with a value, reads it back, versions it and deletes it", async ({ page }) => {
    const secret = unique("e2e-secret");
    await page.goto("/ui/security/secret-manager");
    await page.getByRole("link", { name: "Create secret" }).click();
    await page.getByTestId("secrets-create-id").fill(secret);
    await page.getByTestId("secrets-create-value").fill("s3cr3t-value");
    await page.getByTestId("secrets-create-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/security/secret-manager/secret/${secret}/versions`));
    await page.getByRole("tab", { name: "Versions" }).click();
    const versions = page.getByTestId("secrets-versions-table");
    await expect(versions).toContainText("Enabled");
    await versions.getByRole("button", { name: "Actions for version 1" }).click();
    await page.getByRole("menuitem", { name: "View secret value" }).click();
    await expect(page.getByTestId("secrets-value")).toHaveText("s3cr3t-value");
    await page.getByRole("button", { name: "Done" }).click();

    await page.getByTestId("secrets-detail-add-version").click();
    await page.getByTestId("secrets-add-version-value").fill("second");
    await page.getByTestId("secrets-add-version-submit").click();
    await expect(versions.getByRole("row")).toHaveCount(3);
    await versions.getByRole("button", { name: "Actions for version 1" }).click();
    await page.getByRole("menuitem", { name: "Disable" }).click();
    await expect(versions.getByRole("row").filter({ hasText: "Disabled" })).toHaveCount(1);

    await page.getByTestId("secrets-detail-delete").click();
    await page.getByTestId("secrets-delete-confirmation").fill(secret);
    await page.getByTestId("secrets-delete-confirm").click();
    await expect(page).toHaveURL(/\/ui\/security\/secret-manager\?project=sockerless$/);
    await expect(page.getByTestId("secrets-table")).not.toContainText(secret);
  });
});

test.describe("Artifact Registry", () => {
  test("creates a repository on its own page and deletes it from the list", async ({ page }) => {
    const repo = unique("e2e-repo");
    await page.goto("/ui/artifacts");
    await page.getByRole("link", { name: "Create repository" }).click();
    await page.getByTestId("ar-create-id").fill(repo);
    await page.getByRole("radio", { name: "Npm" }).check();
    await page.getByLabel("Description").fill("console e2e");
    await page.getByTestId("ar-create-form-submit").click();
    await expect(page).toHaveURL(new RegExp(`/ui/artifacts/npm/sockerless/us-central1/${repo}`));

    await page.goto("/ui/artifacts");
    const table = page.getByTestId("ar-repos-table");
    await expect(table.getByRole("row", { name: new RegExp(repo) })).toContainText("Npm");
    await table.getByRole("checkbox", { name: `Select ${repo}` }).check();
    await page.getByRole("button", { name: "Delete" }).click();
    await page.getByTestId("ar-delete-confirm").click();
    await expect(table).not.toContainText(repo);
  });
});

test.describe("IAM", () => {
  test("grants a role to a principal and removes it", async ({ page }) => {
    const member = `user:${unique("e2e")}@example.com`;
    await page.goto("/ui/iam-admin/iam");
    await page.getByTestId("iam-grant-access").click();
    await page.getByTestId("iam-grant-member").fill(member);
    await page.getByTestId("iam-grant-role").selectOption("roles/viewer");
    await page.getByTestId("iam-grant-submit").click();
    const table = page.getByTestId("iam-grants-table");
    await expect(table).toContainText(member);
    await table.getByRole("button", { name: `Actions for ${member}` }).click();
    await page.getByRole("menuitem", { name: /^Remove/ }).click();
    await page.getByTestId("iam-remove-confirm").click();
    await expect(table).not.toContainText(member);
  });

  test("lists the predefined roles", async ({ page }) => {
    await page.goto("/ui/iam-admin/roles");
    await expect(page.getByTestId("iam-roles-table")).toContainText("roles/viewer");
  });

  test("creates a service account and opens it", async ({ page }) => {
    const id = unique("sa").slice(0, 28);
    await page.goto("/ui/iam-admin/serviceaccounts");
    await page.getByRole("button", { name: /Create service account/ }).click();
    await page.getByTestId("sa-create-name").fill(id);
    await page.getByTestId("sa-create-submit").click();
    const email = `${id}@sockerless.iam.gserviceaccount.com`;
    await page.getByTestId(`sa-row-${email}`).click();
    await expect(page).toHaveURL(new RegExp(`/ui/iam-admin/serviceaccounts/details/${email}`));
  });
});

test.describe("Logs Explorer", () => {
  test("runs a Cloud Logging query and expands an entry", async ({ page }) => {
    await page.goto("/ui/logs/query");
    await page.getByTestId("log-query-input").fill('protoPayload.methodName="SetIamPolicy"');
    await page.getByTestId("log-query-run").click();
    await expect(page).toHaveURL(/query=protoPayload\.methodName/);
    const results = page.getByTestId("log-results");
    await expect(results).toContainText("SetIamPolicy");
    await results.getByRole("button", { expanded: false }).first().click();
    await expect(page.getByTestId("log-entry-payload").first()).toContainText('"methodName": "SetIamPolicy"');
  });
});

test.describe("Product pages", () => {
  for (const product of PRODUCTS) {
    for (const section of product.nav) {
      for (const item of section.items) {
        test(`${product.name} › ${item.label} renders under its product navigation`, async ({ page }) => {
          await page.goto(item.to);
          await expect(page.getByRole("navigation", { name: product.name })).toBeVisible();
          await expect(page.getByRole("navigation", { name: product.name }).getByRole("link", { name: item.label, exact: true })).toHaveAttribute(
            "aria-current",
            "page",
          );
          await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
          await expect(page.getByRole("alert")).toHaveCount(0);
        });
      }
    }
  }
});

test.describe("Accessibility", () => {
  const pages = [
    "/ui/welcome",
    "/ui/home/dashboard",
    "/ui/home/activity",
    "/ui/products",
    "/ui/storage/browser",
    "/ui/storage/create-bucket",
    "/ui/compute/instances",
    "/ui/compute/instancesAdd",
    "/ui/networking/networks/add",
    "/ui/run",
    "/ui/run/create",
    "/ui/cloudpubsub/topic/list",
    "/ui/cloudpubsub/subscription/create",
    "/ui/security/secret-manager/create",
    "/ui/artifacts/create-repo",
    "/ui/iam-admin/iam",
    "/ui/iam-admin/workforce-identity-pools",
    "/ui/logs/query",
    "/ui/cloudscheduler/jobs/new",
    "/ui/cloud-resource-manager",
    "/ui/projectcreate",
  ];
  for (const theme of ["Light", "Dark"] as const) {
    for (const path of pages) {
      test(`${path} has no detectable violations (${theme.toLowerCase()})`, async ({ page }) => {
        await page.goto(path);
        await setTheme(page, theme);
        await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
        await expect(page.locator(".gc-loading")).toHaveCount(0);
        await expectNoAxeViolations(page);
      });
    }
    test(`the navigation menu and project dialog have no detectable violations (${theme.toLowerCase()})`, async ({ page }) => {
      await page.goto("/ui/welcome");
      await setTheme(page, theme);
      await page.getByRole("button", { name: "Navigation menu" }).click();
      await page.getByRole("button", { name: "More products" }).click();
      await expectNoAxeViolations(page);
      await page.keyboard.press("Escape");
      await page.getByTestId("project-picker").click();
      await expect(page.getByTestId("project-row-sockerless")).toBeVisible();
      await expectNoAxeViolations(page);
    });
  }
});
