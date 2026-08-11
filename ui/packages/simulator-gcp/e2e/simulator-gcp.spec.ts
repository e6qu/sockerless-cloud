import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

const SERVICES = [
  { path: "/ui/cloudrun", nav: "Cloud Run jobs", title: "Cloud Run jobs", columns: ["Name", "Status of last execution", "Created", "Executions", "Launch stage", "Actions"] },
  { path: "/ui/functions", nav: "Cloud Run functions", title: "Cloud Run functions", columns: ["Name", "State", "Environment", "Actions"] },
  { path: "/ui/ar", nav: "Artifact Registry", title: "Artifact Registry", columns: ["Name", "Format", "Created", "Actions"] },
  { path: "/ui/gcs", nav: "Cloud Storage", title: "Cloud Storage", columns: ["Name", "Actions"] },
  { path: "/ui/serviceaccounts", nav: "Service accounts", title: "Service accounts", columns: ["Email", "Status", "Name", "Description", "Actions"] },
  { path: "/ui/projects", nav: "Manage resources", title: "Manage resources", columns: ["Name", "ID", "Number", "State", "Created", "Actions"] },
  { path: "/ui/logging", nav: "Logs Explorer", title: "Logs Explorer", columns: ["Timestamp", "Severity", "Log name"] },
];

// Every product the simulator serves a real API for, and the page the catalog
// links it to. The list is the honest map the "Not supported" chip is checked
// against: a product here must never carry the chip, and its page must render
// its own shell (title, description and table columns) rather than a stub.
const DRAWER_PRODUCTS = [
  { id: "compute-engine", href: "/ui/compute", title: "VM instances", columns: ["Name", "Status", "Zone", "Machine type", "Internal IP", "Created", "Actions"] },
  { id: "eventarc", href: "/ui/eventarc", title: "Eventarc triggers", columns: ["Name", "Event type", "Destination", "Created", "Actions"] },
  { id: "cloud-sql", href: "/ui/sql", title: "Cloud SQL instances", columns: ["Instance ID", "Status", "Database version", "Location", "IP address", "Actions"] },
  { id: "firestore", href: "/ui/firestore", title: "Firestore databases", columns: ["Database ID", "Database type", "Location", "Concurrency mode", "Created"] },
  { id: "spanner", href: "/ui/spanner", title: "Spanner instances", columns: ["Instance ID", "Instance name", "Configuration", "Nodes", "Status", "Actions"] },
  { id: "bigtable", href: "/ui/bigtable", title: "Bigtable instances", columns: ["Instance ID", "Instance name", "Instance type", "Status", "Actions"] },
  { id: "memorystore", href: "/ui/memorystore", title: "Memorystore for Redis", columns: ["Instance ID", "Status", "Tier", "Capacity", "Version", "Primary endpoint", "Actions"] },
  { id: "vpc-network", href: "/ui/vpc", title: "VPC networks", columns: ["Name", "Subnet creation mode", "Dynamic routing mode", "Created"] },
  { id: "cloud-load-balancing", href: "/ui/loadbalancing", title: "Load balancing", columns: [] },
  { id: "cloud-dns", href: "/ui/dns", title: "Cloud DNS", columns: ["Zone name", "DNS name", "Zone type", "Description", "Actions"] },
  { id: "serverless-vpc-access", href: "/ui/vpcaccess", title: "Serverless VPC Access", columns: ["Name", "Status", "Network", "IP range", "Machine type", "Instances", "Actions"] },
  { id: "bigquery", href: "/ui/bigquery", title: "BigQuery datasets", columns: ["Dataset ID", "Location", "Display name", "Created", "Actions"] },
  { id: "pub-sub", href: "/ui/pubsub", title: "Pub/Sub topics", columns: ["Topic ID", "Message retention", "Encryption key", "Actions"] },
  { id: "dataflow", href: "/ui/dataflow", title: "Dataflow jobs", columns: ["Name", "Status", "Type", "Start time", "Region", "Actions"] },
  { id: "cloud-build", href: "/ui/cloudbuild", title: "Cloud Build history", columns: ["Build", "Status", "Created", "Duration", "Steps", "Actions"] },
  { id: "api-gateway", href: "/ui/apigateway", title: "API Gateway", columns: ["Gateway ID", "Display name", "Status", "API config", "Gateway URL"] },
  { id: "enabled-apis", href: "/ui/apis", title: "Enabled APIs & services", columns: ["Service", "Service name", "Status", "Actions"] },
  { id: "cloud-kms", href: "/ui/kms", title: "Cloud KMS key rings", columns: ["Key ring", "Location", "Created"] },
  { id: "secret-manager", href: "/ui/secrets", title: "Secret Manager", columns: ["Name", "Replication", "Created", "Actions"] },
  { id: "iam", href: "/ui/iam", title: "IAM", columns: [] },
  // Already covered by the product navigation above; listed so the chip check
  // sweeps the whole catalog rather than only the pages this pass added.
  { id: "cloud-run", href: "/ui/cloudrun", title: "Cloud Run jobs", columns: [] },
  { id: "cloud-run-functions", href: "/ui/functions", title: "Cloud Run functions", columns: [] },
  { id: "cloud-storage", href: "/ui/gcs", title: "Cloud Storage", columns: [] },
  { id: "artifact-registry", href: "/ui/ar", title: "Artifact Registry", columns: [] },
  { id: "service-accounts", href: "/ui/serviceaccounts", title: "Service accounts", columns: [] },
  { id: "manage-resources", href: "/ui/projects", title: "Manage resources", columns: [] },
  { id: "logging", href: "/ui/logging", title: "Logs Explorer", columns: [] },
];

// The detail routes this pass added. Like the existing detail pages, this
// suite has no identity provider, so each read reaches the enforcing simulator
// unauthenticated; the assertion is that the page shell renders honestly
// (breadcrumb plus the API's own error) rather than crashing or fabricating.
const DRAWER_DETAIL_PAGES = [
  "/ui/compute/us-central1-a/example-vm",
  "/ui/vpc/example-network",
  "/ui/dns/example-zone",
  "/ui/sql/example-instance",
  "/ui/spanner/example-instance",
  "/ui/bigtable/example-instance",
  "/ui/memorystore/example-instance",
  "/ui/bigquery/example_dataset",
  "/ui/pubsub/example-topic",
  "/ui/dataflow/example-job",
  "/ui/cloudbuild/example-build",
  "/ui/eventarc/example-trigger",
  "/ui/kms/example-ring",
  "/ui/kms/example-ring/example-key",
  "/ui/secrets/example-secret",
];

