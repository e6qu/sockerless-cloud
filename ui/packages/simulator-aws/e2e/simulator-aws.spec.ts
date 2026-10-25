import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { SERVICES } from "../src/console/serviceCatalog";

// Every test signs in the way an operator does — the console redirects to the
// OpenID Connect issuer and back — and the console then federates that
// assertion into AWS credentials with AssumeRoleWithWebIdentity and signs every
// AWS request with them. Nothing here reaches the simulator any other way.

/** A name no other test, run, or worker shares. */
function unique(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

/** Forces the console into the requested visual mode through its own Settings
 * menu, with transitions off so contrast is measured at settled colours. */
async function ensureTheme(page: Page, theme: "light" | "dark") {
  await page.evaluate(() => {
    const id = "e2e-disable-transitions";
    if (document.getElementById(id)) return;
    const style = document.createElement("style");
    style.id = id;
    style.textContent = "*, *::before, *::after { transition: none !important; animation: none !important; }";
    document.head.append(style);
  });
  await page.getByRole("button", { name: "Settings" }).click();
  await page.getByRole("menuitemcheckbox", { name: theme === "dark" ? "Dark" : "Light" }).click();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(theme === "dark");
}

async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
}

function table(page: Page, testId: string) {
  return page.getByTestId(testId);
}

test.describe("Sign-in and federation", () => {
  test("signs the operator in and shows the federated principal and account in the account menu", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page).toHaveURL(/\/ui\/$/);
    const account = page.getByRole("button", { name: "Account menu: ConsoleOperator/console @ 1234-5678-9012" });
    await expect(account).toBeVisible();
    await account.click();
    await expect(page.getByRole("menuitem", { name: /Account ID: 1234-5678-9012/ })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Sign out" })).toBeVisible();
  });

  test("lists the Regions DescribeRegions answers in the Region selector", async ({ page }) => {
    await page.goto("/ui/");
    const region = page.getByRole("button", { name: "Region: N. Virginia" });
    await expect(region).toHaveText("N. Virginia");
    await region.click();
    await expect(page.getByRole("menuitemcheckbox", { name: /US East \(N\. Virginia\)/ })).toHaveAttribute("aria-checked", "true");
  });

  test("lists the console's own identity provider under IAM Identity providers", async ({ page }) => {
    await page.goto("/ui/iam/identity-providers");
    await expect(page.getByRole("heading", { level: 1, name: "Identity providers" })).toBeVisible();
    await expect(table(page, "iam-identity-providers-table")).toContainText("127.0.0.1:19311/11111111-1111-1111-1111-111111111111/v2.0");
    await expect(table(page, "iam-identity-providers-table")).toContainText("aws-console");
  });
});

