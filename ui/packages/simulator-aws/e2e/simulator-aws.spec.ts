import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

/** Forces the console into the requested theme regardless of what the OS
 * colour-scheme preference or a prior test left it in, so a theme-fidelity
 * assertion never passes by accident. */
async function ensureTheme(page: Page, theme: "light" | "dark") {
  // Cloudscape animates its surface colours when the mode flips. A contrast
  // audit that samples mid-transition reads a transient blend of the two
  // themes — the side navigation's own background is still the lighter
  // surface while its link colour has already switched — and reports a
  // contrast failure for a pairing that never actually paints. Suppressing
  // transitions makes the flip instantaneous, so colours are measured at their
  // settled values deterministically rather than by out-waiting an animation,
  // which races the machine's speed and fails on a loaded CI runner.
  await page.evaluate(() => {
    const id = "e2e-disable-transitions";
    if (document.getElementById(id)) return;
    const style = document.createElement("style");
    style.id = id;
    style.textContent = "*, *::before, *::after { transition: none !important; animation: none !important; }";
    document.head.append(style);
  });
  const isDark = await page.evaluate(() => document.documentElement.classList.contains("dark"));
  if ((theme === "dark") !== isDark) {
    await page.getByRole("button", { name: /Switch to (light|dark) theme/ }).click();
  }
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(theme === "dark");
}

const SERVICES = [
  { path: "/ui/ecs", nav: "Elastic Container Service", title: "Tasks", columns: ["Task ARN", "Last status"] },
  { path: "/ui/lambda", nav: "Lambda", title: "Functions", columns: ["Function name", "State"] },
  { path: "/ui/ecr", nav: "Elastic Container Registry", title: "Repositories", columns: ["Repository name", "URI"] },
  { path: "/ui/s3", nav: "Simple Storage Service", title: "Buckets", columns: ["Name", "Creation date"] },
  { path: "/ui/logs", nav: "CloudWatch Logs", title: "Log groups", columns: ["Log group", "Retention"] },
  { path: "/ui/organizations", nav: "AWS Organizations", title: "AWS accounts", columns: ["Account name", "Account ID", "Email"] },
  { path: "/ui/iam", nav: "Identity and Access Management", title: "Users", columns: ["User name", "ARN"] },
];

test.describe("AWS console shell", () => {
  test("presents the console header, breadcrumbs and grouped service navigation", async ({ page }) => {
    await page.goto("/ui/");
    expect((await page.request.get("/ui/favicon.svg")).status()).toBe(200);
    await expect(page.locator(".aws-header")).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toBeVisible();
    const nav = page.getByRole("navigation", { name: "Service" });
    // Grouped the way AWS's own "All services" menu groups them — Containers
    // and Storage are separate categories there, not the simulator's own
    // "Storage and registry" shorthand — since the nav now lists AWS's real
    // catalog, supported and not, rather than only the services sockerless
    // implements.
    for (const group of [
      "Dashboard",
      "Compute",
      "Containers",
      "Storage",
      "Database",
      "Networking & content delivery",
      "Application integration",
      "Management & Governance",
      "Security, identity, and compliance",
    ]) {
      await expect(nav.getByText(group, { exact: true })).toBeVisible();
    }
  });

  // The header used to be a fixed 40px tall. Anything taller than that — the
  // account control in particular — spilled below it, and the breadcrumb bar
  // painted over the overflow. A control covered by an opaque sibling still
  // reports as visible, so the failure surfaced only as clicks landing on the
  // breadcrumbs instead of the sign-out control. Assert containment, which is
  // the property that actually broke.
  test("contains every header control within the header itself", async ({ page }) => {
    await page.goto("/ui/");
    const header = page.locator(".aws-header");
    const headerBox = await header.boundingBox();
    expect(headerBox).not.toBeNull();
    const controls = header.locator(":scope > * > *");
    const count = await controls.count();
    expect(count).toBeGreaterThan(0);
    for (let index = 0; index < count; index += 1) {
      const control = controls.nth(index);
      const box = await control.boundingBox();
      if (!box) continue;
      const label = await control.evaluate((node) => node.outerHTML.slice(0, 80));
      expect(box.y, `header control escaped the header: ${label}`).toBeGreaterThanOrEqual(headerBox!.y - 0.5);
      expect(box.y + box.height, `header control escaped the header: ${label}`).toBeLessThanOrEqual(
        headerBox!.y + headerBox!.height + 0.5,
      );
    }
  });

  test("puts the theme control in the top right and switches both ways", async ({ page }) => {
    await page.goto("/ui/");
    const toggle = page.getByRole("button", { name: /Switch to (light|dark) theme/ });
    const headerBox = await page.locator(".aws-header").boundingBox();
    const toggleBox = await toggle.boundingBox();
    expect(toggleBox!.x).toBeGreaterThan(headerBox!.x + headerBox!.width / 2);

    const isDark = () => page.evaluate(() => document.documentElement.classList.contains("dark"));
    const before = await isDark();
    await toggle.click();
    expect(await isDark()).toBe(!before);
    await toggle.click();
    expect(await isDark()).toBe(before);
  });

  test("keeps the AWS S3 root protocol surface separate from /ui/", async ({ page }) => {
    const response = await page.goto("/");
    expect(page.url()).toBe("http://localhost:19310/");
    expect(response?.status()).toBe(200);
    await expect(page.locator("body")).toContainText("ListAllMyBucketsResult");
  });

  test("exposes a skip link ahead of the console content", async ({ page }) => {
    await page.goto("/ui/");
    await page.locator("body").focus();
    await page.keyboard.press("Tab");
    await expect(page.locator(".sl-skip-link")).toBeFocused();
    await expect(page.locator("#main-content")).toHaveCount(1);
  });
});

// These pin real, rendered Cloudscape output — the console's font, action
// blue, rounded containers, and header/table icon controls — against the
// live component, not a hand-authored approximation of it. Where an exact
// hex value would require reverse-engineering Cloudscape's own build-hashed
// internal CSS custom properties (private to the installed package version,
// see tokens.css), the check instead measures the rendered contrast ratio —
// still what actually paints, just not pinned to one literal value.
test.describe("Cloudscape visual fidelity", () => {
  test("renders console text in Open Sans, Cloudscape's own font", async ({ page }) => {
    await page.goto("/ui/");
    // Cloudscape applies its font-family token per component rather than
    // once globally, so the console's own root reasserts the same token
    // (see tokens.css) for the text it draws directly — its heading — as
    // well as any Cloudscape `Link` rendered inside it.
    const heading = await page.getByRole("heading", { level: 1 }).first().evaluate((node) => getComputedStyle(node).fontFamily);
    expect(heading).toContain("Open Sans");
    const link = await page
      .getByRole("navigation", { name: "Service" })
      .getByRole("link", { name: "Overview" })
      .evaluate((node) => getComputedStyle(node).fontFamily);
    expect(link).toContain("Open Sans");
  });

  test("visually distinguishes the current service in the side navigation from every other, clickable one", async ({ page }) => {
    await page.goto("/ui/lambda");
    const nav = page.getByRole("navigation", { name: "Service" });
    const colorOf = (name: string) =>
      nav
        .getByRole("link", { name, exact: true })
        .locator("span, span > *")
        .last()
        .evaluate((node) => getComputedStyle(node).color);
    const current = await colorOf("Lambda");
    const other = await colorOf("Elastic Container Service");
    // The current page's own link text reads as plain body text (dark, not
    // link-blue) — an operator does not need to distinguish "the page I'm
    // on" from "a link I could click" by aria-current alone.
    expect(current).not.toBe(other);
    expect(current).toBe("rgb(15, 20, 26)"); // color-text-label
  });

  test("rounds containers the way the current Cloudscape theme does", async ({ page }) => {
    await page.goto("/ui/ecs");
    const radius = await page
      .locator('[class*="awsui_root"][class*="awsui_variant-default"]')
      .first()
      .evaluate((node) => getComputedStyle(node).borderTopLeftRadius);
    expect(radius).toBe("16px");
  });

  test("carries the global search field and header tool icons", async ({ page }) => {
    await page.goto("/ui/");
    const header = page.locator(".aws-header");
    await expect(header.getByRole("searchbox", { name: "Search" })).toBeVisible();
    for (const label of ["Notifications", "Settings", "Support"]) {
      const button = header.getByRole("button", { name: label });
      await expect(button).toBeVisible();
      await expect(button.locator("svg")).toHaveCount(1);
    }
  });

  test("gives the table a search-prefixed filter and a refresh control", async ({ page }) => {
    await page.goto("/ui/ecs");
    const filter = page.getByRole("searchbox", { name: "Find tasks" });
    await expect(filter).toBeVisible();
    await expect(filter.locator("xpath=ancestor::*[1]").locator("svg")).toHaveCount(1);
    await expect(page.getByTestId("ecs-tasks-table").getByRole("button", { name: "Refresh" })).toBeVisible();
  });
});