test.describe("Google Cloud console shell", () => {
  test("presents the header with project chip, central search and pill navigation", async ({ page }) => {
    await page.goto("/ui/");
    expect((await page.request.get("/ui/favicon.svg")).status()).toBe(200);
    await expect(page.locator(".gc-header")).toBeVisible();
    await expect(page.getByText("Google Cloud", { exact: true })).toBeVisible();
    await expect(page.locator(".gc-project-chip")).toBeVisible();
    await expect(page.getByLabel("Search resources, docs, products, and more")).toBeVisible();
    const nav = page.getByRole("navigation", { name: "Product" });
    await expect(nav.getByRole("link", { name: "Overview" })).toBeVisible();
  });

  // A fixed-height header leaves anything taller drawn outside its box, where
  // the row below paints over it. A control covered by an opaque sibling still
  // reports as visible, so the failure shows up only as clicks landing on the
  // wrong element — as it did on the AWS console. Assert containment.
  test("contains every header control within the header itself", async ({ page }) => {
    await page.goto("/ui/");
    const header = page.locator(".gc-header");
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
    const toggle = page.getByRole("button", { name: /Switch to (dark|light) theme/ });
    const headerBox = await page.locator(".gc-header").boundingBox();
    const toggleBox = await toggle.boundingBox();
    expect(toggleBox!.x).toBeGreaterThan(headerBox!.x + headerBox!.width / 2);

    const isDark = () => page.evaluate(() => document.documentElement.classList.contains("dark"));
    const before = await isDark();
    await toggle.click();
    expect(await isDark()).toBe(!before);
    await toggle.click();
    expect(await isDark()).toBe(before);
  });

  test("focuses the search field on \"/\", the shortcut the placeholder names", async ({ page }) => {
    await page.goto("/ui/");
    const search = page.getByLabel("Search resources, docs, products, and more");
    await expect(search).not.toBeFocused();
    await page.keyboard.press("/");
    await expect(search).toBeFocused();
  });

  test("does not steal a literal \"/\" typed into another field", async ({ page }) => {
    await page.goto("/ui/logging");
    const query = page.getByTestId("log-query-input");
    await query.fill("logName:\"run.googleapis.com/stdout\"");
    await expect(query).toHaveValue('logName:"run.googleapis.com/stdout"');
  });

  test("redirects / to /ui/", async ({ page }) => {
    const response = await page.goto("/");
    expect(page.url()).toContain("/ui/");
    expect(response?.status()).toBe(200);
  });

  test("exposes a skip link ahead of the console content", async ({ page }) => {
    await page.goto("/ui/");
    await page.locator("body").focus();
    await page.keyboard.press("Tab");
    await expect(page.locator(".sl-skip-link")).toBeFocused();
    await expect(page.locator("#main-content")).toHaveCount(1);
  });
});

test.describe("Project picker", () => {
  // The lightweight suite has no identity provider, so the dialog's project
  // list reaches the enforcing simulator unauthenticated and reports the
  // API's own error; the selected-project chip, the dialog, and the New
  // project form are the console's own and must render regardless. The
  // authenticated list/create/switch flows are proven in the relying-party
  // suite (ui/e2e/shauth-rps.mjs).
  test("names the selected project on the chip and opens the project dialog", async ({ page }) => {
    await page.goto("/ui/");
    const chip = page.getByTestId("project-picker");
    await expect(chip).toBeVisible();
    await expect(chip).toContainText("sockerless");
    await chip.click();
    await expect(page.getByTestId("project-dialog")).toBeVisible();
    await expect(page.getByLabel("Search projects and folders")).toBeVisible();
    await expect(page.getByTestId("project-manage-link")).toBeVisible();
  });

  test("offers the New project form with a derived, validated project ID", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByTestId("project-picker").click();
    await page.getByTestId("project-create-open").click();
    const name = page.getByTestId("project-create-name");
    await expect(name).toBeVisible();
    await expect(page.getByTestId("project-create-submit")).toBeDisabled();
    await name.fill("Console Test Project");
    await expect(page.getByTestId("project-create-id")).toHaveValue("console-test-project");
    await expect(page.getByTestId("project-create-submit")).toBeEnabled();
  });

  test("navigates to Manage resources from the dialog", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByTestId("project-picker").click();
    await page.getByTestId("project-manage-link").click();
    await expect(page).toHaveURL(/\/ui\/projects$/);
    await expect(page.getByRole("heading", { name: "Manage resources" })).toBeVisible();
  });
});

test.describe("Overview", () => {
  test("presents the overview", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
    // The per-resource counts require an authenticated read; this lightweight
    // suite has no identity provider, so the console reaches the enforcing
    // simulator unauthenticated. The counts and the links they carry are proven
    // in the authenticated relying-party suite (ui/e2e/shauth-rps.mjs).
  });
});

for (const service of SERVICES) {
  test.describe(service.nav, () => {
    test("renders its title, description and table columns", async ({ page }) => {
      await page.goto(service.path);
      await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
      await expect(page.locator(".gc-page-description")).toBeVisible();
      for (const column of service.columns) {
        await expect(page.getByRole("columnheader", { name: column, exact: true })).toBeVisible();
      }
    });
  });
}

test.describe("Navigation", () => {
  test("reaches every product from the navigation and marks the active one", async ({ page }) => {
    await page.goto("/ui/");
    for (const service of SERVICES) {
      await page.getByRole("link", { name: service.nav, exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`${service.path}$`));
      await expect(page.getByRole("heading", { name: service.title })).toBeVisible();
      const link = page.getByRole("link", { name: service.nav, exact: true });
      await expect(link).toHaveClass(/gc-nav-link-active/);
      // react-router's NavLink sets aria-current="page" on the active link by
      // default; a screen reader announces which product the pill highlights
      // visually, not just its colour.
      await expect(link).toHaveAttribute("aria-current", "page");
    }
    await page.getByRole("link", { name: "Overview", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  });
});

test.describe("Product catalog navigation drawer", () => {
  // The real console's hamburger opens the full Google Cloud product catalog,
  // grouped the way the console groups it. The simulator implements a slice of
  // Google Cloud, so the catalog stays truthful: products it implements link to
  // their page, the rest carry a "Not supported" chip and route to a short
  // honest explanation rather than being silently omitted or faked.
  test("opens from the hamburger with the grouped product catalog", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("button", { name: "Main menu" });
    await expect(menu).toHaveAttribute("aria-expanded", "false");
    await menu.click();
    const drawer = page.getByRole("dialog", { name: "Google Cloud products" });
    await expect(drawer).toBeVisible();
    await expect(menu).toHaveAttribute("aria-expanded", "true");
    await expect(drawer.getByRole("heading", { name: "Compute" })).toBeVisible();
    await expect(drawer.getByRole("heading", { name: "IAM & Admin" })).toBeVisible();
    await expect(drawer.getByTestId("drawer-item-cloud-run")).toBeVisible();
    await expect(drawer.getByTestId("drawer-item-compute-engine")).toBeVisible();
    await expect(drawer.getByTestId("drawer-item-kubernetes-engine")).toBeVisible();
  });

  test("links a supported product to its real page and closes the drawer", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    await page.getByTestId("drawer-item-cloud-run").click();
    await expect(page).toHaveURL(/\/ui\/cloudrun$/);
    await expect(page.getByRole("heading", { name: "Cloud Run jobs" })).toBeVisible();
    await expect(page.getByTestId("nav-drawer")).toHaveCount(0);
  });

  test("marks an unsupported product with a Not supported chip conveyed in text, not colour alone", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    const item = page.getByTestId("drawer-item-kubernetes-engine");
    await expect(item.locator(".gc-chip-unsupported")).toHaveText("Not supported");
    // The link carries an explicit aria-label so the accessible name reads as a
    // single clean phrase ("<product>, not supported in this simulator"),
    // matching the AWS and Azure consoles; the visible "Not supported" chip
    // conveys the same meaning in text, not colour alone.
    await expect(item).toHaveAccessibleName("Kubernetes Engine, not supported in this simulator");
  });

  // The chip is a claim about the simulator, so it must appear only where the
  // simulator serves no API for the product. Every product whose real REST
  // surface the simulator answers links to its own page instead — asserted
  // here so a page can never regress back into a "Not supported" link.
  test("never marks a product the simulator implements as unsupported", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    const drawer = page.getByRole("dialog", { name: "Google Cloud products" });
    for (const { id, href } of DRAWER_PRODUCTS) {
      const item = drawer.getByTestId(`drawer-item-${id}`);
      await expect(item).toHaveAttribute("href", href);
      await expect(item.locator(".gc-chip-unsupported")).toHaveCount(0);
    }
  });

  test("routes an unsupported product to a short, honest explanation instead of a dead link", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    await page.getByTestId("drawer-item-kubernetes-engine").click();
    await expect(page).toHaveURL(/\/ui\/not-supported\/kubernetes-engine$/);
    await expect(page.getByRole("heading", { name: "Kubernetes Engine", exact: true })).toBeVisible();
    await expect(page.getByText(/isn't implemented by the Sockerless simulator/)).toBeVisible();
    await expect(page.getByRole("link", { name: "Back to Overview" })).toBeVisible();

    // A direct visit (not only a drawer click) resolves the same way, and an
    // unrecognised slug still renders honestly rather than crashing.
    await page.goto("/ui/not-supported/vertex-ai");
    await expect(page.getByRole("heading", { name: "Vertex AI", exact: true })).toBeVisible();
  });

  test("closes on Escape and returns focus to the hamburger", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("button", { name: "Main menu" });
    await menu.click();
    await expect(page.getByTestId("nav-drawer")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("nav-drawer")).toHaveCount(0);
    await expect(menu).toBeFocused();
  });

  test("closes on a scrim click", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    const viewport = page.viewportSize()!;
    await page.locator(".gc-drawer-overlay").click({ position: { x: viewport.width - 20, y: 20 } });
    await expect(page.getByTestId("nav-drawer")).toHaveCount(0);
  });
});