test.describe("Console shell", () => {
  test("puts the AWS logo, Services menu, search, and utilities in the top navigation", async ({ page }) => {
    await page.goto("/ui/s3");
    const header = page.locator(".aws-header");
    await expect(header.getByRole("link", { name: "AWS Console Home" })).toHaveAttribute("href", "/ui/");
    await expect(header.getByRole("button", { name: "Services" })).toBeVisible();
    await expect(header.getByRole("combobox", { name: "Search" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Settings" })).toBeVisible();
    await header.getByRole("link", { name: "AWS Console Home" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Console Home" })).toBeVisible();
  });

  // A control taller than the header used to spill below it, where the
  // breadcrumb bar painted over it and clicks landed on the breadcrumbs.
  test("contains every header control within the header itself", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("button", { name: /^Account menu:/ })).toBeVisible();
    const escaped = await page.evaluate(() => {
      const header = document.querySelector(".aws-header")!.getBoundingClientRect();
      return Array.from(document.querySelectorAll(".aws-header button, .aws-header a, .aws-header input"))
        .filter((control) => !control.closest('[aria-hidden="true"]'))
        .map((control) => ({ html: control.outerHTML.slice(0, 80), box: control.getBoundingClientRect() }))
        .filter(({ box }) => box.width > 0 && (box.top < header.top - 0.5 || box.bottom > header.bottom + 0.5))
        .map(({ html }) => html);
    });
    expect(escaped).toEqual([]);
  });

  test("keeps the AWS S3 root protocol surface separate from /ui/", async ({ page }) => {
    const response = await page.goto("/");
    expect(page.url()).toBe("http://localhost:19310/");
    expect(response?.status()).toBe(200);
    await expect(page.locator("body")).toContainText("ListAllMyBucketsResult");
  });

  test("exposes a skip link ahead of the console content", async ({ page }) => {
    await page.goto("/ui/s3");
    await page.locator("body").focus();
    await page.keyboard.press("Tab");
    await expect(page.locator(".sl-skip-link")).toBeFocused();
    await expect(page.locator("#main-content")).toHaveCount(1);
  });

  test("shows the service's own side navigation, with the current page marked", async ({ page }) => {
    await page.goto("/ui/iam/roles");
    const nav = page.getByRole("navigation", { name: "Service navigation" });
    await expect(nav.getByRole("link", { name: "IAM", exact: true })).toBeVisible();
    await expect(nav.getByRole("link", { name: "Roles", exact: true })).toHaveAttribute("aria-current", "page");
    await expect(nav.getByRole("link", { name: "Users", exact: true })).not.toHaveAttribute("aria-current", "page");
    await nav.getByRole("link", { name: "Policies", exact: true }).click();
    await expect(page).toHaveURL(/\/ui\/iam\/policies$/);
    await expect(page.getByRole("heading", { level: 1, name: "Policies" })).toBeVisible();
  });

  test("hides the side navigation on Console Home, as the real console does", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("navigation", { name: "Service navigation" })).toHaveCount(0);
    await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toHaveCount(0);
  });

  test("roots the breadcrumbs at the service and names the resource on a detail page", async ({ page }) => {
    await page.goto("/ui/ec2/key-pairs");
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    await expect(crumbs).toContainText("EC2");
    await expect(crumbs).toContainText("Key Pairs");
    await crumbs.getByRole("link", { name: "EC2" }).click();
    await expect(page).toHaveURL(/\/ui\/ec2\/home$/);
    await expect(page.getByRole("heading", { level: 1, name: "Dashboard" })).toBeVisible();
  });

  test("toggles the side navigation with its own open and close controls", async ({ page }) => {
    await page.goto("/ui/s3");
    await page.getByRole("button", { name: "Close navigation" }).click();
    await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
    await page.getByRole("button", { name: "Open navigation" }).click();
    await expect(page.getByRole("button", { name: "Close navigation" })).toBeVisible();
  });

  test("opens the help panel from a page heading's Info link", async ({ page }) => {
    await page.goto("/ui/sqs");
    await expect(page.getByRole("heading", { level: 1, name: /Queues/ })).toBeVisible();
    await page.getByRole("button", { name: /^Info/ }).first().click();
    const help = page.getByRole("complementary", { name: "Help panel" });
    await expect(help.getByRole("heading", { name: "Simple Queue Service" })).toBeVisible();
    await expect(help.getByRole("link", { name: /Simple Queue Service documentation/ })).toHaveAttribute(
      "href",
      "https://docs.aws.amazon.com/sqs/",
    );
  });

  test("sets the tab title the real console sets: page, service, Region", async ({ page }) => {
    await page.goto("/ui/iam/roles");
    await expect(page).toHaveTitle("Roles | IAM | us-east-1");
  });

  test("switches between light and dark visual modes from Settings", async ({ page }) => {
    await page.goto("/ui/");
    await ensureTheme(page, "dark");
    await expect(page.locator("body")).toHaveClass(/awsui-dark-mode/);
    await ensureTheme(page, "light");
    await expect(page.locator("body")).not.toHaveClass(/awsui-dark-mode/);
  });

  test("switches the content density from Settings", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Settings" }).click();
    await page.getByRole("menuitemcheckbox", { name: "Compact" }).click();
    await expect(page.locator("body")).toHaveClass(/awsui-compact-mode/);
    await page.getByRole("button", { name: "Settings" }).click();
    await page.getByRole("menuitemcheckbox", { name: "Comfortable" }).click();
    await expect(page.locator("body")).not.toHaveClass(/awsui-compact-mode/);
  });
});