test.describe("Overview", () => {
  test("states the Region alongside the resource counts", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
    await expect(page.getByText("us-east-1").first()).toBeVisible();
  });
});

for (const service of SERVICES) {
  test.describe(service.nav, () => {
    test("renders its heading and table columns", async ({ page }) => {
      await page.goto(service.path);
      await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
      for (const column of service.columns) {
        await expect(page.getByRole("columnheader", { name: column })).toBeVisible();
      }
    });
  });
}

// The Users page's credential-minting affordances are part of the shell — the
// header actions and the create dialog render before (and regardless of) any
// cloud read, so they are assertable here without an identity provider. The
// authenticated mint→configure→signed-read loop belongs to the Shauth
// relying-party suite.
test.describe("Identity and Access Management credential minting", () => {
  test("offers Create user and opens the create dialog", async ({ page }) => {
    await page.goto("/ui/iam");
    const create = page.getByTestId("iam-create-user");
    await expect(create).toBeVisible();
    await expect(page.getByTestId("iam-delete-user")).toBeDisabled();
    await create.click();
    const dialog = page.getByRole("dialog", { name: "Create user" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByTestId("iam-user-name-input")).toBeVisible();
    // An empty or invalid user name must not be submittable.
    await expect(dialog.getByTestId("iam-create-user-submit")).toBeDisabled();
    await dialog.getByTestId("iam-user-name-input").fill("cli-operator");
    await expect(dialog.getByTestId("iam-create-user-submit")).toBeEnabled();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("routes to a user's Security credentials page with the access-key table and Create access key", async ({
    page,
  }) => {
    await page.goto("/ui/iam/users/cli-operator");
    await expect(page.getByRole("heading", { name: "cli-operator" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Access keys" })).toBeVisible();
    for (const column of ["Access key ID", "Status", "Created"]) {
      await expect(page.getByRole("columnheader", { name: column })).toBeVisible();
    }
    await expect(page.getByTestId("iam-create-access-key")).toBeVisible();
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    await expect(crumbs).toContainText("Identity and Access Management");
    await expect(crumbs).toContainText("cli-operator");
  });
});

// The Organizations page's account-management affordances are part of the
// shell — the header actions and the Add an AWS account dialog render before
// (and regardless of) any cloud read. The authenticated
// CreateAccount→DescribeCreateAccountStatus→ListAccounts loop belongs to the
// Shauth relying-party suite.
test.describe("AWS Organizations account management", () => {
  test("offers Add an AWS account and opens the add dialog", async ({ page }) => {
    await page.goto("/ui/organizations");
    await expect(page.getByTestId("org-accounts-table")).toBeVisible();
    await expect(page.getByTestId("org-remove-account")).toBeDisabled();
    await expect(page.getByTestId("org-close-account")).toBeDisabled();
    const add = page.getByTestId("org-add-account");
    await expect(add).toBeVisible();
    await add.click();
    const dialog = page.getByRole("dialog", { name: "Add an AWS account" });
    await expect(dialog).toBeVisible();
    // Neither field alone is enough: CreateAccount requires the account name
    // and the owner email, so the submit stays disabled until both are valid.
    await expect(dialog.getByTestId("org-add-account-submit")).toBeDisabled();
    await dialog.getByTestId("org-account-name-input").fill("Sandbox");
    await expect(dialog.getByTestId("org-add-account-submit")).toBeDisabled();
    await dialog.getByTestId("org-account-email-input").fill("sandbox@sim.invalid");
    await expect(dialog.getByTestId("org-add-account-submit")).toBeEnabled();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("routes to an account's detail page with the Organizations breadcrumb", async ({ page }) => {
    await page.goto("/ui/organizations/accounts/123456789012");
    await expect(page.getByRole("heading", { name: "123456789012" })).toBeVisible();
    await expect(page.getByTestId("org-remove-account")).toBeVisible();
    await expect(page.getByTestId("org-close-account")).toBeVisible();
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    await expect(crumbs).toContainText("AWS Organizations");
    await expect(crumbs).toContainText("123456789012");
  });
});

// Amazon S3, Amazon ECR, and CloudWatch Logs used to be read-only in this
// console — list and detail, no way to create a resource, even though the
// real console lets an operator create a bucket, a repository, or a log
// group from the same page. Each now offers a real "Create …" header action
// that opens a Cloudscape Modal wired to the real CreateBucket /
// CreateRepository / CreateLogGroup operation, matching the IAM "Create
// user" and Organizations "Add an AWS account" dialogs above: the header
// action and the dialog render before (and regardless of) any cloud read, so
// they are assertable here without an identity provider. The authenticated
// create→list-appears loop is exercised by the mocked-federated-fetch suite
// (ResourceHeaderActions.test.tsx), the same split the IAM/Organizations
// dialogs above document.
test.describe("Resource creation", () => {
  const cases = [
    {
      path: "/ui/s3",
      createTestId: "s3-create-bucket",
      dialogName: "Create bucket",
      inputTestId: "s3-bucket-name-input",
      submitTestId: "s3-create-bucket-submit",
      validName: "my-new-bucket",
    },
    {
      path: "/ui/ecr",
      createTestId: "ecr-create-repo",
      dialogName: "Create repository",
      inputTestId: "ecr-repo-name-input",
      submitTestId: "ecr-create-repo-submit",
      validName: "my-new-repo",
    },
    {
      path: "/ui/logs",
      createTestId: "logs-create-log-group",
      dialogName: "Create log group",
      inputTestId: "logs-log-group-name-input",
      submitTestId: "logs-create-log-group-submit",
      validName: "/ecs/my-new-task",
    },
  ];

  for (const { path, createTestId, dialogName, inputTestId, submitTestId, validName } of cases) {
    test(`${path} offers ${dialogName} and opens the create dialog with an initially-disabled submit`, async ({
      page,
    }) => {
      await page.goto(path);
      const create = page.getByTestId(createTestId);
      await expect(create).toBeVisible();
      await create.click();
      const dialog = page.getByRole("dialog", { name: dialogName });
      await expect(dialog).toBeVisible();
      const input = dialog.getByTestId(inputTestId);
      await expect(input).toBeVisible();
      // An empty or invalid name must not be submittable.
      await expect(dialog.getByTestId(submitTestId)).toBeDisabled();
      await input.fill(validName);
      await expect(dialog.getByTestId(submitTestId)).toBeEnabled();
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(page.getByRole("dialog")).toHaveCount(0);
    });

    test(`${path} traps focus in the ${dialogName} dialog and returns it to the trigger on Escape`, async ({ page }) => {
      await page.goto(path);
      const trigger = page.getByTestId(createTestId);
      await trigger.click();
      const dialog = page.getByRole("dialog", { name: dialogName });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole("button", { name: "Close" })).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(trigger).toBeFocused();
    });
  }
});

// The console full-parity pass added the compute-resource creates the earlier
// passes deferred: AWS Lambda "Create function" (CreateFunction) and Amazon ECS
// "Run new task" (RegisterTaskDefinition + RunTask). Like the storage creates
// above, the header action and the dialog render before (and regardless of) any
// cloud read, so the shell — the trigger, the form fields, the initially
// disabled submit, and focus management — is assertable here without an
// identity provider. The authenticated create→appears-in-list loop belongs to
// the Shauth relying-party suite; the request shaping is pinned in the
// per-package vitest suite (ResourceEditActions.test.tsx).
test.describe("Compute resource creation", () => {
  test("Lambda offers Create function and validates the container-image form", async ({ page }) => {
    await page.goto("/ui/lambda");
    const create = page.getByTestId("lambda-create-function");
    await expect(create).toBeVisible();
    await create.click();
    const dialog = page.getByRole("dialog", { name: "Create function" });
    await expect(dialog).toBeVisible();
    // Container image is the default package type; name + image URI + role are
    // all required before CreateFunction can go out.
    await expect(dialog.getByTestId("lambda-create-function-submit")).toBeDisabled();
    await dialog.getByTestId("lambda-function-name-input").fill("my-fn");
    await dialog.getByTestId("lambda-image-uri-input").fill("123456789012.dkr.ecr.us-east-1.amazonaws.com/app:latest");
    await expect(dialog.getByTestId("lambda-create-function-submit")).toBeDisabled();
    await dialog.getByTestId("lambda-role-input").fill("arn:aws:iam::123456789012:role/lambda-role");
    await expect(dialog.getByTestId("lambda-create-function-submit")).toBeEnabled();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("Lambda Create function traps focus and returns it to the trigger on Escape", async ({ page }) => {
    await page.goto("/ui/lambda");
    const trigger = page.getByTestId("lambda-create-function");
    await trigger.click();
    const dialog = page.getByRole("dialog", { name: "Create function" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Close" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("ECS offers Run new task and opens the run-task form with an initially disabled submit", async ({ page }) => {
    await page.goto("/ui/ecs");
    const run = page.getByTestId("ecs-run-task");
    await expect(run).toBeVisible();
    await run.click();
    const dialog = page.getByRole("dialog", { name: "Run new task" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByTestId("ecs-run-task-cluster")).toBeVisible();
    await expect(dialog.getByTestId("ecs-run-task-launch-type")).toBeVisible();
    // No cluster is selectable without a cloud read, so RunTask can never fire
    // from this unauthenticated shell — the submit stays disabled.
    await expect(dialog.getByTestId("ecs-run-task-submit")).toBeDisabled();
    // Switching to the inline task-definition mode reveals its fields.
    await dialog.getByRole("radio", { name: "Define a new task definition" }).click();
    await expect(dialog.getByTestId("ecs-run-task-new-family")).toBeVisible();
    await expect(dialog.getByTestId("ecs-run-task-image")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("ECS renders service scheduling and deployment controls alongside tasks", async ({ page }) => {
    await page.goto("/ui/ecs");
    await expect(page.getByRole("heading", { name: "Services", exact: true })).toBeVisible();
    for (const column of ["Service name", "Desired tasks", "Running tasks", "Deployments"]) {
      await expect(page.getByRole("columnheader", { name: column, exact: true })).toBeVisible();
    }
  });

  test("ECS service details expose scaling, deletion, deployments, events, alarms, and service discovery", async ({
    page,
  }) => {
    await page.goto("/ui/ecs/services/default/web");
    await expect(page.getByRole("heading", { name: "web", exact: true })).toBeVisible();
    await expect(page.getByTestId("ecs-service-delete")).toBeDisabled();
    for (const heading of ["Deployments", "Service events", "Deployment configuration", "Service discovery"]) {
      await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
    }
    await expect(page.getByTestId("ecs-service-error")).toBeVisible();
  });
});

// Every resource detail page grew the real update and lifecycle actions its
// service's console offers — Lambda Test/Edit/Manage tags, CloudWatch Logs Edit
// retention, S3 Edit versioning/Manage tags, ECR Edit/Manage tags — alongside
// the existing Delete/Stop. Each opens a Cloudscape dialog wired to a real
// cloud operation once the resource has loaded. This unauthenticated suite has
// no identity provider, so the read is rejected and the actions render disabled
// (they gate on a successful read); this pins that they render, in the right
// disabled state, without a live cloud read. The authenticated open→edit→save
// loops belong to the Shauth relying-party suite, and the request shaping to
// the per-package vitest suite (ResourceEditActions.test.tsx).
test.describe("Resource detail update actions", () => {
  const cases = [
    { path: "/ui/lambda/my-fn", testIds: ["lambda-function-test", "lambda-function-edit", "lambda-function-delete"] },
    { path: "/ui/logs/" + encodeURIComponent("/ecs/my-task"), testIds: ["logs-log-group-edit-retention", "logs-log-group-delete"] },
    { path: "/ui/s3/my-bucket", testIds: ["s3-bucket-edit-versioning", "s3-bucket-manage-tags", "s3-bucket-delete"] },
    { path: "/ui/ecr/my-repo", testIds: ["ecr-repo-edit", "ecr-repo-manage-tags", "ecr-repo-delete"] },
  ];
  for (const { path, testIds } of cases) {
    test(`${path} renders its real, initially-disabled update actions without a live cloud read`, async ({ page }) => {
      await page.goto(path);
      for (const testId of testIds) {
        const action = page.getByTestId(testId);
        await expect(action).toBeVisible();
        await expect(action).toBeDisabled();
      }
    });
  }
});

// The Amazon ECS, AWS Lambda, Amazon ECR, Amazon S3, and CloudWatch Logs
// pages used to render AwsTable's default "View details" and "Delete" header
// actions — enabled, but with no handler wired (BUG-2637). Each page now
// passes its own `actions`: a real "View details" that navigates to the
// resource's detail route, and the real mutation (Stop for ECS tasks, Delete
// elsewhere) — both disabled until a row is selected, and neither the AwsTable
// default. Like the IAM and Organizations header-action checks above, this is
// assertable without an identity provider — reading zero rows still renders
// the header controls. The authenticated select→confirm→mutate and
// select→View details→navigate loops are exercised in the Shauth
// relying-party suite (real data) and in the per-package vitest suite
// (mocked federated fetch); this suite pins that the controls render, in the
// right disabled state, without a live cloud read.
test.describe("Resource header actions", () => {
  const cases = [
    { path: "/ui/ecs", viewTestId: "ecs-view-task", actionTestId: "ecs-stop-task", actionLabel: "Stop" },
    { path: "/ui/lambda", viewTestId: "lambda-view-function", actionTestId: "lambda-delete-function", actionLabel: "Delete" },
    { path: "/ui/ecr", viewTestId: "ecr-view-repo", actionTestId: "ecr-delete-repo", actionLabel: "Delete" },
    { path: "/ui/s3", viewTestId: "s3-view-bucket", actionTestId: "s3-delete-bucket", actionLabel: "Delete" },
    { path: "/ui/logs", viewTestId: "logs-view-log-group", actionTestId: "logs-delete-log-group", actionLabel: "Delete" },
  ];

  for (const { path, viewTestId, actionTestId, actionLabel } of cases) {
    test(`${path} wires a real, initially-disabled View details and ${actionLabel} action`, async ({ page }) => {
      await page.goto(path);
      const view = page.getByTestId(viewTestId);
      await expect(view).toBeVisible();
      await expect(view).toHaveText("View details");
      await expect(view).toBeDisabled();
      const action = page.getByTestId(actionTestId);
      await expect(action).toBeVisible();
      await expect(action).toHaveText(actionLabel);
      await expect(action).toBeDisabled();
      if (actionLabel !== "Delete") {
        await expect(page.getByRole("button", { name: "Delete", exact: true })).toHaveCount(0);
      }
    });
  }
});

// Pass 1 dropped "View details" because no detail pages existed. Pass 2 adds
// one real, API-wired detail route per supported service — ECS task, Lambda
// function, ECR repository, S3 bucket, CloudWatch Logs log group — each
// reachable by encoded identifier and rendering a Cloudscape-shaped
// breadcrumb → page header → properties → sub-resource(s) layout. This suite
// has no identity provider (see the file-level comment at the bottom), so the
// simulator rejects the unsigned read exactly as real AWS would; these tests
// pin the shell that renders regardless — heading, breadcrumb, and a real,
// initially-disabled action — while the authenticated real-data render is
// exercised by the Shauth relying-party suite and the mocked-fetch unit
// suite (ResourceHeaderActions.test.tsx).
const DETAIL_PAGES = [
  {
    id: "arn:aws:ecs:us-east-1:123456789012:task/default/abc123",
    prefix: "/ui/ecs/",
    crumbService: "Elastic Container Service",
    actionTestId: "ecs-task-stop",
    errorTestId: "ecs-task-error",
  },
  {
    id: "my-function",
    prefix: "/ui/lambda/",
    crumbService: "Lambda",
    actionTestId: "lambda-function-delete",
    errorTestId: "lambda-function-error",
  },
  {
    id: "my-repo",
    prefix: "/ui/ecr/",
    crumbService: "Elastic Container Registry",
    actionTestId: "ecr-repo-delete",
    errorTestId: "ecr-repo-error",
  },
  {
    id: "my-bucket",
    prefix: "/ui/s3/",
    crumbService: "Simple Storage Service",
    actionTestId: "s3-bucket-delete",
    errorTestId: "s3-bucket-error",
  },
  {
    id: "/ecs/my-task",
    prefix: "/ui/logs/",
    crumbService: "CloudWatch Logs",
    actionTestId: "logs-log-group-delete",
    errorTestId: "logs-log-group-error",
  },
];

test.describe("Resource detail pages", () => {
  for (const detail of DETAIL_PAGES) {
    test(`${detail.prefix}:id renders the resource heading, breadcrumb, and a disabled action without a live cloud read`, async ({
      page,
    }) => {
      await page.goto(`${detail.prefix}${encodeURIComponent(detail.id)}`);
      await expect(page.getByRole("heading", { name: detail.id, exact: true })).toBeVisible();
      const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
      await expect(crumbs).toContainText(detail.crumbService);
      await expect(crumbs).toContainText(detail.id);
      const action = page.getByTestId(detail.actionTestId);
      await expect(action).toBeVisible();
      await expect(action).toBeDisabled();
      // Unauthenticated in this suite (no identity provider) — the simulator
      // rejects the read exactly as real AWS would, and the page reports the
      // failure rather than rendering an empty or falsely-successful state.
      await expect(page.getByTestId(detail.errorTestId)).toBeVisible();
    });

    test(`${detail.prefix}:id passes an automated accessibility audit in both themes`, async ({ page }) => {
      for (const theme of ["light", "dark"] as const) {
        await page.goto(`${detail.prefix}${encodeURIComponent(detail.id)}`);
        if (theme === "dark") await ensureTheme(page, "dark");
        await expect(page.getByTestId(detail.errorTestId)).toBeVisible();
        const results = await new AxeBuilder({ page }).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      }
    });
  }
});

test.describe("Navigation", () => {
  test("reaches every service from the side navigation and updates the breadcrumb", async ({ page }) => {
    await page.goto("/ui/");
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    for (const service of SERVICES) {
      await page.getByRole("link", { name: service.nav, exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`${service.path}$`));
      await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
      await expect(crumbs).toContainText(service.nav);
    }
    await page.getByRole("link", { name: "Overview", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  });
});

// The header's "Services" trigger opens the mega-menu overlay: a full-width
// dialog docked under the header, reusing the exact NAV_GROUPS catalog the
// side navigation renders (same groups, same supported/"Not supported"
// treatment), with a live search field that narrows it. The side navigation
// stays the current-section affordance; this is the global, from-anywhere
// one, matching the real console's split between the two.
test.describe("Services mega-menu", () => {
  test("opens from the header trigger with aria-haspopup/aria-expanded, focuses the search field, and closes on Escape returning focus to the trigger", async ({
    page,
  }) => {
    await page.goto("/ui/");
    const trigger = page.getByRole("button", { name: "Services" });
    await expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
    await expect(trigger).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await trigger.click();
    await expect(trigger).toHaveAttribute("aria-expanded", "true");
    const dialog = page.getByRole("dialog", { name: "All services" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("searchbox", { name: "Search services" })).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(trigger).toHaveAttribute("aria-expanded", "false");
    await expect(trigger).toBeFocused();
  });

  test("toggles closed on a second click of the trigger", async ({ page }) => {
    await page.goto("/ui/");
    const trigger = page.getByRole("button", { name: "Services" });
    await trigger.click();
    await expect(page.getByRole("dialog", { name: "All services" })).toBeVisible();
    await trigger.click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("dismisses on an outside click", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    await expect(page.getByRole("dialog", { name: "All services" })).toBeVisible();
    // The panel docks under the header and can grow tall enough to cover the
    // page body beneath it (the catalog has plenty of rows), so "outside"
    // has to be a point the panel never reaches: the header's own account
    // cluster, which sits above the panel's top edge.
    await page.locator(".aws-header-region").click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("traps Tab focus inside the open panel, wrapping from the last link back to the search field and vice versa", async ({
    page,
  }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    const dialog = page.getByRole("dialog", { name: "All services" });
    const search = dialog.getByRole("searchbox", { name: "Search services" });
    const lastLink = dialog.getByRole("link").last();

    await expect(search).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(lastLink).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(search).toBeFocused();
  });

  test("filters the catalog live as the operator types, narrowing to matching services and dropping empty categories", async ({
    page,
  }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    const dialog = page.getByRole("dialog", { name: "All services" });
    await expect(dialog.getByRole("link", { name: "Lambda", exact: true })).toBeVisible();
    await expect(dialog.getByRole("link", { name: "Elastic Container Service", exact: true })).toBeVisible();

    await dialog.getByRole("searchbox", { name: "Search services" }).fill("lambda");
    await expect(dialog.getByRole("link", { name: "Lambda", exact: true })).toBeVisible();
    await expect(dialog.getByRole("link", { name: "Elastic Container Service", exact: true })).toHaveCount(0);
    await expect(dialog.getByText("Compute", { exact: true })).toBeVisible();
    await expect(dialog.getByText("Containers", { exact: true })).toHaveCount(0);

    await dialog.getByRole("searchbox", { name: "Search services" }).fill("zzzznotaservice");
    await expect(dialog.getByText('No services match "zzzznotaservice".')).toBeVisible();

    await dialog.getByRole("searchbox", { name: "Search services" }).fill("");
    await expect(dialog.getByRole("link", { name: "Elastic Container Service", exact: true })).toBeVisible();
  });

  test("resets the search field to empty every time the panel re-opens", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    let dialog = page.getByRole("dialog", { name: "All services" });
    await dialog.getByRole("searchbox", { name: "Search services" }).fill("lambda");
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Services" }).click();
    dialog = page.getByRole("dialog", { name: "All services" });
    await expect(dialog.getByRole("searchbox", { name: "Search services" })).toHaveValue("");
    await expect(dialog.getByRole("link", { name: "Elastic Container Service", exact: true })).toBeVisible();
  });

  test("a supported service link navigates to its real page and closes the menu", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    const dialog = page.getByRole("dialog", { name: "All services" });
    await dialog.getByRole("link", { name: "Lambda", exact: true }).click();
    await expect(page).toHaveURL(/\/ui\/lambda$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Functions" })).toBeVisible();
  });

  test("an unsupported service link routes to the honest not-supported page", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    const dialog = page.getByRole("dialog", { name: "All services" });
    const link = dialog.getByRole("link", { name: "Elastic Kubernetes Service, not supported in this simulator" });
    // The "Not supported" pill sits beside the link (not fused into its
    // accessible name — see AwsConsole.tsx's AwsServicesMenu/AwsSideNavigation
    // comments), so it's read from the enclosing list item.
    await expect(link.locator("xpath=ancestor::li[1]").getByText("Not supported")).toBeVisible();
    await link.click();
    await expect(page).toHaveURL(/\/ui\/not-supported\/eks$/);
    await expect(page.getByRole("heading", { name: "Elastic Kubernetes Service" })).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("passes an automated accessibility audit while open, in both themes", async ({ page }) => {
    for (const theme of ["light", "dark"] as const) {
      await page.goto("/ui/");
      if (theme === "dark") await ensureTheme(page, "dark");
      await page.getByRole("button", { name: "Services" }).click();
      await expect(page.getByRole("dialog", { name: "All services" })).toBeVisible();
      const results = await new AxeBuilder({ page }).analyze();
      expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
    }
  });

  test("clears WCAG AA contrast for the panel's search field, group labels, service links, and not-supported badge in both themes", async ({
    page,
  }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Services" }).click();
    await expect(page.getByRole("dialog", { name: "All services" })).toBeVisible();
    const results = await measureContrast(page, [
      ".aws-services-search input",
      '[role="dialog"] h3',
      '[role="dialog"] a[href="/ui/lambda"]',
      '[class*="badge-color-grey"]',
    ]);
    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBe(4);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    }
  });
});

// The real AWS console lists its full service catalog and is honest about
// what an account can and cannot use; this simulator does the same for what
// it does and does not implement. These tests pin that contract: a service
// AWS offers but sockerless has not built stays in the nav, reachable, and
// visibly and audibly marked as unsupported, rather than hidden or silently
// broken.
test.describe("Not-supported services", () => {
  test("marks an unimplemented AWS service with a visible pill and an accessible name that states it, and routes to an honest page", async ({
    page,
  }) => {
    await page.goto("/ui/");
    const nav = page.getByRole("navigation", { name: "Service" });
    const link = nav.getByRole("link", { name: "Elastic Kubernetes Service, not supported in this simulator" });
    await expect(link).toBeVisible();
    await expect(link.locator("xpath=ancestor::li[1]").getByText("Not supported")).toBeVisible();
    await link.click();
    await expect(page).toHaveURL(/\/ui\/not-supported\/eks$/);
    await expect(page.getByRole("heading", { name: "Elastic Kubernetes Service" })).toBeVisible();
    await expect(page.getByText("This service is not implemented by the Sockerless simulator.")).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toContainText("Elastic Kubernetes Service");
  });

  test("names the service from the catalog when the not-supported page is reached directly by URL", async ({ page }) => {
    await page.goto("/ui/not-supported/cloudformation");
    await expect(page.getByRole("heading", { name: "CloudFormation" })).toBeVisible();
    await expect(page.getByLabel("CloudFormation is not supported in this simulator")).toBeVisible();
  });

  test("renders no not-supported pill on any service sockerless implements", async ({ page }) => {
    await page.goto("/ui/");
    const nav = page.getByRole("navigation", { name: "Service" });
    for (const service of SERVICES) {
      const link = nav.getByRole("link", { name: service.nav, exact: true });
      await expect(link.locator("xpath=ancestor::li[1]").getByText("Not supported")).toHaveCount(0);
    }
  });
});

test.describe("Accessibility landmarks and keyboard operability", () => {
  test("exposes banner, service navigation, breadcrumb navigation, and main landmarks", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("banner")).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Service" })).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toBeVisible();
    await expect(page.getByRole("main")).toBeVisible();
  });

  test("marks the current service with aria-current, and no other service", async ({ page }) => {
    await page.goto("/ui/lambda");
    const nav = page.getByRole("navigation", { name: "Service" });
    await expect(nav.getByRole("link", { name: "Lambda", exact: true })).toHaveAttribute("aria-current", "page");
    await expect(nav.getByRole("link", { name: "Elastic Container Service", exact: true })).not.toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  // AppLayout renders the navigation-open and navigation-close controls as
  // two separate real buttons (Cloudscape's own pattern) rather than one
  // toggle with a changing label: the "Open navigation" trigger is present
  // but `aria-hidden` while the drawer is open, and vice versa for "Close
  // navigation" — so exactly one of the two is ever exposed to the
  // accessibility tree (and therefore to a role-based query) at a time.
  test("toggles the side navigation via its own open/close controls", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("button", { name: "Close navigation" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open navigation" })).toHaveCount(0);
    await page.getByRole("button", { name: "Close navigation" }).click();
    await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Close navigation" })).toHaveCount(0);
    await page.getByRole("button", { name: "Open navigation" }).click();
    await expect(page.getByRole("button", { name: "Close navigation" })).toBeVisible();
  });

  test("carries a visible focus indicator that clears 3:1 in both themes", async ({ page }) => {
    await page.goto("/ui/");
    // Cloudscape's own focus ring only renders once its focus-visible
    // polyfill has seen a real keyboard interaction (it gates the ring's
    // CSS behind `body[data-awsui-focus-visible=true]`, toggled by that
    // polyfill, not by `:focus` alone) — one Tab press anywhere arms it,
    // same as a real keyboard user's first Tab press would.
    await page.keyboard.press("Tab");
    const link = page.getByRole("navigation", { name: "Service" }).getByRole("link", { name: "Overview" });
    await link.focus();
    // Cloudscape's own focus ring is a `box-shadow`, not the native
    // `outline` this console's hand-built links used to draw — still a
    // visible, non-colour-only indicator, just Cloudscape's real mechanism
    // for it.
    const focusRing = await link.evaluate((el) => getComputedStyle(el).boxShadow);
    expect(focusRing).not.toBe("none");
  });

  // Cloudscape's own `Modal` focus-locks to the first focusable element in
  // the dialog on open — structurally its own close control, ahead of any
  // form field in the body — and returns focus to whatever opened it on
  // close. This is Cloudscape's real, unconfigurable behaviour (verified
  // against the actual component), not something this console retargets.
  test("moves focus into a dialog on open, to its own close control, and returns it to the trigger on Escape", async ({
    page,
  }) => {
    await page.goto("/ui/iam");
    const trigger = page.getByTestId("iam-create-user");
    await trigger.click();
    const dialog = page.getByRole("dialog", { name: "Create user" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Close" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("traps Tab focus inside an open dialog, wrapping from the last control back to the first and vice versa", async ({
    page,
  }) => {
    await page.goto("/ui/iam");
    await page.getByTestId("iam-create-user").click();
    const dialog = page.getByRole("dialog", { name: "Create user" });
    const closeButton = dialog.getByRole("button", { name: "Close" });
    // "Create user" starts disabled (the name field is empty), so the browser
    // itself would never Tab to it — the trap's own notion of "last focusable
    // control" has to agree, and here that is Cancel, not the submit button.
    const cancelButton = dialog.getByRole("button", { name: "Cancel" });
    await expect(dialog.getByTestId("iam-create-user-submit")).toBeDisabled();
    await expect(closeButton).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(cancelButton).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(closeButton).toBeFocused();
  });
});

test.describe("Theme fidelity", () => {
  // These pin colour values read directly from the real, rendered Cloudscape
  // component — captured against the actual painted pixels, not asserted
  // from a token name this console's own CSS cannot reliably reference (see
  // tokens.css): Cloudscape exposes its palette as build-hashed custom
  // properties private to the installed package version.
  test("drives light-mode container, text, and link colour from the real rendered Cloudscape components", async ({
    page,
  }) => {
    await page.goto("/ui/ecs");
    await ensureTheme(page, "light");
    const container = await page
      .locator('[class*="awsui_root"][class*="awsui_variant-default"]')
      .first()
      .evaluate((node) => getComputedStyle(node).backgroundColor);
    expect(container).toBe("rgb(255, 255, 255)"); // color-background-container-content
    const link = await page
      .getByRole("navigation", { name: "Service" })
      .getByRole("link", { name: "Elastic Container Registry", exact: true })
      .evaluate((node) => getComputedStyle(node).color);
    expect(link).toBe("rgb(0, 108, 224)");
  });

  test("drives dark-mode container and link colour from the real rendered Cloudscape components", async ({ page }) => {
    await page.goto("/ui/ecs");
    await ensureTheme(page, "dark");
    const container = await page
      .locator('[class*="awsui_root"][class*="awsui_variant-default"]')
      .first()
      .evaluate((node) => getComputedStyle(node).backgroundColor);
    expect(container).toBe("rgb(22, 29, 38)"); // color-background-container-content, dark
    const link = await page
      .getByRole("navigation", { name: "Service" })
      .getByRole("link", { name: "Elastic Container Registry", exact: true })
      .evaluate((node) => getComputedStyle(node).color);
    // Distinct from, and visibly different to, the light-mode blue above —
    // dark mode is a real theme switch, not the same colour redrawn.
    expect(link).not.toBe("rgb(0, 108, 224)");
  });

  test("keeps the not-supported badge's grey background and readable text in both themes", async ({ page }) => {
    await page.goto("/ui/not-supported/eks");
    for (const theme of ["light", "dark"] as const) {
      await ensureTheme(page, theme);
      // Scoped to the page-header badge by its accessible name: the side
      // navigation renders the same "Not supported" pill (decorative there)
      // for every unimplemented service, so a class-only locator would match
      // dozens.
      const badge = page.getByLabel("Elastic Kubernetes Service is not supported in this simulator");
      await expect(badge).toBeVisible();
      const colors = await badge.evaluate((node) => {
        const cs = getComputedStyle(node);
        return { bg: cs.backgroundColor, fg: cs.color };
      });
      // color-text-badge-grey — identical in both themes.
      expect(colors.fg).toBe("rgb(249, 249, 250)");
      expect(colors.bg).not.toBe("rgba(0, 0, 0, 0)");
    }
  });
});

/**
 * A WCAG contrast measurement that runs inside the page. It walks up from the
 * sampled element for the first ancestor with an opaque background — the
 * colour the browser actually paints behind the text, not a value asserted
 * from a token — and computes the WCAG relative-luminance contrast ratio
 * against it. This measures what actually renders rather than trusting a
 * comment, and is more precise than axe-core's own contrast heuristic — the
 * dedicated accessibility audit below runs the full, undisabled axe ruleset
 * anyway, so this suite's job is the finer-grained, painted-pixel check.
 *
 * Passed directly to `page.evaluate`, which serialises it by source and reruns
 * it in the browser context, so it must not close over anything from this
 * module — only browser globals (`document`, `getComputedStyle`).
 */
function sampleContrast(selectors: string[]): { light: { selector: string; ratio: number }[]; dark: { selector: string; ratio: number }[] } {
  const parse = (c: string) => {
    const m = (c.match(/[\d.]+/g) ?? []).map(Number);
    return [m[0] ?? 0, m[1] ?? 0, m[2] ?? 0, m[3] ?? 1] as const;
  };
  const lin = (v: number) => {
    const n = v / 255;
    return n <= 0.04045 ? n / 12.92 : Math.pow((n + 0.055) / 1.055, 2.4);
  };
  const lum = (c: readonly number[]) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
  // `over` alpha-composites a translucent layer onto whatever is behind it.
  // Cloudscape's own selection/hover tints are translucent backgrounds
  // (e.g. rgba(...) with alpha < 1) — stopping at the first
  // non-fully-transparent layer and reading its raw channel values mistakes
  // a low-strength tint for a saturated one; only compositing every
  // translucent layer down to an opaque base gives the colour a viewer
  // actually sees.
  const over = (top: readonly number[], bottom: readonly number[]) => {
    const a = top[3] + bottom[3] * (1 - top[3]);
    if (a === 0) return [255, 255, 255, 0] as const;
    const mix = (i: number) => (top[i] * top[3] + bottom[i] * bottom[3] * (1 - top[3])) / a;
    return [mix(0), mix(1), mix(2), a] as const;
  };
  const backgroundBehind = (el: Element) => {
    const layers: (readonly number[])[] = [];
    let node: Element | null = el;
    while (node && node !== document.documentElement) {
      const c = parse(getComputedStyle(node).backgroundColor);
      if (c[3] > 0) layers.push(c);
      if (c[3] >= 1) break;
      node = node.parentElement;
    }
    let result: readonly number[] = [255, 255, 255, 1];
    for (let i = layers.length - 1; i >= 0; i -= 1) result = over(layers[i], result);
    return result;
  };
  const ratio = (el: Element) => {
    const a = lum(parse(getComputedStyle(el).color));
    const b = lum(backgroundBehind(el));
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  };
  const sample = () =>
    selectors
      .map((selector) => {
        const el = document.querySelector(selector);
        return el ? { selector, ratio: ratio(el) } : null;
      })
      .filter((entry): entry is { selector: string; ratio: number } => entry !== null);

  document.documentElement.classList.remove("dark");
  const light = sample();
  document.documentElement.classList.add("dark");
  const dark = sample();
  document.documentElement.classList.remove("dark");
  return { light, dark };
}

function measureContrast(page: Page, selectors: string[]) {
  return page.evaluate(sampleContrast, selectors);
}

test.describe("Contrast", () => {
  // Measured against the surfaces the browser actually paints in both
  // themes, for the console's own chrome and the resource-page text roles a
  // real Cloudscape data page renders without a cloud read — not asserted
  // from a token or a hand-authored class name.
  test("every text role clears WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/ecs");
    const results = await measureContrast(page, [
      '[data-testid="ecs-tasks-table"] h1',
      '[data-testid="ecs-tasks-table"] p',
      "button:not(:disabled)",
      'nav[aria-label="Service"] a',
      'nav[aria-label="Service"] h3',
    ]);
    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBeGreaterThan(3);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    }
  });

  // The "Not supported" badge is the surface this pass added; it gets the
  // same measured-not-assumed check, on the page header (labelled) and the
  // side navigation's decorative one.
  test("the not-supported badge clears WCAG AA in both themes, on the page header and in the side navigation", async ({
    page,
  }) => {
    await page.goto("/ui/not-supported/eks");
    const results = await measureContrast(page, [
      '[aria-label="Elastic Kubernetes Service is not supported in this simulator"]',
      'nav[aria-label="Service"] [class*="badge-color-grey"]',
    ]);
    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBe(2);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    }
  });

  // The log-events viewer is a surface with no packaged Cloudscape
  // equivalent (see console.css) so it stays hand-built; it needs a live
  // cloud read to reach through a resource-detail page's real content, so it
  // is rendered in isolation here rather than through page.goto, and
  // measured the same way as every other surface above.
  test("the log-events viewer's timestamp and message text clear WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/ecs");
    await page.evaluate(() => {
      document.querySelector("#main-content")!.insertAdjacentHTML(
        "beforeend",
        '<div class="aws-log-events">' +
          '<div class="aws-log-event-line"><span class="aws-log-event-timestamp">2026-01-01 00:00:00 UTC</span>' +
          '<span class="aws-log-event-message">a log line</span></div>' +
          "</div>",
      );
    });
    const results = await measureContrast(page, [".aws-log-event-timestamp", ".aws-log-event-message"]);
    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBe(2);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    }
  });

  // Status is rendered by the real Cloudscape `StatusIndicator` (see
  // AwsConsole.tsx's `AwsStatus`) everywhere this console shows one — but
  // every one of them (a task's status, an account's status, an access
  // key's status) is resource data, unreachable without a live cloud read,
  // and this suite has no identity provider (see the file-level comment at
  // the bottom). Its icon/colour distinctness is proven directly against
  // the component in AwsStatus.test.tsx; its rendered contrast, wherever it
  // actually appears with real data, is covered by the same unrestricted
  // axe-core ruleset the "Automated accessibility audit" suite below runs
  // against every reachable page and overlay — the Shauth relying-party
  // suite is where a StatusIndicator with live data would actually render.
});

test.describe("Automated accessibility audit", () => {
  // The full, undisabled axe-core ruleset (including colour-contrast — the
  // dedicated Contrast suite above additionally measures the painted pixels
  // directly, but axe's own heuristic catches defect classes those
  // hand-authored checks do not aim at: missing form labels, invalid ARIA
  // usage, duplicate IDs, landmark structure, list structure). It runs
  // against every list page, the not-supported page, and the two overlays
  // (the Services mega-menu and the "Create user" dialog) that render
  // content no simple page.goto reaches, in both themes.
  const PAGES = ["/ui/", "/ui/ecs", "/ui/lambda", "/ui/ecr", "/ui/s3", "/ui/logs", "/ui/organizations", "/ui/iam", "/ui/not-supported/eks"];
  for (const theme of ["light", "dark"] as const) {
    for (const target of PAGES) {
      test(`${target} has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(target);
        if (theme === "dark") await ensureTheme(page, "dark");
        const results = await new AxeBuilder({ page }).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }

    test(`the Services mega-menu has no detectable violations (${theme})`, async ({ page }) => {
      await page.goto("/ui/");
      if (theme === "dark") await ensureTheme(page, "dark");
      await page.getByRole("button", { name: "Services" }).click();
      await expect(page.getByRole("dialog", { name: "All services" })).toBeVisible();
      const results = await new AxeBuilder({ page }).analyze();
      expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
    });

    test(`the Create user dialog has no detectable violations (${theme})`, async ({ page }) => {
      await page.goto("/ui/iam");
      if (theme === "dark") await ensureTheme(page, "dark");
      await page.getByTestId("iam-create-user").click();
      // Cloudscape's Modal has a brief open transition; sampling colours
      // mid-transition reads a transient, partially-composited blend rather
      // than the settled result.
      await page.waitForTimeout(400);
      const results = await new AxeBuilder({ page }).analyze();
      expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
    });

    const CREATE_DIALOGS = [
      { path: "/ui/s3", createTestId: "s3-create-bucket", dialogName: "Create bucket" },
      { path: "/ui/ecr", createTestId: "ecr-create-repo", dialogName: "Create repository" },
      { path: "/ui/logs", createTestId: "logs-create-log-group", dialogName: "Create log group" },
      { path: "/ui/lambda", createTestId: "lambda-create-function", dialogName: "Create function" },
      { path: "/ui/ecs", createTestId: "ecs-run-task", dialogName: "Run new task" },
      { path: "/ui/rds", createTestId: "rds-create-instance", dialogName: "Create database" },
      {
        path: "/ui/vpc",
        createTestId: "vpc-account-encryption-open",
        dialogName: "Account-level VPC encryption controls",
      },
      { path: "/ui/codebuild", createTestId: "codebuild-create-project", dialogName: "Create build project" },
      { path: "/ui/amplify", createTestId: "amplify-create-app", dialogName: "Create Amplify app" },
      { path: "/ui/glue", createTestId: "glue-create-glossary", dialogName: "Create business glossary" },
    ];
    for (const { path, createTestId, dialogName } of CREATE_DIALOGS) {
      test(`the ${dialogName} dialog has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(path);
        if (theme === "dark") await ensureTheme(page, "dark");
        await page.getByTestId(createTestId).click();
        await expect(page.getByRole("dialog", { name: dialogName })).toBeVisible();
        // Cloudscape's Modal has a brief open transition; sampling colours
        // mid-transition reads a transient, partially-composited blend rather
        // than the settled result.
        await page.waitForTimeout(400);
        const results = await new AxeBuilder({ page }).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }
  }
});

// The console used to mark 22 AWS services "Not supported" that the simulator
// in fact implements, and left another dozen it implements out of the catalog
// entirely. Each of the services below now has a real console page reading the
// service's own AWS API — the same operation its real console page reads — so
// this suite pins that every one of them renders its heading and its real
// column headers, and clears an automated accessibility audit in both themes.
// This suite has no identity provider, so a Query- or JSON-protocol read is
// rejected by the simulator exactly as real AWS would reject it and the page
// reports the failure; a REST-protocol read succeeds and renders an honest
// empty state. Either way the shell asserted here renders, and the
// authenticated data render belongs to the Shauth relying-party suite.
const SERVICE_PAGES = [
  { path: "/ui/ec2", nav: "EC2", title: "Instances", columns: ["Instance ID", "Instance state"] },
  { path: "/ui/autoscaling", nav: "EC2 Auto Scaling", title: "Auto Scaling groups", columns: ["Name", "Desired capacity"] },
  { path: "/ui/batch", nav: "AWS Batch", title: "Jobs", columns: ["Job name", "Status", "Job queue"] },
  { path: "/ui/efs", nav: "Elastic File System", title: "File systems", columns: ["File system ID", "Mount targets"] },
  { path: "/ui/rds", nav: "RDS", title: "DB instances", columns: ["Size", "Endpoint", "Storage"] },
  { path: "/ui/dynamodb", nav: "DynamoDB", title: "Tables", columns: ["Name", "Partition key"] },
  { path: "/ui/elasticache", nav: "ElastiCache", title: "Caches", columns: ["Cache name", "Node type"] },
  { path: "/ui/vpc", nav: "VPC", title: "Your VPCs", columns: ["VPC ID", "IPv4 CIDR"] },
  { path: "/ui/cloudfront", nav: "CloudFront", title: "Distributions", columns: ["Distribution ID", "Domain name"] },
  { path: "/ui/route53", nav: "Route 53", title: "Hosted zones", columns: ["Domain name", "Record count"] },
  { path: "/ui/apigateway", nav: "API Gateway", title: "REST APIs", columns: ["Description", "Endpoint type"] },
  { path: "/ui/elb", nav: "Elastic Load Balancing", title: "Load balancers", columns: ["DNS name", "Scheme"] },
  { path: "/ui/cloudmap", nav: "AWS Cloud Map", title: "Namespaces", columns: ["Namespace name", "Service count"] },
  { path: "/ui/codebuild", nav: "CodeBuild", title: "Build projects", columns: ["Name", "Source provider"] },
  { path: "/ui/amplify", nav: "AWS Amplify", title: "Apps", columns: ["App name", "Default domain", "Repository access"] },
  { path: "/ui/kinesis", nav: "Kinesis Data Streams", title: "Data streams", columns: ["Stream name", "Open shards"] },
  { path: "/ui/firehose", nav: "Amazon Data Firehose", title: "Delivery streams", columns: ["Delivery stream", "Destination"] },
  { path: "/ui/glue", nav: "AWS Glue", title: "Databases", columns: ["Description", "Location"] },
  { path: "/ui/sns", nav: "Simple Notification Service", title: "Topics", columns: ["Name", "ARN"] },
  { path: "/ui/sqs", nav: "Simple Queue Service", title: "Queues", columns: ["Name", "Messages available"] },
  { path: "/ui/eventbridge", nav: "Amazon EventBridge", title: "Rules", columns: ["Event bus", "Schedule"] },
  { path: "/ui/scheduler", nav: "Amazon EventBridge Scheduler", title: "Schedules", columns: ["Schedule name", "Target"] },
  { path: "/ui/stepfunctions", nav: "AWS Step Functions", title: "State machines", columns: ["Name", "Type"] },
  { path: "/ui/cloudwatch", nav: "CloudWatch", title: "Alarms", columns: ["Conditions", "Namespace"] },
  { path: "/ui/cloudtrail", nav: "AWS CloudTrail", title: "Trails", columns: ["Name", "Home Region"] },
  { path: "/ui/ssm", nav: "Systems Manager", title: "Parameter Store", columns: ["Tier", "Data type"] },
  {
    path: "/ui/secretsmanager",
    nav: "Secrets Manager",
    title: "Secrets",
    columns: ["Secret name", "Replication", "Rotation"],
  },
  { path: "/ui/kms", nav: "Key Management Service", title: "Customer managed keys", columns: ["Key ID", "Key usage"] },
  { path: "/ui/acm", nav: "AWS Certificate Manager", title: "Certificates", columns: ["Domain name", "Expires"] },
  { path: "/ui/private-ca", nav: "AWS Private Certificate Authority", title: "Private certificate authorities", columns: ["Common name", "Status"] },
  { path: "/ui/waf", nav: "AWS WAF", title: "Web ACLs", columns: ["Web ACL ID", "ARN"] },
  { path: "/ui/budgets", nav: "AWS Budgets", title: "Budgets", columns: ["Budget name", "Budget type"] },
];

test.describe("Service coverage", () => {
  for (const service of SERVICE_PAGES) {
    test.describe(service.nav, () => {
      test("renders its heading and real column headers", async ({ page }) => {
        await page.goto(service.path);
        await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
        for (const column of service.columns) {
          await expect(page.getByRole("columnheader", { name: column, exact: true }).first()).toBeVisible();
        }
      });

      test("is reachable from the side navigation without a not-supported pill", async ({ page }) => {
        await page.goto("/ui/");
        const nav = page.getByRole("navigation", { name: "Service" });
        const link = nav.getByRole("link", { name: service.nav, exact: true });
        await expect(link.locator("xpath=ancestor::li[1]").getByText("Not supported")).toHaveCount(0);
        await link.click();
        await expect(page).toHaveURL(new RegExp(`${service.path}$`));
        await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
        await expect(link).toHaveAttribute("aria-current", "page");
        await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toContainText(service.nav);
      });

      test("passes an automated accessibility audit in both themes", async ({ page }) => {
        for (const theme of ["light", "dark"] as const) {
          await page.goto(service.path);
          if (theme === "dark") await ensureTheme(page, "dark");
          await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
          const results = await new AxeBuilder({ page }).analyze();
          expect(results.violations, `${theme}: ${JSON.stringify(results.violations, null, 2)}`).toEqual([]);
        }
      });
    });
  }
});

// The detail routes the newly-covered services grew. Each renders its resource
// identifier as the page heading and breadcrumbs under its own service before
// any cloud read resolves, which is what this unauthenticated suite pins.
const SERVICE_DETAIL_PAGES = [
  { path: "/ui/ec2/i-0123456789abcdef0", heading: "i-0123456789abcdef0", crumbService: "EC2" },
  { path: "/ui/vpc/vpc-0123456789abcdef0", heading: "vpc-0123456789abcdef0", crumbService: "VPC" },
  { path: "/ui/dynamodb/orders", heading: "orders", crumbService: "DynamoDB" },
  { path: "/ui/efs/fs-0123456789abcdef0", heading: "fs-0123456789abcdef0", crumbService: "Elastic File System" },
  { path: "/ui/route53/Z0123456789ABCDEFGHIJ", heading: "Z0123456789ABCDEFGHIJ", crumbService: "Route 53" },
];

test.describe("Service detail pages", () => {
  for (const detail of SERVICE_DETAIL_PAGES) {
    test(`${detail.path} renders its resource heading and breadcrumb`, async ({ page }) => {
      await page.goto(detail.path);
      await expect(page.getByRole("heading", { name: detail.heading, exact: true })).toBeVisible();
      const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
      await expect(crumbs).toContainText(detail.crumbService);
      await expect(crumbs).toContainText(detail.heading);
    });

    test(`${detail.path} passes an automated accessibility audit in both themes`, async ({ page }) => {
      for (const theme of ["light", "dark"] as const) {
        await page.goto(detail.path);
        if (theme === "dark") await ensureTheme(page, "dark");
        await expect(page.getByRole("heading", { name: detail.heading, exact: true })).toBeVisible();
        const results = await new AxeBuilder({ page }).analyze();
        expect(results.violations, `${theme}: ${JSON.stringify(results.violations, null, 2)}`).toEqual([]);
      }
    });
  }
});

// The write flows the newly-covered services offer. Like every other creation
// dialog in this suite, the trigger and the dialog render before (and
// regardless of) any cloud read, so the shell — the trigger, the fields, the
// initially disabled submit, and focus management — is assertable here; the
// authenticated create→appears-in-list loop belongs to the Shauth
// relying-party suite.
const SERVICE_CREATE_DIALOGS = [
  {
    path: "/ui/efs",
    createTestId: "efs-create-file-system",
    dialogName: "Create file system",
    inputTestId: "efs-file-system-name-input",
    submitTestId: "efs-create-file-system-submit",
    validValue: "shared-workspace",
  },
  {
    path: "/ui/dynamodb",
    createTestId: "dynamodb-create-table",
    dialogName: "Create table",
    inputTestId: "dynamodb-table-name-input",
    submitTestId: "dynamodb-create-table-submit",
    validValue: "orders",
    extraFill: { testId: "dynamodb-partition-key-input", value: "orderId" },
  },
  {
    path: "/ui/sns",
    createTestId: "sns-create-topic",
    dialogName: "Create topic",
    inputTestId: "sns-topic-name-input",
    submitTestId: "sns-create-topic-submit",
    validValue: "order-events",
  },
  {
    path: "/ui/sqs",
    createTestId: "sqs-create-queue",
    dialogName: "Create queue",
    inputTestId: "sqs-queue-name-input",
    submitTestId: "sqs-create-queue-submit",
    validValue: "order-queue",
  },
  {
    path: "/ui/ssm",
    createTestId: "ssm-create-parameter",
    dialogName: "Create parameter",
    inputTestId: "ssm-parameter-name-input",
    submitTestId: "ssm-create-parameter-submit",
    validValue: "/app/prod/database-url",
    extraFill: { testId: "ssm-parameter-value-input", value: "postgres://db" },
  },
  {
    path: "/ui/secretsmanager",
    createTestId: "secrets-create-secret",
    dialogName: "Store a new secret",
    inputTestId: "secrets-secret-name-input",
    submitTestId: "secrets-create-secret-submit",
    validValue: "prod/db/password",
    extraFill: { testId: "secrets-secret-value-input", value: "s3cr3t" },
  },
  {
    path: "/ui/kms",
    createTestId: "kms-create-key",
    dialogName: "Create key",
    inputTestId: "kms-key-description-input",
    submitTestId: "kms-create-key-submit",
    validValue: "Application data key",
  },
];

test.describe("Service resource creation", () => {
  for (const dialog of SERVICE_CREATE_DIALOGS) {
    test(`${dialog.path} opens ${dialog.dialogName} with an initially disabled submit`, async ({ page }) => {
      await page.goto(dialog.path);
      const trigger = page.getByTestId(dialog.createTestId);
      await expect(trigger).toBeVisible();
      await trigger.click();
      const modal = page.getByRole("dialog", { name: dialog.dialogName });
      await expect(modal).toBeVisible();
      await expect(modal.getByTestId(dialog.submitTestId)).toBeDisabled();
      await modal.getByTestId(dialog.inputTestId).fill(dialog.validValue);
      if (dialog.extraFill) {
        await expect(modal.getByTestId(dialog.submitTestId)).toBeDisabled();
        await modal.getByTestId(dialog.extraFill.testId).fill(dialog.extraFill.value);
      }
      await expect(modal.getByTestId(dialog.submitTestId)).toBeEnabled();
      await modal.getByRole("button", { name: "Cancel" }).click();
      await expect(page.getByRole("dialog")).toHaveCount(0);
    });

    test(`${dialog.path} traps focus in ${dialog.dialogName} and returns it to the trigger on Escape`, async ({
      page,
    }) => {
      await page.goto(dialog.path);
      const trigger = page.getByTestId(dialog.createTestId);
      await trigger.click();
      const modal = page.getByRole("dialog", { name: dialog.dialogName });
      await expect(modal).toBeVisible();
      await expect(modal.getByRole("button", { name: "Close" })).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(trigger).toBeFocused();
    });

    test(`the ${dialog.dialogName} dialog has no detectable violations in both themes`, async ({ page }) => {
      for (const theme of ["light", "dark"] as const) {
        await page.goto(dialog.path);
        if (theme === "dark") await ensureTheme(page, "dark");
        await page.getByTestId(dialog.createTestId).click();
        await expect(page.getByRole("dialog", { name: dialog.dialogName })).toBeVisible();
        // Cloudscape's Modal has a brief open transition; sampling colours
        // mid-transition reads a transient, partially-composited blend rather
        // than the settled result.
        await page.waitForTimeout(400);
        const results = await new AxeBuilder({ page }).analyze();
        expect(results.violations, `${theme}: ${JSON.stringify(results.violations, null, 2)}`).toEqual([]);
      }
    });
  }
});

// The console's authenticated reads of the real AWS APIs are proven in the
// Shauth relying-party suite (ui/e2e/shauth-rps.mjs): with a live operator
// identity the console federates into credentials and reads the real APIs. This
// lightweight per-package suite has no identity provider, so the console reaches
// the simulator unauthenticated and the now-enforcing simulator rejects those
// reads exactly as real AWS would — data-render assertions therefore belong to
// the authenticated RPS path, not here. This suite covers the shell, the
// navigation, and the visual language, which do not depend on cloud reads.