test.describe("Dialog keyboard operability", () => {
  // GcpDialog backs the project picker and every create/delete confirmation;
  // proving it once here covers all of them rather than each caller
  // reimplementing Escape-to-close and focus return.
  test("the project picker dialog closes on Escape and returns focus to its trigger", async ({ page }) => {
    await page.goto("/ui/");
    const trigger = page.getByTestId("project-picker");
    await trigger.click();
    await expect(page.getByTestId("project-dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("project-dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });
});

test.describe("Accessibility landmarks", () => {
  test("exposes banner, main and product-navigation landmarks", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("banner")).toBeVisible();
    await expect(page.getByRole("main")).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Product" })).toBeVisible();
  });

  test("the document declares its language", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
  });
});

// Shared by every contrast test below: measured against the surfaces the
// browser actually paints, walking up for the first opaque background,
// rather than asserted from the palette. Disabled controls are excluded —
// WCAG 1.4.3 exempts inactive components, and the console greys them
// deliberately. Runs in the browser via page.evaluate, so it must be a
// standalone function with no closure over the test's outer scope.
function sampleContrast(selectors: string[]) {
  const parse = (c: string) => {
    const m = (c.match(/[\d.]+/g) ?? []).map(Number);
    return [m[0] ?? 0, m[1] ?? 0, m[2] ?? 0, m[3] ?? 1] as const;
  };
  const lin = (v: number) => {
    const n = v / 255;
    return n <= 0.04045 ? n / 12.92 : Math.pow((n + 0.055) / 1.055, 2.4);
  };
  const lum = (c: readonly number[]) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
  const opaqueBehind = (el: Element) => {
    let node: Element | null = el;
    while (node && node !== document.documentElement) {
      const c = parse(getComputedStyle(node).backgroundColor);
      if (c[3] > 0) return c;
      node = node.parentElement;
    }
    return [255, 255, 255, 1] as const;
  };
  const ratio = (el: Element) => {
    const a = lum(parse(getComputedStyle(el).color));
    const b = lum(opaqueBehind(el));
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  };
  const sample = () =>
    selectors
      .flatMap((selector) => Array.from(document.querySelectorAll(selector)))
      // A disabled control is exempt from the contrast requirement.
      .filter((el) => !(el as HTMLButtonElement).disabled && !el.closest(":disabled"))
      .map((el) => ({ tag: el.className, ratio: ratio(el) }));

  document.documentElement.classList.remove("dark");
  const light = sample();
  document.documentElement.classList.add("dark");
  const dark = sample();
  document.documentElement.classList.remove("dark");
  return { light, dark };
}