test.describe("Unified search", () => {
  test("finds a service feature and opens it", async ({ page }) => {
    await page.goto("/ui/");
    const search = page.getByRole("combobox", { name: "Search" });
    await expect(search).toBeVisible();
    await page.keyboard.press("Alt+s");
    await expect(search).toBeFocused();
    await search.fill("key pairs");
    await page.getByRole("option", { name: /Key Pairs/ }).click();
    await expect(page).toHaveURL(/\/ui\/ec2\/key-pairs$/);
    await expect(page.getByRole("heading", { level: 1, name: "Key pairs" })).toBeVisible();
  });

  test("ranks the service ahead of its features", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("combobox", { name: "Search" }).fill("lambda");
    await expect(page.getByRole("option").first()).toContainText("Lambda");
    await expect(page.getByRole("option").first()).toContainText("Run code without thinking about servers");
  });
});

test.describe("Services menu", () => {
  test("opens with aria-expanded, focuses its search field, and closes on Escape returning focus", async ({ page }) => {
    await page.goto("/ui/");
    const trigger = page.getByRole("button", { name: "Services" });
    await expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
    await trigger.click();
    await expect(trigger).toHaveAttribute("aria-expanded", "true");
    const dialog = page.getByRole("dialog", { name: "Services" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("searchbox", { name: "Search services" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("dismisses on an outside click", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    await expect(page.getByRole("dialog", { name: "Services" })).toBeVisible();
    await page.mouse.click(700, 800);
    await expect(page.getByRole("dialog", { name: "Services" })).toHaveCount(0);
  });

  test("traps Tab focus inside the open panel", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    const dialog = page.getByRole("dialog", { name: "Services" });
    const first = dialog.getByRole("link", { name: "Recently visited" });
    await first.focus();
    await page.keyboard.press("Shift+Tab");
    await expect(dialog.locator(":focus")).toHaveCount(1);
  });

  test("lists a category's services with their descriptions, and narrows as the operator types", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    const dialog = page.getByRole("dialog", { name: "Services" });
    await dialog.getByRole("link", { name: "Storage", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: "Storage" })).toBeVisible();
    await expect(dialog.getByRole("link", { name: "S3", exact: true })).toBeVisible();
    await expect(dialog).toContainText("Scalable Storage in the Cloud");
    await expect(dialog.getByRole("link", { name: "Lambda", exact: true })).toHaveCount(0);
    await dialog.getByRole("searchbox", { name: "Search services" }).fill("queue");
    await expect(dialog.getByRole("link", { name: "Simple Queue Service" })).toBeVisible();
    await dialog.getByRole("link", { name: "Simple Queue Service" }).click();
    await expect(page).toHaveURL(/\/ui\/sqs$/);
    await expect(page.getByRole("dialog", { name: "Services" })).toHaveCount(0);
  });

  test("routes a service with no console page to the page that says so", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    await page.getByRole("searchbox", { name: "Search services" }).fill("kubernetes");
    await page.getByRole("link", { name: "Elastic Kubernetes Service, no page in this console" }).click();
    await expect(page).toHaveURL(/\/ui\/not-supported\/eks$/);
    await expect(page.getByRole("heading", { level: 1, name: "Elastic Kubernetes Service" })).toBeVisible();
    await expect(page.getByLabel("Elastic Kubernetes Service has no page in this console")).toBeVisible();
  });
});

test.describe("Console Home", () => {
  test("lists the services the operator visited, newest first", async ({ page }) => {
    await page.goto("/ui/sqs");
    await expect(page.getByRole("heading", { level: 1, name: /Queues/ })).toBeVisible();
    await page.goto("/ui/dynamodb");
    await expect(page.getByRole("heading", { level: 1, name: /Tables/ })).toBeVisible();
    await page.goto("/ui/");
    const recent = page.getByRole("list", { name: "Recently visited services" });
    await expect(recent.getByRole("link")).toHaveText(["DynamoDB", "Simple Queue Service"]);
    await recent.getByRole("link", { name: "Simple Queue Service" }).click();
    await expect(page).toHaveURL(/\/ui\/sqs$/);
  });

  test("opens the Services menu from View all services", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("link", { name: "View all services" }).click();
    await expect(page.getByRole("dialog", { name: "Services" })).toBeVisible();
  });

  test("starts Build a solution tasks in their own console flows", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("list", { name: "Build a solution" }).getByRole("link", { name: "Create a bucket" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Create bucket" })).toBeVisible();
  });
});

test.describe("Amazon S3", () => {
  test("creates a bucket, uploads and deletes an object, and deletes the bucket", async ({ page }) => {
    const bucket = unique("e2e-bucket");
    await page.goto("/ui/s3");
    await page.getByTestId("s3-create-bucket").click();
    await expect(page).toHaveURL(/\/ui\/s3\/create$/);
    await expect(page.getByText("US East (N. Virginia) us-east-1")).toBeVisible();
    await page.getByTestId("s3-bucket-name-input").fill(bucket);
    await page.getByTestId("s3-create-bucket-submit").click();

    await expect(page).toHaveURL(/\/ui\/s3$/);
    await expect(page.getByText(`Successfully created bucket "${bucket}"`)).toBeVisible();
    await expect(table(page, "s3-buckets-table").getByRole("link", { name: bucket })).toBeVisible();

    await table(page, "s3-buckets-table").getByRole("link", { name: bucket }).click();
    await expect(page.getByRole("heading", { level: 1, name: bucket })).toBeVisible();
    await page.getByTestId("s3-upload-input").setInputFiles({
      name: "hello.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("hello from the console"),
    });
    await expect(page.getByText("Upload succeeded: 1 file")).toBeVisible();
    const objects = table(page, "s3-objects-table");
    await expect(objects.getByRole("row", { name: /hello\.txt/ })).toContainText("22 B");

    await page.getByRole("tab", { name: "Properties" }).click();
    await expect(page.getByTestId("s3-bucket-summary")).toContainText(`arn:aws:s3:::${bucket}`);
    await page.getByRole("tab", { name: "Objects" }).click();

    await objects.getByRole("checkbox", { name: "Select hello.txt" }).check();
    await page.getByTestId("s3-delete-objects").click();
    const deleteObjects = page.getByRole("dialog", { name: "Delete objects" });
    await expect(deleteObjects.getByTestId("s3-delete-objects-confirm")).toBeDisabled();
    await deleteObjects.getByTestId("s3-delete-objects-confirm-input").fill("permanently delete");
    await deleteObjects.getByTestId("s3-delete-objects-confirm").click();
    await expect(page.getByText("Successfully deleted 1 object")).toBeVisible();
    await expect(objects).toContainText("No objects");

    await page.getByTestId("s3-bucket-delete").click();
    const deleteBucket = page.getByRole("dialog", { name: "Delete bucket" });
    await deleteBucket.getByTestId("s3-delete-bucket-confirm-input").fill(bucket);
    await deleteBucket.getByTestId("s3-delete-bucket-confirm").click();
    await expect(page).toHaveURL(/\/ui\/s3$/);
    await expect(page.getByText(`Successfully deleted bucket "${bucket}".`)).toBeVisible();
    await expect(table(page, "s3-buckets-table").getByRole("link", { name: bucket })).toHaveCount(0);
  });

  test("creates a bucket with versioning enabled and edits its tags", async ({ page }) => {
    const bucket = unique("e2e-versioned");
    await page.goto("/ui/s3/create");
    await page.getByTestId("s3-bucket-name-input").fill(bucket);
    await page.getByRole("radio", { name: "Enable" }).check();
    await page.getByRole("button", { name: "Add new tag" }).click();
    await page.getByTestId("s3-create-bucket-tag-key-0").fill("team");
    await page.getByTestId("s3-create-bucket-tag-value-0").fill("console");
    await page.getByTestId("s3-create-bucket-submit").click();
    await table(page, "s3-buckets-table").getByRole("link", { name: bucket }).click();
    await page.getByRole("tab", { name: "Properties" }).click();
    await expect(page.getByTestId("s3-bucket-summary")).toBeVisible();
    await expect(page.getByText("Enabled", { exact: true })).toBeVisible();
    await expect(page.getByRole("row", { name: /team/ })).toContainText("console");
    await page.getByTestId("s3-bucket-manage-tags").click();
    const dialog = page.getByRole("dialog", { name: "Edit bucket tags" });
    await dialog.getByTestId("s3-bucket-tag-value-0").fill("platform");
    await dialog.getByTestId("s3-bucket-tags-save").click();
    await expect(page.getByRole("row", { name: /team/ })).toContainText("platform");
  });

  test("refuses to delete a bucket that still holds objects, with S3's own error", async ({ page }) => {
    const bucket = unique("e2e-full");
    await page.goto("/ui/s3/create");
    await page.getByTestId("s3-bucket-name-input").fill(bucket);
    await page.getByTestId("s3-create-bucket-submit").click();
    await table(page, "s3-buckets-table").getByRole("link", { name: bucket }).click();
    await page.getByTestId("s3-upload-input").setInputFiles({ name: "keep.txt", mimeType: "text/plain", buffer: Buffer.from("x") });
    await expect(table(page, "s3-objects-table").getByRole("row", { name: /keep\.txt/ })).toBeVisible();
    await page.getByTestId("s3-bucket-delete").click();
    const dialog = page.getByRole("dialog", { name: "Delete bucket" });
    await dialog.getByTestId("s3-delete-bucket-confirm-input").fill(bucket);
    await dialog.getByTestId("s3-delete-bucket-confirm").click();
    await expect(dialog.getByRole("alert")).toContainText("BucketNotEmpty");
  });

  test("validates the bucket name before any request goes out", async ({ page }) => {
    await page.goto("/ui/s3/create");
    await page.getByTestId("s3-bucket-name-input").fill("Not_A_Valid_Name");
    await page.getByTestId("s3-create-bucket-submit").click();
    await expect(page.getByText(/Bucket name must be 3 to 63 characters/)).toBeVisible();
    await expect(page).toHaveURL(/\/ui\/s3\/create$/);
  });

  test("filters the bucket list with a property token and reports the match count", async ({ page }) => {
    const bucket = unique("e2e-filter");
    await page.goto("/ui/s3/create");
    await page.getByTestId("s3-bucket-name-input").fill(bucket);
    await page.getByTestId("s3-create-bucket-submit").click();
    await expect(table(page, "s3-buckets-table").getByRole("link", { name: bucket })).toBeVisible();
    const filter = page.getByRole("combobox", { name: "Find buckets by name" });
    await filter.fill(bucket);
    await filter.press("Enter");
    await expect(page.getByText("1 match", { exact: true }).first()).toBeVisible();
    await expect(table(page, "s3-buckets-table").getByRole("link")).toHaveCount(1);
    await page.getByRole("button", { name: "Clear filters" }).click();
    await expect(page.getByText("1 match", { exact: true })).toHaveCount(0);
  });
});

test.describe("IAM", () => {
  test("creates a policy, creates a role with it through the Create role wizard, and deletes both", async ({ page }) => {
    const policy = unique("e2e-policy");
    const role = unique("e2e-role");

    await page.goto("/ui/iam/policies");
    await page.getByTestId("iam-create-policy").click();
    await expect(page).toHaveURL(/\/ui\/iam\/policies\/create$/);
    await page.getByRole("textbox", { name: "Policy document" }).fill(
      JSON.stringify({ Version: "2012-10-17", Statement: [{ Effect: "Allow", Action: "s3:ListBucket", Resource: "*" }] }),
    );
    await page.getByTestId("iam-policy-name-input").fill(policy);
    await page.getByTestId("iam-create-policy-submit").click();
    await expect(page.getByText(`Policy ${policy} created.`)).toBeVisible();
    await expect(table(page, "iam-policies-table")).toContainText(policy);

    await page.goto("/ui/iam/roles");
    await page.getByTestId("iam-create-role").click();
    await expect(page.getByRole("heading", { level: 1, name: "Select trusted entity" })).toBeVisible();
    await page.getByRole("radio", { name: /AWS service/ }).check();
    await page.getByRole("button", { name: /Service or use case/ }).click();
    await page.getByRole("option", { name: "Lambda" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    await expect(page.getByRole("heading", { level: 1, name: "Add permissions" })).toBeVisible();
    await page.getByRole("searchbox", { name: "Filter by policy name" }).fill(policy);
    await page.getByRole("checkbox", { name: `Select ${policy}` }).check();
    await page.getByRole("button", { name: "Next" }).click();

    await expect(page.getByRole("heading", { level: 1, name: "Name, review, and create" })).toBeVisible();
    await page.getByTestId("iam-role-name-input").fill(role);
    await expect(page.getByTestId("iam-create-role-trust-policy")).toContainText('"Service": "lambda.amazonaws.com"');
    await page.getByRole("button", { name: "Create role" }).click();

    await expect(page).toHaveURL(/\/ui\/iam\/roles$/);
    await expect(page.getByText(`Role ${role} created.`)).toBeVisible();
    const roles = table(page, "iam-roles-table");
    await expect(roles.getByRole("row", { name: new RegExp(role) })).toContainText("AWS Service: lambda");

    await roles.getByRole("link", { name: role }).click();
    await expect(page.getByRole("heading", { level: 1, name: role })).toBeVisible();
    await expect(page.getByText(`arn:aws:iam::123456789012:role/${role}`)).toBeVisible();
    await expect(page.getByRole("row", { name: new RegExp(policy) })).toContainText("Customer managed");
    await page.getByRole("tab", { name: "Trust relationships" }).click();
    await expect(page.getByTestId("iam-role-trust-policy")).toContainText("sts:AssumeRole");

    await page.getByTestId("iam-role-delete").click();
    const dialog = page.getByRole("dialog", { name: `Delete ${role}?` });
    await expect(dialog.getByTestId("iam-delete-role-confirm")).toBeDisabled();
    await dialog.getByTestId("iam-delete-role-confirm-input").fill(role);
    await dialog.getByTestId("iam-delete-role-confirm").click();
    await expect(page).toHaveURL(/\/ui\/iam\/roles$/);
    await expect(page.getByText(`Role ${role} deleted.`)).toBeVisible();
    await expect(table(page, "iam-roles-table")).not.toContainText(role);

    await page.goto("/ui/iam/policies");
    await table(page, "iam-policies-table").getByRole("radio", { name: `Select arn:aws:iam::123456789012:policy/${policy}` }).check();
    await page.getByTestId("iam-delete-policy").click();
    const deletePolicy = page.getByRole("dialog", { name: `Delete ${policy}?` });
    await deletePolicy.getByTestId("iam-delete-policy-confirm-input").fill(policy);
    await deletePolicy.getByTestId("iam-delete-policy-confirm").click();
    await expect(page.getByText(`Policy ${policy} deleted.`)).toBeVisible();
    await expect(table(page, "iam-policies-table")).not.toContainText(policy);
  });

  test("builds a web identity trust policy from a registered identity provider", async ({ page }) => {
    await page.goto("/ui/iam/roles/create");
    await page.getByRole("radio", { name: /Web identity/ }).check();
    await page.getByRole("button", { name: /Identity provider/ }).click();
    await page.getByRole("option", { name: /127\.0\.0\.1:19311/ }).click();
    await page.getByRole("button", { name: /Audience/ }).click();
    await page.getByRole("option", { name: "aws-console" }).click();
    await page.getByRole("button", { name: "Next" }).click();
    await page.getByRole("button", { name: "Next" }).click();
    const trust = page.getByTestId("iam-create-role-trust-policy");
    await expect(trust).toContainText("sts:AssumeRoleWithWebIdentity");
    await expect(trust).toContainText('"127.0.0.1:19311/11111111-1111-1111-1111-111111111111/v2.0:aud": "aws-console"');
  });

  test("creates and deletes a user", async ({ page }) => {
    const user = unique("e2e-user");
    await page.goto("/ui/iam");
    await page.getByTestId("iam-create-user").click();
    const dialog = page.getByRole("dialog", { name: "Create user" });
    await dialog.getByTestId("iam-user-name-input").fill(user);
    await dialog.getByTestId("iam-create-user-submit").click();
    await expect(page.getByRole("link", { name: user })).toBeVisible();
    await page.getByRole("checkbox", { name: `Select ${user}` }).check();
    await page.getByTestId("iam-delete-user").click();
    const confirm = page.getByRole("dialog", { name: new RegExp(`Delete ${user}`) });
    await confirm.getByTestId("iam-delete-user-confirm-input").fill("confirm");
    await confirm.getByTestId("iam-delete-user-confirm").click();
    await expect(page.getByRole("link", { name: user })).toHaveCount(0);
  });
});

test.describe("Amazon EC2", () => {
  test("creates a key pair, downloads its private key, and deletes it", async ({ page }) => {
    const keyName = unique("e2e-key");
    await page.goto("/ui/ec2/key-pairs");
    await page.getByTestId("ec2-create-key-pair").click();
    await expect(page.getByRole("heading", { level: 1, name: "Create key pair" })).toBeVisible();
    await page.getByTestId("ec2-key-pair-name-input").fill(keyName);
    await page.getByRole("radio", { name: /ED25519/ }).check();
    const download = page.waitForEvent("download");
    await page.getByTestId("ec2-create-key-pair-submit").click();
    expect((await download).suggestedFilename()).toBe(`${keyName}.pem`);
    await expect(page).toHaveURL(/\/ui\/ec2\/key-pairs$/);
    await expect(page.getByText(`Successfully created key pair ${keyName}`)).toBeVisible();
    const row = table(page, "ec2-key-pairs-table").getByRole("row", { name: new RegExp(keyName) });
    await expect(row).toContainText("ed25519");

    await row.getByRole("checkbox").check();
    await page.getByTestId("ec2-delete-key-pair").click();
    const dialog = page.getByRole("dialog", { name: `Delete ${keyName}?` });
    await dialog.getByTestId("ec2-delete-key-pair-confirm-input").fill("Delete");
    await dialog.getByTestId("ec2-delete-key-pair-confirm").click();
    await expect(page.getByText(`Successfully deleted key pair ${keyName}`)).toBeVisible();
    await expect(table(page, "ec2-key-pairs-table").getByRole("row", { name: new RegExp(keyName) })).toHaveCount(0);
  });

  test("counts the Region's resources on the EC2 Dashboard and links each to its page", async ({ page }) => {
    await page.goto("/ui/ec2/home");
    await expect(page.getByRole("heading", { name: "Resources" })).toBeVisible();
    await expect(page.getByRole("link", { name: /^Security groups: \d+$/ })).toBeVisible();
    await page.getByRole("link", { name: /^Key pairs: \d+$/ }).click();
    await expect(page).toHaveURL(/\/ui\/ec2\/key-pairs$/);
  });

  test("offers the Instance state actions only for a selection", async ({ page }) => {
    await page.goto("/ui/ec2");
    await expect(page.getByRole("heading", { level: 1, name: /Instances/ })).toBeVisible();
    await expect(page.getByRole("button", { name: "Instance state", exact: true }).and(page.locator("[aria-haspopup]"))).toBeDisabled();
  });
});

// The create and delete dialogs the remaining services keep, each driven end
// to end against the simulator.
const DIALOG_FLOWS = [
  {
    service: "Amazon SQS",
    path: "/ui/sqs",
    create: "sqs-create-queue",
    dialog: "Create queue",
    nameInput: "sqs-queue-name-input",
    submit: "sqs-create-queue-submit",
    remove: "sqs-delete-queue",
    confirmWord: "confirm",
  },
  {
    service: "Amazon SNS",
    path: "/ui/sns",
    create: "sns-create-topic",
    dialog: "Create topic",
    nameInput: "sns-topic-name-input",
    submit: "sns-create-topic-submit",
    remove: "sns-delete-topic",
    confirmWord: "delete me",
  },
  {
    service: "Amazon ECR",
    path: "/ui/ecr",
    create: "ecr-create-repo",
    dialog: "Create repository",
    nameInput: "ecr-repo-name-input",
    submit: "ecr-create-repo-submit",
    remove: "ecr-delete-repo",
    confirmWord: "delete",
  },
  {
    service: "CloudWatch Logs",
    path: "/ui/logs",
    create: "logs-create-log-group",
    dialog: "Create log group",
    nameInput: "logs-log-group-name-input",
    submit: "logs-create-log-group-submit",
    remove: "logs-delete-log-group",
    confirmWord: "",
  },
];

for (const flow of DIALOG_FLOWS) {
  test(`${flow.service}: creates a resource from the console and deletes it`, async ({ page }) => {
    const name = unique("e2e");
    await page.goto(flow.path);
    await page.getByTestId(flow.create).click();
    const dialog = page.getByRole("dialog", { name: flow.dialog });
    await dialog.getByTestId(flow.nameInput).fill(name);
    await dialog.getByTestId(flow.submit).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const row = page.getByRole("row", { name: new RegExp(name) });
    await expect(row).toBeVisible();
    await row.getByRole("checkbox").check();
    await page.getByTestId(flow.remove).click();
    const confirm = page.getByRole("dialog");
    if (flow.confirmWord) {
      await expect(confirm.getByTestId(`${flow.remove}-confirm`)).toBeDisabled();
      await confirm.getByTestId(`${flow.remove}-confirm-input`).fill(flow.confirmWord);
    }
    await confirm.getByTestId(`${flow.remove}-confirm`).click();
    await expect(page.getByRole("row", { name: new RegExp(name) })).toHaveCount(0);
  });
}

test.describe("Table preferences", () => {
  test("hides a column and remembers the choice", async ({ page }) => {
    await page.goto("/ui/ec2/key-pairs");
    await expect(page.getByRole("columnheader", { name: "Fingerprint" })).toBeVisible();
    await page.getByRole("button", { name: "Preferences" }).click();
    const dialog = page.getByRole("dialog", { name: "Preferences" });
    await dialog.getByRole("checkbox", { name: "Fingerprint" }).click();
    await dialog.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByRole("columnheader", { name: "Fingerprint" })).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole("heading", { level: 1, name: /Key pairs/ })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: "Fingerprint" })).toHaveCount(0);
  });
});