function assertAA(results: { light: { tag: string; ratio: number }[]; dark: { tag: string; ratio: number }[] }) {
  for (const [theme, samples] of Object.entries(results)) {
    expect(samples.length).toBeGreaterThan(0);
    for (const sample of samples) {
      expect(sample.ratio, `${theme}: ${sample.tag} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
    }
  }
}

test.describe("Contrast", () => {
  // The tightest role here is around 4.68:1, so the guard has little room to
  // spare and is worth holding.
  test("every enabled text role clears WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/cloudrun");
    const results = await page.evaluate(sampleContrast, [
      ".gc-wordmark",
      ".gc-project-chip",
      ".gc-nav-link",
      ".gc-nav-link-active",
      ".gc-product-title",
      ".gc-page-header h1",
      ".gc-page-description",
      ".gc-refresh",
      ".gc-filter-chip",
      ".gc-table th button",
      ".gc-empty-headline",
      ".gc-empty-description",
    ]);
    expect(results.light.length).toBeGreaterThan(10);
    assertAA(results);
  });

  // The navigation drawer and its "Not supported" chip are new surfaces this
  // pass added; they get the same empirical guard as the rest of the shell.
  test("the navigation drawer and its Not supported chip clear WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    await expect(page.getByTestId("nav-drawer")).toBeVisible();
    const results = await page.evaluate(sampleContrast, [
      ".gc-drawer-title",
      ".gc-drawer-note",
      ".gc-drawer-group-label",
      ".gc-drawer-link",
      ".gc-drawer-link-active",
      ".gc-drawer-link-unsupported",
      ".gc-chip-unsupported",
    ]);
    expect(results.light.length).toBeGreaterThan(10);
    assertAA(results);
  });

  test("the not-supported page clears WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/not-supported/kubernetes-engine");
    const results = await page.evaluate(sampleContrast, [
      ".gc-page-header h1",
      ".gc-page-description",
      ".gc-empty-headline",
      ".gc-empty-description",
      ".gc-button-primary",
    ]);
    assertAA(results);
  });

  // The Logs Explorer query bar is this pass's new surface: unlike the
  // authenticated tabs and sub-resource tables, it renders unconditionally,
  // so it gets the same empirical guard here.
  test("the Logs Explorer query bar clears WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/logging");
    const results = await page.evaluate(sampleContrast, [".gc-log-query-field", '[data-testid="log-query-run"]']);
    assertAA(results);
  });

  // The Create bucket / Create repository dialogs are this pass's new
  // surface: their labels, hints and the Artifact Registry format select
  // get the same empirical guard as the rest of the shell.
  test("the resource-creation dialogs clear WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/gcs");
    await page.getByTestId("gcs-create-bucket").click();
    await expect(page.getByTestId("gcs-create-dialog")).toBeVisible();
    const gcsResults = await page.evaluate(sampleContrast, [
      ".gc-dialog-title",
      ".gc-field",
      ".gc-field-hint",
      ".gc-button-text",
      ".gc-button-primary",
    ]);
    assertAA(gcsResults);

    await page.goto("/ui/ar");
    await page.getByTestId("ar-create-repo").click();
    await expect(page.getByTestId("ar-create-dialog")).toBeVisible();
    const arResults = await page.evaluate(sampleContrast, [
      ".gc-dialog-title",
      ".gc-field",
      ".gc-field-hint",
      ".gc-button-text",
      ".gc-button-primary",
    ]);
    assertAA(arResults);
  });

  // The compute create dialogs (Cloud Run job / Cloud Function) and the job's
  // Execute confirm this pass added get the same empirical guard, including the
  // reusable key/value labels editor embedded in the Create job dialog.
  test("the compute create and run dialogs clear WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/cloudrun");
    await page.getByTestId("cloudrun-create-job").click();
    await expect(page.getByTestId("cloudrun-create-dialog")).toBeVisible();
    await page.getByTestId("cloudrun-create-env-label-add").click();
    const jobResults = await page.evaluate(sampleContrast, [
      ".gc-dialog-title",
      ".gc-field",
      ".gc-field-hint",
      ".gc-labels-editor .gc-field",
      ".gc-button-text",
      ".gc-button-primary",
    ]);
    assertAA(jobResults);

    await page.goto("/ui/functions");
    await page.getByTestId("function-create").click();
    await expect(page.getByTestId("function-create-dialog")).toBeVisible();
    const fnResults = await page.evaluate(sampleContrast, [
      ".gc-dialog-title",
      ".gc-field",
      ".gc-field-hint",
      ".gc-button-text",
      ".gc-button-primary",
    ]);
    assertAA(fnResults);

    await page.goto("/ui/cloudrun/example-job");
    await page.getByTestId("cloudrun-job-run").click();
    await expect(page.getByTestId("cloudrun-run-dialog")).toBeVisible();
    const runResults = await page.evaluate(sampleContrast, [".gc-dialog-title", ".gc-dialog p", ".gc-button-text", ".gc-button-primary"]);
    assertAA(runResults);
  });

  // The drawer's product search is this pass's new shell surface.
  test("the product catalog search field clears WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    await expect(page.getByTestId("nav-drawer")).toBeVisible();
    const results = await page.evaluate(sampleContrast, [".gc-drawer-search input", ".gc-drawer-title"]);
    assertAA(results);
  });

  // The delete-confirm dialogs this pass added to every resource detail page
  // get the same empirical guard: their title, warning copy and Cancel/Delete
  // controls.
  test("the resource-deletion dialogs clear WCAG AA in both themes", async ({ page }) => {
    const cases = [
      { path: "/ui/gcs/example-bucket", openTestId: "gcs-bucket-delete", dialogTestId: "gcs-delete-dialog" },
      { path: "/ui/ar/example-repo", openTestId: "ar-repo-delete", dialogTestId: "ar-delete-dialog" },
      { path: "/ui/functions/example-function", openTestId: "function-delete", dialogTestId: "function-delete-dialog" },
      { path: "/ui/cloudrun/example-job", openTestId: "cloudrun-job-delete", dialogTestId: "cloudrun-delete-dialog" },
    ];
    for (const { path, openTestId, dialogTestId } of cases) {
      await page.goto(path);
      await page.getByTestId(openTestId).click();
      await expect(page.getByTestId(dialogTestId)).toBeVisible();
      const results = await page.evaluate(sampleContrast, [".gc-dialog-title", ".gc-dialog p", ".gc-button-text", ".gc-button-primary"]);
      assertAA(results);
    }
  });
});

// Cloud Storage buckets and Artifact Registry repositories used to render a
// disabled "Create …" action even though the real console lets an operator
// create either from the same page. Each now offers a real "Create …" header
// action and empty-state primary that open a GcpDialog with client-side name
// validation, wired to the real storage.buckets.insert / Artifact Registry
// create operation. The header action and the dialog it opens render before
// (and regardless of) any cloud read, so they are assertable here without an
// identity provider; this suite's unauthenticated read is rejected by the
// enforcing simulator (see the Visual fidelity describe above), so the
// empty-state primary — which only renders on a successful empty read — and
// the authenticated create→appears round trip are exercised by the
// mocked-fetch suite (src/__tests__/ResourceCreateFlows.test.tsx), the same
// split the AWS console's ECR/S3/CloudWatch Logs create dialogs document.
test.describe("Resource creation", () => {
  const cases = [
    {
      path: "/ui/gcs",
      createTestId: "gcs-create-bucket",
      dialogTestId: "gcs-create-dialog",
      dialogName: "Create a bucket",
      inputTestId: "gcs-create-name",
      submitTestId: "gcs-create-submit",
      validName: "my-new-bucket",
    },
    {
      path: "/ui/ar",
      createTestId: "ar-create-repo",
      dialogTestId: "ar-create-dialog",
      dialogName: "Create repository",
      inputTestId: "ar-create-id",
      submitTestId: "ar-create-submit",
      validName: "my-new-repo",
    },
  ];

  for (const { path, createTestId, dialogTestId, dialogName, inputTestId, submitTestId, validName } of cases) {
    test(`${path} offers ${dialogName} and opens the create dialog with an initially-disabled submit`, async ({ page }) => {
      await page.goto(path);
      const create = page.getByTestId(createTestId);
      await expect(create).toBeVisible();
      await create.click();
      const dialog = page.getByTestId(dialogTestId);
      await expect(dialog).toBeVisible();
      await expect(dialog).toHaveAccessibleName(dialogName);
      const input = dialog.getByTestId(inputTestId);
      await expect(input).toBeVisible();
      // An empty or invalid name must not be submittable.
      await expect(dialog.getByTestId(submitTestId)).toBeDisabled();
      await input.fill(validName);
      await expect(dialog.getByTestId(submitTestId)).toBeEnabled();
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(page.getByTestId(dialogTestId)).toHaveCount(0);
    });

    test(`${path} closes ${dialogName} on Escape and returns focus to its trigger`, async ({ page }) => {
      await page.goto(path);
      const trigger = page.getByTestId(createTestId);
      await trigger.click();
      const dialog = page.getByTestId(dialogTestId);
      await expect(dialog).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(page.getByTestId(dialogTestId)).toHaveCount(0);
      await expect(trigger).toBeFocused();
    });
  }

  test("the Create repository dialog offers a format picker defaulting to DOCKER", async ({ page }) => {
    await page.goto("/ui/ar");
    await page.getByTestId("ar-create-repo").click();
    const dialog = page.getByTestId("ar-create-dialog");
    const format = dialog.getByTestId("ar-create-format");
    await expect(format).toHaveValue("DOCKER");
    await expect(format.locator("option")).toHaveCount(8);
  });
});

// Cloud Storage buckets, Artifact Registry repositories, Cloud Run functions
// and Cloud Run jobs used to have no delete affordance in this console, even
// though the real console (and the AWS console, which deletes every
// resource) lets an operator delete each of them. Every detail page now
// offers a real "Delete" header action that opens a GcpDialog confirm — wired
// to the real storage.buckets.delete / projects.locations.repositories.delete
// / projects.locations.functions.delete / projects.locations.jobs.delete
// operation — and, unlike the resource itself, doesn't wait on a successful
// read to render: it addresses the resource by the id in the route, the same
// way the list pages' "Create …" header action doesn't wait on a successful
// list read. That is what makes the dialog reachable in this unauthenticated
// suite. The list pages' equivalent per-row "Delete" action needs a loaded
// row and so is exercised in the mocked-fetch suite
// (src/__tests__/ResourceDeleteFlows.test.tsx) instead; the authenticated
// delete round trip against a live identity provider (list and detail) is
// covered by the Shauth relying-party suite (ui/e2e/shauth-rps.mjs).
test.describe("Resource deletion", () => {
  const DELETE_DIALOGS = [
    { path: "/ui/gcs/example-bucket", openTestId: "gcs-bucket-delete", dialogTestId: "gcs-delete-dialog", dialogName: "Delete bucket?" },
    { path: "/ui/ar/example-repo", openTestId: "ar-repo-delete", dialogTestId: "ar-delete-dialog", dialogName: "Delete repository?" },
    { path: "/ui/functions/example-function", openTestId: "function-delete", dialogTestId: "function-delete-dialog", dialogName: "Delete function?" },
    { path: "/ui/cloudrun/example-job", openTestId: "cloudrun-job-delete", dialogTestId: "cloudrun-delete-dialog", dialogName: "Delete job?" },
  ];

  for (const { path, openTestId, dialogTestId, dialogName } of DELETE_DIALOGS) {
    test(`${path} offers a Delete action and opens "${dialogName}" with Cancel and Delete controls`, async ({ page }) => {
      await page.goto(path);
      const trigger = page.getByTestId(openTestId);
      await expect(trigger).toBeVisible();
      await trigger.click();
      const dialog = page.getByTestId(dialogTestId);
      await expect(dialog).toBeVisible();
      await expect(dialog).toHaveAccessibleName(dialogName);
      await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
      await expect(dialog.getByRole("button", { name: "Delete" })).toBeVisible();
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(page.getByTestId(dialogTestId)).toHaveCount(0);
    });

    test(`${path} closes "${dialogName}" on Escape and returns focus to its trigger`, async ({ page }) => {
      await page.goto(path);
      const trigger = page.getByTestId(openTestId);
      await trigger.click();
      const dialog = page.getByTestId(dialogTestId);
      await expect(dialog).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(page.getByTestId(dialogTestId)).toHaveCount(0);
      await expect(trigger).toBeFocused();
    });
  }
});

// Cloud Run jobs and Cloud Run functions were deferred as read-only-plus-delete;
// the real console creates both, and Cloud Run jobs are executed on demand. The
// list pages now offer a real "Create job" / "Create function" header action
// (rendered before, and regardless of, any list read) and the jobs list a
// per-row / detail "Run" action (which addresses the job by the id in the
// route, so it is reachable without a successful read). Those are assertable in
// this unauthenticated suite; the Edit header actions on the detail pages need
// the loaded resource to prefill their form, so — like the empty state — they
// are exercised over an authenticated read in the relying-party suite
// (ui/e2e/shauth-rps.mjs) and against mocked auth in
// src/__tests__/ResourceEditFlows.test.tsx.
test.describe("Compute resource creation", () => {
  test("Cloud Run jobs offers Create job with a form that validates before submitting", async ({ page }) => {
    await page.goto("/ui/cloudrun");
    const create = page.getByTestId("cloudrun-create-job");
    await expect(create).toBeVisible();
    await create.click();
    const dialog = page.getByTestId("cloudrun-create-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog).toHaveAccessibleName("Create job");
    await expect(dialog.getByTestId("cloudrun-create-region")).toHaveValue("us-central1");
    await expect(dialog.getByTestId("cloudrun-create-submit")).toBeDisabled();
    await dialog.getByTestId("cloudrun-create-id").fill("my-job");
    await expect(dialog.getByTestId("cloudrun-create-submit")).toBeDisabled();
    await dialog.getByTestId("cloudrun-create-image").fill("gcr.io/p/img");
    await expect(dialog.getByTestId("cloudrun-create-submit")).toBeEnabled();
    // The environment-variable editor (the reusable labels editor) adds a row.
    await dialog.getByTestId("cloudrun-create-env-label-add").click();
    await expect(dialog.getByTestId("cloudrun-create-env-label-key-0")).toBeVisible();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("cloudrun-create-dialog")).toHaveCount(0);
  });

  test("Cloud Run job Create dialog closes on Escape and returns focus to its trigger", async ({ page }) => {
    await page.goto("/ui/cloudrun");
    const trigger = page.getByTestId("cloudrun-create-job");
    await trigger.click();
    await expect(page.getByTestId("cloudrun-create-dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("cloudrun-create-dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });

  test("Cloud Run functions offers Create function with a form that validates before submitting", async ({ page }) => {
    await page.goto("/ui/functions");
    const create = page.getByTestId("function-create");
    await expect(create).toBeVisible();
    await create.click();
    const dialog = page.getByTestId("function-create-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog).toHaveAccessibleName("Create function");
    await expect(dialog.getByTestId("function-create-region")).toHaveValue("us-central1");
    await expect(dialog.getByTestId("function-create-runtime")).toHaveValue("nodejs20");
    await expect(dialog.getByTestId("function-create-submit")).toBeDisabled();
    await dialog.getByTestId("function-create-id").fill("my-fn");
    await expect(dialog.getByTestId("function-create-submit")).toBeDisabled();
    await dialog.getByTestId("function-create-entrypoint").fill("handler");
    await expect(dialog.getByTestId("function-create-submit")).toBeEnabled();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("function-create-dialog")).toHaveCount(0);
  });

  test("Cloud Function Create dialog closes on Escape and returns focus to its trigger", async ({ page }) => {
    await page.goto("/ui/functions");
    const trigger = page.getByTestId("function-create");
    await trigger.click();
    await expect(page.getByTestId("function-create-dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("function-create-dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });
});

test.describe("Cloud Run job execution", () => {
  // The Run action addresses the job by the id in the route, so — like the
  // Delete action — it is reachable on a failed (unauthenticated) detail read.
  test("offers a Run action that opens an Execute confirm with Execute and Cancel controls", async ({ page }) => {
    await page.goto("/ui/cloudrun/example-job");
    const trigger = page.getByTestId("cloudrun-job-run");
    await expect(trigger).toBeVisible();
    await trigger.click();
    const dialog = page.getByTestId("cloudrun-run-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog).toHaveAccessibleName("Execute job?");
    await expect(dialog.getByRole("button", { name: "Execute" })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("cloudrun-run-dialog")).toHaveCount(0);
    await expect(trigger).toBeFocused();
  });
});

test.describe("Product catalog search", () => {
  // The drawer's product search filters the catalog live, matching the AWS
  // console's services flyout, while keeping unsupported products honest.
  test("filters the product catalog live and keeps unsupported products honest", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByRole("button", { name: "Main menu" }).click();
    const drawer = page.getByRole("dialog", { name: "Google Cloud products" });
    await expect(drawer).toBeVisible();
    await drawer.getByTestId("nav-drawer-search").fill("storage");
    await expect(drawer.getByTestId("drawer-item-cloud-storage")).toBeVisible();
    await expect(drawer.getByTestId("drawer-item-cloud-run")).toHaveCount(0);
    await expect(drawer.getByTestId("drawer-item-compute-engine")).toHaveCount(0);

    await drawer.getByTestId("nav-drawer-search").fill("kubernetes");
    // An unsupported product still surfaces (and keeps its chip).
    const item = drawer.getByTestId("drawer-item-kubernetes-engine");
    await expect(item).toBeVisible();
    await expect(item.locator(".gc-chip-unsupported")).toHaveText("Not supported");

    await drawer.getByTestId("nav-drawer-search").fill("zzz-nothing");
    await expect(drawer.getByTestId("nav-drawer-empty")).toBeVisible();
  });
});

test.describe("Resource detail pages", () => {
  // This lightweight suite has no identity provider (see the Overview and
  // Project picker describes above), so every detail read reaches the
  // enforcing simulator unauthenticated and reports the API's own error
  // rather than rendering the resource. That still proves the page shell
  // itself — breadcrumb, page header and description — renders honestly on a
  // failed read instead of crashing or fabricating data; the authenticated
  // property grids, tabs and sub-resource tables (Executions, Images,
  // Objects) are proven in the relying-party suite (ui/e2e/shauth-rps.mjs).
  const DETAILS = [
    { path: "/ui/cloudrun/example-job", back: "Cloud Run jobs", backHref: "/ui/cloudrun" },
    { path: "/ui/functions/example-function", back: "Cloud Run functions", backHref: "/ui/functions" },
    { path: "/ui/ar/example-repo", back: "Artifact Registry", backHref: "/ui/ar" },
    { path: "/ui/gcs/example-bucket", back: "Cloud Storage", backHref: "/ui/gcs" },
    { path: "/ui/serviceaccounts/example@sockerless.iam.gserviceaccount.com", back: "Service accounts", backHref: "/ui/serviceaccounts" },
  ];

  for (const detail of DETAILS) {
    test(`renders the breadcrumb and page shell for ${detail.path}`, async ({ page }) => {
      await page.goto(detail.path);
      const back = page.getByRole("link", { name: `‹ ${detail.back}` });
      await expect(back).toBeVisible();
      await expect(back).toHaveAttribute("href", detail.backHref);
      await expect(page.locator(".gc-message-error, .gc-loading")).toBeVisible();
    });
  }
});

test.describe("Logs Explorer query bar", () => {
  // The query bar itself — unlike the results table — needs no authenticated
  // read to render, so its structure and interaction are provable in this
  // lightweight suite; the real filtered results are proven in the
  // relying-party suite.
  test("offers a Cloud Logging query field and a minimum-severity picker", async ({ page }) => {
    await page.goto("/ui/logging");
    const query = page.getByTestId("log-query-input");
    await expect(query).toBeVisible();
    const severity = page.getByTestId("log-severity-select");
    await expect(severity).toBeVisible();
    // "Any" plus the 9 Cloud Logging severity levels (DEFAULT..EMERGENCY).
    await expect(severity.locator("option")).toHaveCount(10);
    await expect(page.getByTestId("log-query-run")).toBeVisible();
  });

  test("composes the typed query and severity into the request without reloading the page", async ({ page }) => {
    await page.goto("/ui/logging");
    await page.getByTestId("log-query-input").fill('resource.type="cloud_run_job"');
    await page.getByTestId("log-severity-select").selectOption("ERROR");
    await page.getByTestId("log-query-run").click();
    // The query composes client-side; the page never navigates away, and the
    // Logs Explorer heading and query bar are still present, unauthenticated
    // read of the composed query notwithstanding.
    await expect(page.getByRole("heading", { name: "Logs Explorer" })).toBeVisible();
    await expect(page.getByTestId("log-query-input")).toHaveValue('resource.type="cloud_run_job"');
  });
});

test.describe("Visual fidelity", () => {
  // Structural proxies for the visual rebuild, so the icons, typeface, and
  // account avatar the console gained can't silently regress to the earlier
  // glyph-and-text sketch.
  test("renders a real icon on every navigation item", async ({ page }) => {
    await page.goto("/ui/");
    const links = page.getByRole("navigation", { name: "Product" }).getByRole("link");
    const count = await links.count();
    expect(count).toBeGreaterThanOrEqual(6);
    for (let index = 0; index < count; index += 1) {
      await expect(links.nth(index).locator("svg")).toHaveCount(1);
    }
  });

  test("carries the header tool cluster and an account avatar, not an error string", async ({ page }) => {
    await page.goto("/ui/");
    // Several tool icons in the header rather than bare text.
    const headerIcons = page.locator(".gc-header-right svg");
    expect(await headerIcons.count()).toBeGreaterThanOrEqual(5);
    // An unauthenticated console shows a neutral avatar, never "unavailable".
    await expect(page.locator(".gc-avatar")).toBeVisible();
    await expect(page.getByText(/identity is unavailable/i)).toHaveCount(0);
  });

  test("applies the Roboto typeface", async ({ page }) => {
    await page.goto("/ui/");
    const family = await page.evaluate(() => getComputedStyle(document.querySelector(".gcp")!).fontFamily);
    expect(family).toContain("Roboto");
  });

  // The empty state renders only on a successful empty read; this suite has no
  // identity provider, so the console reaches the enforcing simulator
  // unauthenticated and the read is rejected. The empty state (and every other
  // data-dependent view) is exercised over an authenticated read in the
  // relying-party suite (ui/e2e/shauth-rps.mjs).
});

test.describe("Automated accessibility audit", () => {
  // axe-core is a coarser net than the hand-measured contrast and landmark
  // assertions above — it catches defect classes those checks do not aim at
  // (missing form labels, invalid ARIA usage, duplicate IDs, list structure).
  // It runs against both themes and the ordinary, not-supported, and resource
  // detail page shapes, disabling only the colour-contrast rule (already
  // covered, more precisely, by the dedicated Contrast suite above, which
  // walks up to the actually-painted background rather than axe's own
  // heuristic).
  for (const theme of ["light", "dark"] as const) {
    // Deduplicated: DRAWER_PRODUCTS carries the whole catalog, including the
    // products the product navigation already lists, so the base entries would
    // otherwise repeat a test title.
    const AXE_TARGETS = [
      ...new Set([
        "/ui/cloudrun",
        "/ui/logging",
        "/ui/not-supported/kubernetes-engine",
        "/ui/cloudrun/example-job",
        ...DRAWER_PRODUCTS.map((product) => product.href),
        ...DRAWER_DETAIL_PAGES,
      ]),
    ];
    for (const target of AXE_TARGETS) {
      test(`${target} has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(target);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }

    const CREATE_DIALOGS = [
      { path: "/ui/gcs", createTestId: "gcs-create-bucket", dialogTestId: "gcs-create-dialog" },
      { path: "/ui/ar", createTestId: "ar-create-repo", dialogTestId: "ar-create-dialog" },
    ];
    for (const { path, createTestId, dialogTestId } of CREATE_DIALOGS) {
      test(`the ${dialogTestId} dialog has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(path);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        await page.getByTestId(createTestId).click();
        await expect(page.getByTestId(dialogTestId)).toBeVisible();
        const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }

    const DELETE_DIALOGS = [
      { path: "/ui/gcs/example-bucket", openTestId: "gcs-bucket-delete", dialogTestId: "gcs-delete-dialog" },
      { path: "/ui/ar/example-repo", openTestId: "ar-repo-delete", dialogTestId: "ar-delete-dialog" },
      { path: "/ui/functions/example-function", openTestId: "function-delete", dialogTestId: "function-delete-dialog" },
      { path: "/ui/cloudrun/example-job", openTestId: "cloudrun-job-delete", dialogTestId: "cloudrun-delete-dialog" },
    ];
    for (const { path, openTestId, dialogTestId } of DELETE_DIALOGS) {
      test(`the ${dialogTestId} dialog has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(path);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        await page.getByTestId(openTestId).click();
        await expect(page.getByTestId(dialogTestId)).toBeVisible();
        const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }

    // The compute create dialogs (with the embedded labels editor) and the Run
    // confirm this pass added get the same automated audit in both themes.
    const COMPUTE_DIALOGS = [
      { path: "/ui/cloudrun", openTestId: "cloudrun-create-job", dialogTestId: "cloudrun-create-dialog", addEnv: true },
      { path: "/ui/functions", openTestId: "function-create", dialogTestId: "function-create-dialog", addEnv: false },
      { path: "/ui/cloudrun/example-job", openTestId: "cloudrun-job-run", dialogTestId: "cloudrun-run-dialog", addEnv: false },
    ];
    for (const { path, openTestId, dialogTestId, addEnv } of COMPUTE_DIALOGS) {
      test(`the ${dialogTestId} dialog has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(path);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        await page.getByTestId(openTestId).click();
        await expect(page.getByTestId(dialogTestId)).toBeVisible();
        if (addEnv) {
          // Exercise a populated labels/env editor row so its inputs are audited.
          await page.getByTestId("cloudrun-create-env-label-add").click();
        }
        const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }
  }
});


// Every product page the corrected catalog links to renders its own real
// shell. The list reads are authenticated (this suite has no identity
// provider, so the enforcing simulator rejects them and the table reports the
// API's own error) — the page title, description and column headers are the
// console's own and render regardless, which is exactly what a "Not supported"
// stub would not do.
test.describe("Products the simulator implements", () => {
  for (const product of DRAWER_PRODUCTS.filter((candidate) => candidate.columns.length > 0)) {
    test(`${product.href} renders ${product.title} with its real table columns`, async ({ page }) => {
      await page.goto(product.href);
      await expect(page.getByRole("heading", { name: product.title, exact: true })).toBeVisible();
      await expect(page.locator(".gc-page-description").first()).toBeVisible();
      for (const column of product.columns) {
        await expect(page.getByRole("columnheader", { name: column, exact: true }).first()).toBeVisible();
      }
    });
  }

  // Load balancing and IAM are tab-strip pages rather than a single table, so
  // they assert their own tabs instead of column headers.
  test("/ui/loadbalancing presents the five Cloud Load Balancing collections as tabs", async ({ page }) => {
    await page.goto("/ui/loadbalancing");
    await expect(page.getByRole("heading", { name: "Load balancing" })).toBeVisible();
    for (const tab of ["Frontends", "Target proxies", "URL maps", "Backends", "Health checks"]) {
      await expect(page.getByRole("tab", { name: tab })).toBeVisible();
    }
  });

  test("/ui/iam presents the allow policy and roles tabs", async ({ page }) => {
    await page.goto("/ui/iam");
    await expect(page.getByRole("heading", { name: "IAM" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Permissions" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Roles" })).toBeVisible();
  });

  test("reaches every implemented product from the catalog drawer", async ({ page }) => {
    for (const product of DRAWER_PRODUCTS) {
      await page.goto("/ui/");
      await page.getByRole("button", { name: "Main menu" }).click();
      await page.getByTestId(`drawer-item-${product.id}`).click();
      await expect(page).toHaveURL(new RegExp(`${product.href}$`));
      await expect(page.getByRole("heading", { name: product.title, exact: true })).toBeVisible();
      await expect(page.getByTestId("nav-drawer")).toHaveCount(0);
    }
  });

  for (const detail of DRAWER_DETAIL_PAGES) {
    test(`${detail} renders its detail shell on an unauthenticated read`, async ({ page }) => {
      await page.goto(detail);
      await expect(page.locator(".gc-detail-back a")).toBeVisible();
      await expect(page.locator(".gc-message-error, .gc-loading").first()).toBeVisible();
    });
  }
});

// The create/delete dialogs this pass added get the same treatment as the
// existing ones: they render before (and regardless of) any cloud read, so
// their structure, validation, Escape handling and focus return are provable
// here, and their contrast and axe cleanliness are asserted in both themes.
const NEW_CREATE_DIALOGS = [
  { path: "/ui/dns", openTestId: "dns-create-zone", dialogTestId: "dns-create-dialog", name: "Create a DNS zone", submitTestId: "dns-create-submit", fill: [["dns-create-name", "example-zone"], ["dns-create-dnsname", "example.com."]] },
  { path: "/ui/sql", openTestId: "sql-create-instance", dialogTestId: "sql-create-dialog", name: "Create a Cloud SQL instance", submitTestId: "sql-create-submit", fill: [["sql-create-id", "example-instance"]] },
  { path: "/ui/firestore", openTestId: "firestore-create", dialogTestId: "firestore-create-dialog", name: "Create a Firestore database", submitTestId: "firestore-create-submit", fill: [] },
  { path: "/ui/spanner", openTestId: "spanner-create-instance", dialogTestId: "spanner-create-dialog", name: "Create a Spanner instance", submitTestId: "spanner-create-submit", fill: [["spanner-create-id", "example-instance"], ["spanner-create-name", "Example"]] },
  { path: "/ui/bigtable", openTestId: "bigtable-create-instance", dialogTestId: "bigtable-create-dialog", name: "Create a Bigtable instance", submitTestId: "bigtable-create-submit", fill: [["bigtable-create-id", "example-instance"], ["bigtable-create-name", "Example"]] },
  { path: "/ui/memorystore", openTestId: "memorystore-create-instance", dialogTestId: "memorystore-create-dialog", name: "Create a Redis instance", submitTestId: "memorystore-create-submit", fill: [["memorystore-create-id", "example-instance"]] },
  { path: "/ui/bigquery", openTestId: "bigquery-create-dataset", dialogTestId: "bigquery-create-dialog", name: "Create dataset", submitTestId: "bigquery-create-submit", fill: [["bigquery-create-id", "example_dataset"]] },
  { path: "/ui/pubsub", openTestId: "pubsub-create-topic", dialogTestId: "pubsub-create-topic-dialog", name: "Create a topic", submitTestId: "pubsub-create-topic-submit", fill: [["pubsub-create-topic-id", "example-topic"]] },
  { path: "/ui/apis", openTestId: "apis-enable", dialogTestId: "apis-enable-dialog", name: "Enable an API", submitTestId: "apis-enable-submit", fill: [["apis-enable-name", "run.googleapis.com"]] },
  { path: "/ui/kms", openTestId: "kms-create-ring", dialogTestId: "kms-create-ring-dialog", name: "Create key ring", submitTestId: "kms-create-ring-submit", fill: [["kms-create-ring-id", "example-ring"]] },
  { path: "/ui/secrets", openTestId: "secrets-create", dialogTestId: "secrets-create-dialog", name: "Create secret", submitTestId: "secrets-create-submit", fill: [["secrets-create-id", "example-secret"]] },
];

const NEW_DETAIL_DIALOGS = [
  { path: "/ui/compute/us-central1-a/example-vm", openTestId: "compute-instance-start", dialogTestId: "compute-start-dialog", name: "Start VM instance?" },
  { path: "/ui/compute/us-central1-a/example-vm", openTestId: "compute-instance-stop", dialogTestId: "compute-stop-dialog", name: "Stop VM instance?" },
  { path: "/ui/compute/us-central1-a/example-vm", openTestId: "compute-instance-delete", dialogTestId: "compute-delete-dialog", name: "Delete VM instance?" },
  { path: "/ui/dns/example-zone", openTestId: "dns-zone-delete", dialogTestId: "dns-delete-dialog", name: "Delete DNS zone?" },
  { path: "/ui/sql/example-instance", openTestId: "sql-instance-delete", dialogTestId: "sql-delete-dialog", name: "Delete instance?" },
  { path: "/ui/sql/example-instance", openTestId: "sql-instance-create-db", dialogTestId: "sql-create-db-dialog", name: "Create a database" },
  { path: "/ui/spanner/example-instance", openTestId: "spanner-instance-delete", dialogTestId: "spanner-delete-dialog", name: "Delete instance?" },
  { path: "/ui/bigtable/example-instance", openTestId: "bigtable-instance-delete", dialogTestId: "bigtable-delete-dialog", name: "Delete instance?" },
  { path: "/ui/bigquery/example_dataset", openTestId: "bigquery-dataset-delete", dialogTestId: "bigquery-delete-dialog", name: "Delete dataset?" },
  { path: "/ui/pubsub/example-topic", openTestId: "pubsub-topic-delete", dialogTestId: "pubsub-delete-topic-dialog", name: "Delete topic?" },
  { path: "/ui/pubsub/example-topic", openTestId: "pubsub-topic-create-sub", dialogTestId: "pubsub-create-sub-dialog", name: "Create a subscription" },
  { path: "/ui/eventarc/example-trigger", openTestId: "eventarc-trigger-delete", dialogTestId: "eventarc-delete-dialog", name: "Delete trigger?" },
  { path: "/ui/secrets/example-secret", openTestId: "secrets-detail-delete", dialogTestId: "secrets-delete-dialog", name: "Delete secret?" },
  { path: "/ui/secrets/example-secret", openTestId: "secrets-detail-add-version", dialogTestId: "secrets-add-version-dialog", name: "Add new version" },
];

test.describe("New product write flows", () => {
  for (const dialog of NEW_CREATE_DIALOGS) {
    test(`${dialog.path} offers "${dialog.name}" with a submit that validates first`, async ({ page }) => {
      await page.goto(dialog.path);
      const trigger = page.getByTestId(dialog.openTestId);
      await expect(trigger).toBeVisible();
      await trigger.click();
      const panel = page.getByTestId(dialog.dialogTestId);
      await expect(panel).toBeVisible();
      await expect(panel).toHaveAccessibleName(dialog.name);
      if (dialog.fill.length > 0) {
        await expect(panel.getByTestId(dialog.submitTestId)).toBeDisabled();
        for (const [testId, value] of dialog.fill) {
          await panel.getByTestId(testId).fill(value);
        }
      }
      await expect(panel.getByTestId(dialog.submitTestId)).toBeEnabled();
      await page.keyboard.press("Escape");
      await expect(page.getByTestId(dialog.dialogTestId)).toHaveCount(0);
      await expect(trigger).toBeFocused();
    });
  }

  for (const dialog of NEW_DETAIL_DIALOGS) {
    test(`${dialog.path} opens "${dialog.name}" and returns focus on Escape`, async ({ page }) => {
      await page.goto(dialog.path);
      const trigger = page.getByTestId(dialog.openTestId);
      await expect(trigger).toBeVisible();
      await trigger.click();
      const panel = page.getByTestId(dialog.dialogTestId);
      await expect(panel).toBeVisible();
      await expect(panel).toHaveAccessibleName(dialog.name);
      await page.keyboard.press("Escape");
      await expect(page.getByTestId(dialog.dialogTestId)).toHaveCount(0);
      await expect(trigger).toBeFocused();
    });
  }
});

test.describe("New product surfaces contrast and accessibility", () => {
  test("every new product page clears WCAG AA in both themes", async ({ page }) => {
    for (const product of DRAWER_PRODUCTS) {
      await page.goto(product.href);
      const results = await page.evaluate(sampleContrast, [
        ".gc-page-header h1",
        ".gc-page-description",
        ".gc-refresh",
        ".gc-filter-chip",
        ".gc-table th",
        ".gc-detail-heading",
        ".gc-tab",
        ".gc-tab-active",
      ]);
      assertAA(results);
    }
  });

  test("every new dialog clears WCAG AA in both themes", async ({ page }) => {
    for (const dialog of [...NEW_CREATE_DIALOGS, ...NEW_DETAIL_DIALOGS]) {
      await page.goto(dialog.path);
      await page.getByTestId(dialog.openTestId).click();
      await expect(page.getByTestId(dialog.dialogTestId)).toBeVisible();
      const results = await page.evaluate(sampleContrast, [
        ".gc-dialog-title",
        ".gc-dialog p",
        ".gc-field",
        ".gc-field-hint",
        ".gc-button-text",
        ".gc-button-primary",
      ]);
      assertAA(results);
    }
  });

  for (const theme of ["light", "dark"] as const) {
    test(`every new dialog has no detectable axe violations (${theme})`, async ({ page }) => {
      for (const dialog of [...NEW_CREATE_DIALOGS, ...NEW_DETAIL_DIALOGS]) {
        await page.goto(dialog.path);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        await page.getByTestId(dialog.openTestId).click();
        await expect(page.getByTestId(dialog.dialogTestId)).toBeVisible();
        const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
        expect(results.violations, `${dialog.path} (${theme}): ${JSON.stringify(results.violations, null, 2)}`).toEqual([]);
      }
    });
  }
});