// Every page of every service the console covers renders its heading over a
// successful, signed read: no load-failure alert anywhere.
const PAGES = SERVICES.filter((service) => service.supported !== false).flatMap((service) =>
  (service.nav ?? []).flatMap((section) => section.items.map((link) => ({ service: service.name, ...link }))),
);

test.describe("Service coverage", () => {
  for (const entry of PAGES) {
    test(`${entry.service} › ${entry.text} reads its resources and passes an accessibility audit`, async ({ page }) => {
      await page.goto(entry.href);
      await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();
      await expect(page.getByRole("navigation", { name: "Service navigation" }).getByRole("heading", { name: entry.service, exact: true })).toBeVisible();
      await expect(page.getByRole("progressbar")).toHaveCount(0);
      await expect(page.getByText(/^Could not load/)).toHaveCount(0);
      await expectNoAxeViolations(page);
    });
  }
});

test.describe("Accessibility in both visual modes", () => {
  const TARGETS: { name: string; path: string; open?: (page: Page) => Promise<void> }[] = [
    { name: "Console Home", path: "/ui/" },
    { name: "the not-supported page", path: "/ui/not-supported/eks" },
    { name: "Create bucket", path: "/ui/s3/create" },
    { name: "Create key pair", path: "/ui/ec2/key-pairs/create" },
    { name: "Create policy", path: "/ui/iam/policies/create" },
    { name: "the Create role wizard", path: "/ui/iam/roles/create" },
    {
      name: "the Services menu",
      path: "/ui/",
      open: async (page) => {
        await page.getByRole("button", { name: "Services" }).click();
        await expect(page.getByRole("dialog", { name: "Services" })).toBeVisible();
      },
    },
    {
      name: "the help panel",
      path: "/ui/lambda",
      open: async (page) => {
        await page.getByRole("button", { name: "Open help panel" }).click();
        await expect(page.getByRole("complementary", { name: "Help panel" }).getByRole("heading", { name: "Lambda", level: 2 })).toBeVisible();
      },
    },
    {
      name: "the Create user dialog",
      path: "/ui/iam",
      open: async (page) => {
        await page.getByTestId("iam-create-user").click();
        await expect(page.getByRole("dialog", { name: "Create user" })).toBeVisible();
        await page.waitForTimeout(400);
      },
    },
  ];
  for (const theme of ["light", "dark"] as const) {
    for (const target of TARGETS) {
      test(`${target.name} has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(target.path);
        await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();
        if (theme === "dark") await ensureTheme(page, "dark");
        if (target.open) await target.open(page);
        await expectNoAxeViolations(page);
      });
    }
  }
});

test.describe("Keyboard operability", () => {
  test("moves focus into a dialog on open and returns it to the trigger on Escape", async ({ page }) => {
    await page.goto("/ui/iam");
    const trigger = page.getByTestId("iam-create-user");
    await trigger.click();
    const dialog = page.getByRole("dialog", { name: "Create user" });
    await expect(dialog.getByRole("button", { name: "Close" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("carries a visible focus indicator on side navigation links", async ({ page }) => {
    await page.goto("/ui/iam");
    const link = page.getByRole("navigation", { name: "Service navigation" }).getByRole("link", { name: "Roles", exact: true });
    await expect
      .poll(async () => {
        await page.keyboard.press("Tab");
        await link.focus();
        return link.evaluate((el) => getComputedStyle(el).boxShadow + getComputedStyle(el).outlineStyle);
      })
      .not.toBe("nonenone");
  });
});
