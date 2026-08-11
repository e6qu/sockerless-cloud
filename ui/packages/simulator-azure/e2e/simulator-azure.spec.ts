import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// Every list page's table carries a real Fluent `TableSelectionCell` — a
// `<td>` — as its very first cell in the header row, ahead of every
// `TableHeaderCell` (`<th>`). Per the browser's own HTML-AAM role mapping,
// that shifts the accessible role of the first `<th>` that follows it from
// "columnheader" to "rowheader" (the same convention a spreadsheet or data
// grid uses for the column that uniquely identifies each row) — real,
// correct Fluent Table behaviour, not a defect. Subscriptions' table has no
// selection column, so its first header cell stays a plain "columnheader".
const SERVICES = [
  {
    path: "/ui/subscriptions",
    menu: "Subscriptions",
    columns: ["Subscription name", "Subscription ID", "Status"],
    rowHeaderColumn: undefined as string | undefined,
  },
  { path: "/ui/container-apps", menu: "Container App jobs", columns: ["Name", "Resource group", "Type"], rowHeaderColumn: "Name" },
  { path: "/ui/functions", menu: "Function Apps", columns: ["Name", "Resource group", "App kind"], rowHeaderColumn: "Name" },
  { path: "/ui/acr", menu: "Container registries", columns: ["Name", "Login server"], rowHeaderColumn: "Name" },
  { path: "/ui/storage", menu: "Storage accounts", columns: ["Name", "Kind"], rowHeaderColumn: "Name" },
  { path: "/ui/monitor", menu: "Logs", columns: ["Time generated", "Source", "Message"], rowHeaderColumn: "Time generated" },
  {
    path: "/ui/entra/app-registrations",
    menu: "App registrations",
    columns: ["Display name", "Application (client) ID", "Object ID"],
    rowHeaderColumn: "Display name",
  },
];

// The descriptor-driven service blades (src/services.ts). Each reads one real
// Azure Resource Manager subscription-wide List operation and opens a resource
// on that provider's real Get. `column` is a distinctive header from the
// blade's own column set — one that names a property the resource's Azure REST
// specification documents, so a blade silently losing its real shape fails
// here. These are pinned literally rather than imported from src so this suite
// stays a black-box check of what the built portal actually renders.
const BLADE_SERVICES = [
  { path: "/ui/resource-groups", menu: "Resource groups", column: "Provisioning state", testid: "rg", detailKind: "Resource group" },
  { path: "/ui/virtual-machines", menu: "Virtual machines", column: "Size", testid: "vm", detailKind: "Virtual machine" },
  { path: "/ui/app-service", menu: "App Service", column: "Default domain", testid: "webapp", detailKind: "App Service" },
  { path: "/ui/app-service-plans", menu: "App Service plans", column: "Pricing tier", testid: "plan", detailKind: "App Service plan" },
  { path: "/ui/containerapps", menu: "Container Apps", column: "Environment", testid: "capp", detailKind: "Container App" },
  { path: "/ui/container-instances", menu: "Container instances", column: "OS type", testid: "aci", detailKind: "Container group" },
  { path: "/ui/container-app-environments", menu: "Container Apps environments", column: "Provisioning state", testid: "cae", detailKind: "Container Apps environment" },
  { path: "/ui/cosmos-db", menu: "Azure Cosmos DB", column: "API", testid: "cosmos", detailKind: "Cosmos DB account" },
  { path: "/ui/postgresql", menu: "Azure Database for PostgreSQL", column: "Version", testid: "pg", detailKind: "PostgreSQL flexible server" },
  { path: "/ui/redis", menu: "Azure Cache for Redis", column: "Pricing tier", testid: "redis", detailKind: "Azure Cache for Redis" },
  { path: "/ui/virtual-networks", menu: "Virtual networks", column: "Address space", testid: "vnet", detailKind: "Virtual network" },
  { path: "/ui/load-balancers", menu: "Load balancers", column: "Pricing tier", testid: "lb", detailKind: "Load balancer" },
  { path: "/ui/network-security-groups", menu: "Network security groups", column: "Provisioning state", testid: "nsg", detailKind: "Network security group" },
  { path: "/ui/public-ip-addresses", menu: "Public IP addresses", column: "IP address", testid: "pip", detailKind: "Public IP address" },
  { path: "/ui/network-interfaces", menu: "Network interfaces", column: "MAC address", testid: "nic", detailKind: "Network interface" },
  { path: "/ui/route-tables", menu: "Route tables", column: "Provisioning state", testid: "rt", detailKind: "Route table" },
  { path: "/ui/nat-gateways", menu: "NAT gateways", column: "Pricing tier", testid: "nat", detailKind: "NAT gateway" },
  { path: "/ui/dns-zones", menu: "DNS zones", column: "Record sets", testid: "dns", detailKind: "DNS zone" },
  { path: "/ui/private-dns-zones", menu: "Private DNS zones", column: "Virtual network links", testid: "pdns", detailKind: "Private DNS zone" },
  { path: "/ui/key-vaults", menu: "Key vaults", column: "Vault URI", testid: "kv", detailKind: "Key vault" },
  { path: "/ui/service-bus", menu: "Service Bus", column: "Pricing tier", testid: "sb", detailKind: "Service Bus namespace" },
  { path: "/ui/event-hubs", menu: "Event Hubs", column: "Pricing tier", testid: "eh", detailKind: "Event Hubs namespace" },
  { path: "/ui/event-grid-topics", menu: "Event Grid topics", column: "Endpoint", testid: "egt", detailKind: "Event Grid topic" },
  { path: "/ui/event-grid-domains", menu: "Event Grid domains", column: "Endpoint", testid: "egd", detailKind: "Event Grid domain" },
  { path: "/ui/event-grid-system-topics", menu: "Event Grid system topics", column: "Topic type", testid: "egs", detailKind: "Event Grid system topic" },
  { path: "/ui/api-management", menu: "API Management services", column: "Pricing tier", testid: "apim", detailKind: "API Management service" },
  { path: "/ui/logic-apps", menu: "Logic apps", column: "Status", testid: "logic", detailKind: "Logic app" },
  { path: "/ui/application-insights", menu: "Application Insights", column: "Application type", testid: "appi", detailKind: "Application Insights resource" },
  { path: "/ui/log-analytics", menu: "Log Analytics workspaces", column: "Workspace ID", testid: "law", detailKind: "Log Analytics workspace" },
  { path: "/ui/managed-identities", menu: "Managed Identities", column: "Client ID", testid: "msi", detailKind: "User-assigned managed identity" },
];

test.describe("Azure portal shell", () => {
  test("presents the portal header, breadcrumb, resource title and command bar", async ({ page }) => {
    await page.goto("/ui/");
    expect((await page.request.get("/ui/favicon.svg")).status()).toBe(200);
    await expect(page.locator(".az-header")).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Simulator" })).toBeVisible();
    await expect(page.getByRole("toolbar", { name: "Commands" })).toBeVisible();
    await expect(page.getByLabel("Search resources, services, and docs")).toBeVisible();
  });

  // A fixed-height header leaves anything taller drawn outside its box, where
  // the bar below paints over it. A control covered by an opaque sibling still
  // reports as visible, so the failure shows up only as clicks landing on the
  // wrong element — as it did on the AWS console. Assert containment.
  test("contains every header control within the header itself", async ({ page }) => {
    await page.goto("/ui/");
    const header = page.locator(".az-header");
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
    // Cloud Shell, Notifications, and Help sit alongside the theme toggle in
    // the header's right-hand group (see "Header disclosure controls"
    // below), so the toggle is targeted by its own testid rather than the
    // shared `.az-icon-button` class every header icon button now carries.
    const toggle = page.getByTestId("az-theme-toggle");
    const headerBox = await page.locator(".az-header").boundingBox();
    const toggleBox = await toggle.boundingBox();
    expect(toggleBox!.x).toBeGreaterThan(headerBox!.x + headerBox!.width / 2);

    const isDark = () => page.evaluate(() => document.documentElement.classList.contains("dark"));
    const before = await isDark();
    await toggle.click();
    expect(await isDark()).toBe(!before);
    await toggle.click();
    expect(await isDark()).toBe(before);
  });

  test("sends a visitor at the bare origin to the portal", async ({ page }) => {
    // Azure Cosmos DB owns the simulator's API root, because the azcosmos SDK's
    // global endpoint manager reads account properties from it. A browser
    // carries none of Cosmos's data-plane markers, so the root hands it to the
    // portal instead of answering a bare 404; a real Cosmos client still gets
    // account properties from the same URL, which the Go and SDK suites pin.
    await page.goto("/");
    await expect(page).toHaveURL("http://localhost:19330/ui/");
  });

  test("exposes a skip link ahead of the portal content", async ({ page }) => {
    await page.goto("/ui/");
    await page.locator("body").focus();
    await page.keyboard.press("Tab");
    await expect(page.locator(".sl-skip-link")).toBeFocused();
    await expect(page.locator("#main-content")).toHaveCount(1);
  });
});

// The real Azure global header carries Cloud Shell, Notifications, and Help
// alongside the account and Settings (theme) controls. This simulator backs
// none of the first three with a real feature, so each discloses that
// honestly in a small panel instead of acting — a labelled, accessible
// affordance rather than a dead icon that looks live.
test.describe("Header disclosure controls", () => {
  for (const [testid, heading] of [
    ["az-cloud-shell", "Cloud Shell"],
    ["az-notifications", "Notifications"],
    ["az-help", "Help + support"],
  ] as const) {
    test(`${testid} opens an honest panel, closes on Escape, and returns focus`, async ({ page }) => {
      await page.goto("/ui/");
      const button = page.getByTestId(testid);
      await expect(button).toHaveAttribute("aria-expanded", "false");
      await button.click();
      await expect(button).toHaveAttribute("aria-expanded", "true");
      const panel = page.getByRole("dialog", { name: await button.getAttribute("aria-label") ?? "" });
      await expect(panel.getByRole("heading", { name: heading })).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(panel).toHaveCount(0);
      await expect(button).toBeFocused();
    });
  }
});

// These pin the ground-truth values of the Azure portal's visual language — the
// header blue and the Fluent-style command, status, and filter icons — so a
// regression away from the Azure look fails here rather than being judged by eye.
test.describe("Fluent visual fidelity", () => {
  test("paints the header in the iconic Azure brand blue", async ({ page }) => {
    await page.goto("/ui/");
    const color = await page.locator(".az-header").evaluate((node) => getComputedStyle(node).backgroundColor);
    // rgb(0, 120, 212) = #0078d4 = the real Azure portal's signature header
    // blue, applied by overriding Fluent's brand-background tokens on the
    // FluentProvider theme (azureLightTheme/azureDarkTheme in AzureApp.tsx)
    // rather than accepting Fluent's stock "Web" brand (#0f6cbd). Every other
    // Fluent behaviour is unchanged.
    expect(color).toBe("rgb(0, 120, 212)");
  });

  test("draws the command bar and its controls with Fluent-style icons", async ({ page }) => {
    await page.goto("/ui/container-apps");
    // Every command carries a real icon rather than a text glyph.
    const commands = page.getByRole("toolbar", { name: "Commands" }).getByRole("button");
    const count = await commands.count();
    expect(count).toBeGreaterThan(0);
    for (let index = 0; index < count; index += 1) {
      await expect(commands.nth(index).locator("svg")).toHaveCount(1);
    }
    // The header search carries a magnifier icon.
    await expect(page.locator(".az-header-search svg")).toHaveCount(1);
  });
});

test.describe("Service menu", () => {
  test("groups services the way the real Azure portal's All services catalog does", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    for (const group of [
      "General",
      "Compute",
      "Containers",
      "Storage",
      "Databases",
      "Networking",
      "Integration",
      "Monitoring + management",
      "Identity",
      "Microsoft Entra ID",
      "Security",
      "DevOps",
    ]) {
      await expect(menu.getByRole("button", { name: group })).toBeVisible();
    }
  });

  // The Containers group holds "Container Apps" (Microsoft.App/containerApps),
  // "Container App jobs" (Microsoft.App/jobs), and "Container Apps
  // environments" (Microsoft.App/managedEnvironments), so this matches the
  // exact accessible name rather than a prefix of it.
  test("collapses a group without losing the others", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    await expect(menu.getByRole("link", { name: "Container Apps", exact: true })).toBeVisible();
    await menu.getByRole("button", { name: "Containers" }).click();
    await expect(menu.getByRole("link", { name: "Container Apps", exact: true })).toHaveCount(0);
    await expect(menu.getByRole("link", { name: "Storage accounts" })).toBeVisible();
    await menu.getByRole("button", { name: "Containers" }).click();
    await expect(menu.getByRole("link", { name: "Container Apps", exact: true })).toBeVisible();
  });

  test("narrows to what a search matches, opening a collapsed group to show it", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    await menu.getByRole("button", { name: "Containers" }).click();
    await menu.getByLabel("Search the service menu").fill("registries");
    await expect(menu.getByRole("link", { name: "Container registries" })).toBeVisible();
    await expect(menu.getByRole("link", { name: "Storage accounts" })).toHaveCount(0);
  });
});

// The real Azure portal's service menu lists Azure's whole catalog; this
// simulator implements a slice of it. Rather than omitting the rest — which
// would make the menu look like a different, smaller product — every
// service Azure offers that this simulator doesn't implement stays in the
// menu, marked honestly.
//
// The badge is a claim about the simulator, so it is held to exactly the
// services with no Azure Resource Manager surface here at all. Azure
// Kubernetes Service is one: the simulator serves no Microsoft.ContainerService
// routes. Services it does serve — virtual machines, Cosmos DB, key vaults,
// virtual networks, Service Bus, and the rest — carry real blades and must
// never carry this badge; the vitest catalog suite pins that list exactly.
test.describe("Not supported services", () => {
  test("marks an unimplemented service with a non-color badge and an accessible name that says so", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    // Every group opens expanded by default — a click here would collapse
    // "Containers", not open it.
    const link = menu.getByRole("link", { name: "Azure Kubernetes Service, not supported in this simulator" });
    await expect(link).toBeVisible();
    // The badge is real text content, not a colour swatch — a screen reader
    // announces "Not supported" whether or not it renders the icon in front
    // of it.
    await expect(link).toContainText("Not supported");
    await expect(link.locator("svg")).toHaveCount(1);
  });

  test("still navigates — to a small, honest explanation — rather than a dead end", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    await menu.getByRole("link", { name: "Azure Kubernetes Service, not supported in this simulator" }).click();
    await expect(page).toHaveURL(/\/ui\/not-supported\/azure-kubernetes-service$/);
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    await expect(crumbs).toContainText("Azure Kubernetes Service");
    await expect(page.getByRole("heading", { name: "Azure Kubernetes Service" })).toBeVisible();
    await expect(page.getByTestId("not-supported-message")).toContainText(
      "Azure Kubernetes Service is not implemented by the Sockerless simulator.",
    );
    // The Essentials panel still leads the pane, carrying the badge as a
    // real Essentials value rather than a special-cased layout.
    await expect(page.getByRole("region", { name: "Essentials" })).toContainText("Not supported");
  });

  // The badge's accuracy in the other direction: a service the simulator
  // really does implement must reach a real blade, never the honest-but-wrong
  // "not implemented" page.
  test("never badges a service this simulator serves Azure Resource Manager routes for", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    for (const service of BLADE_SERVICES) {
      await expect(menu.getByRole("link", { name: service.menu, exact: true })).toHaveAttribute(
        "href",
        service.path,
      );
      await expect(
        menu.getByRole("link", { name: `${service.menu}, not supported in this simulator` }),
      ).toHaveCount(0);
    }
  });

  test("keeps the not-supported badge to the services with no simulated provider at all", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    const badged = await menu.locator(".az-service-link-unsupported .az-service-label").allTextContents();
    expect(badged.map((label) => label.trim())).toEqual([
      "Management groups",
      "Azure Kubernetes Service",
      "Azure SQL",
      "Front Door and CDN profiles",
      "Cost Management + Billing",
      "Microsoft Defender for Cloud",
      "Azure DevOps organizations",
    ]);
  });

  test("keeps every catalog service reachable by keyboard, supported or not", async ({ page }) => {
    await page.goto("/ui/");
    const menu = page.getByRole("navigation", { name: "Service" });
    // Every group opens expanded by default, so every catalog item is
    // already present without expanding anything.
    // Every link in the service menu is a real, focusable anchor — this is
    // what "not a dead end" means operationally: Tab reaches it and Enter
    // activates it like any other link.
    const links = menu.getByRole("link");
    const count = await links.count();
    expect(count).toBeGreaterThan(10);
    for (let index = 0; index < count; index += 1) {
      await expect(links.nth(index)).toHaveAttribute("href", /^\/ui\//);
    }
  });
});

test.describe("Overview", () => {
  test("leads with Essentials", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("region", { name: "Essentials" })).toBeVisible();
    // The per-resource counts and the links they carry require an authenticated
    // read; this lightweight suite has no identity provider, so the portal
    // reaches the enforcing simulator unauthenticated. The counts are proven in
    // the relying-party suite (ui/e2e/shauth-rps.mjs).
  });
});

for (const service of SERVICES) {
  test.describe(service.menu, () => {
    test("renders Essentials and its table columns", async ({ page }) => {
      await page.goto(service.path);
      await expect(page.getByRole("region", { name: "Essentials" })).toBeVisible();
      for (const column of service.columns) {
        const role = column === service.rowHeaderColumn ? "rowheader" : "columnheader";
        await expect(page.getByRole(role, { name: column, exact: true })).toBeVisible();
      }
    });
  });
}

test.describe("Subscriptions", () => {
  test("offers Add with the create-subscription form driving the alias API", async ({ page }) => {
    await page.goto("/ui/subscriptions");
    await expect(page.getByTestId("subs-table")).toBeVisible();
    await page.getByRole("button", { name: "Add", exact: true }).click();
    await expect(page.getByTestId("subs-create-form")).toBeVisible();
    await expect(page.getByTestId("subs-create-name")).toBeVisible();
    // The billing scope is prefilled with an enrollment-account coordinate;
    // the submit stays disabled until the subscription has a name.
    await expect(page.getByTestId("subs-create-scope")).toHaveValue(/billingAccounts/);
    await expect(page.getByTestId("subs-create-submit")).toBeDisabled();
    await page.getByTestId("subs-create-name").fill("Structural Test Subscription");
    await expect(page.getByTestId("subs-create-submit")).toBeEnabled();
  });

  test("keeps the subscription detail blade's lifecycle commands visible but gated", async ({ page }) => {
    await page.goto("/ui/subscriptions/00000000-0000-0000-0000-000000000001");
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    await expect(crumbs).toContainText("Subscriptions");
    await expect(crumbs).toContainText("Subscription");
    await expect(page.getByTestId("subs-detail")).toBeVisible();
    // This lightweight suite has no identity provider, so the portal reaches
    // the enforcing simulator unauthenticated: the detail read surfaces its
    // error loudly and both lifecycle commands stay greyed rather than hidden.
    await expect(page.getByTestId("subs-cancel")).toBeVisible();
    await expect(page.getByTestId("subs-enable")).toBeVisible();
    await expect(page.getByTestId("subs-cancel")).toBeDisabled();
    await expect(page.getByTestId("subs-enable")).toBeDisabled();
    await expect(page.getByTestId("subs-detail-error")).toBeVisible();
    await expect(page.getByTestId("subs-detail-error")).toContainText("HTTP 401");
  });
});

test.describe("Microsoft Entra ID: App registrations", () => {
  test("offers New registration and the credential blade structure", async ({ page }) => {
    await page.goto("/ui/entra/app-registrations");
    await expect(page.getByRole("button", { name: "New registration" })).toBeVisible();
    await page.getByRole("button", { name: "New registration" }).click();
    await expect(page.getByTestId("entra-app-name-input")).toBeVisible();
    // The submit stays disabled until the application has a name.
    await expect(page.getByTestId("entra-register-submit")).toBeDisabled();
  });

  test("surfaces a loud error for an app registration Microsoft Graph does not know", async ({ page }) => {
    await page.goto("/ui/entra/app-registrations/00000000-dead-beef-0000-000000000000");
    // The blade's breadcrumb keeps the way back to the listing.
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    await expect(crumbs).toContainText("App registrations");
    await expect(crumbs).toContainText("Certificates & secrets");
    await expect(page.getByTestId("entra-app-error")).toBeVisible();
    await expect(page.getByTestId("entra-app-error")).toContainText("HTTP 404");
  });
});

// Storage accounts and Container registries used to be read-only in this
// console — list and detail, no way to create a resource, even though the
// real portal lets an operator create a storage account or registry from
// the same blade. Each now offers a real "Create" command-bar action that
// opens an inline Fluent form wired to the real Microsoft.Storage /
// Microsoft.ContainerRegistry PUT, matching the Subscriptions "Add" and
// App registrations "New registration" forms above: the command and the
// form render before (and regardless of) any cloud read, so they are
// assertable here without an identity provider. The authenticated
// create→list-appears round trip belongs in the relying-party suite
// (ui/e2e/shauth-rps.mjs), the same split those forms document.
test.describe("Resource creation", () => {
  // Storage accounts, Container registries, Container App jobs, and Function
  // Apps each offer a real "Create" command-bar action opening an inline Fluent
  // form wired to the resource's real ARM PUT (matching the Subscriptions "Add"
  // and App registrations "New registration" forms): the command and the form
  // render before (and regardless of) any cloud read, so they are assertable
  // here without an identity provider. The authenticated create→list-appears
  // round trip belongs in the relying-party suite (ui/e2e/shauth-rps.mjs).
  const cases = [
    { path: "/ui/storage", formTestId: "storage-create-form", nameTestId: "storage-create-name", submitTestId: "storage-create-submit", validName: "mynewstorageacct1" },
    { path: "/ui/acr", formTestId: "acr-create-form", nameTestId: "acr-create-name", submitTestId: "acr-create-submit", validName: "mynewregistry1" },
    { path: "/ui/functions", formTestId: "fn-create-form", nameTestId: "fn-create-name", submitTestId: "fn-create-submit", validName: "mynewfuncapp1" },
  ];

  for (const { path, formTestId, nameTestId, submitTestId, validName } of cases) {
    test(`${path} offers Create and opens the create form with an initially-disabled submit`, async ({ page }) => {
      await page.goto(path);
      const create = page.getByRole("toolbar", { name: "Commands" }).getByRole("button", { name: "Create", exact: true });
      await expect(create).toBeVisible();
      await create.click();
      const form = page.getByTestId(formTestId);
      await expect(form).toBeVisible();
      const input = form.getByTestId(nameTestId);
      await expect(input).toBeVisible();
      // An empty or invalid name must not be submittable.
      await expect(form.getByTestId(submitTestId)).toBeDisabled();
      await input.fill(validName);
      await expect(form.getByTestId(submitTestId)).toBeEnabled();
      await form.getByRole("button", { name: "Cancel" }).click();
      await expect(page.getByTestId(formTestId)).toHaveCount(0);
    });
  }

  // The Container App job create form needs a name AND a container image
  // before it will submit, so it gets a dedicated case rather than the
  // name-only pattern above.
  test("/ui/container-apps offers Create and gates submit on a valid name and image", async ({ page }) => {
    await page.goto("/ui/container-apps");
    await page.getByRole("toolbar", { name: "Commands" }).getByRole("button", { name: "Create", exact: true }).click();
    const form = page.getByTestId("ca-job-create-form");
    await expect(form).toBeVisible();
    await expect(form.getByTestId("ca-job-create-submit")).toBeDisabled();
    await form.getByTestId("ca-job-create-name").fill("mynewjob1");
    // Name alone is not enough — the image is required too.
    await expect(form.getByTestId("ca-job-create-submit")).toBeDisabled();
    await form.getByTestId("ca-job-create-image").fill("alpine:3.20");
    await expect(form.getByTestId("ca-job-create-submit")).toBeEnabled();
    await form.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("ca-job-create-form")).toHaveCount(0);
  });
});

// Every resource detail blade now carries the real portal's UPDATE and
// lifecycle command-bar actions (Update/Configuration, Edit tags, the Function
// App Start/Stop/Restart, the Container App job Edit). Opening the editor or
// firing an action needs a successfully loaded resource — an authenticated
// cloud read this lightweight suite (no identity provider) never gets — so,
// matching the Delete-command convention above, this pins that each action
// renders, and disabled, on its blade. The authenticated open→edit→PATCH/PUT
// and open→action→POST round trips run in the mocked-fetch vitest suite
// (ResourceUpdateFlows.test.tsx) and the relying-party suite.
test.describe("Resource update and lifecycle actions", () => {
  const ACTION_COMMANDS: { path: string; testids: string[] }[] = [
    { path: "/ui/acr/structuraltestregistry", testids: ["acr-registry-update", "acr-registry-tags"] },
    { path: "/ui/storage/structuraltestaccount", testids: ["storage-account-config", "storage-account-tags"] },
    { path: "/ui/container-apps/structural-test-job", testids: ["ca-job-edit", "ca-job-tags"] },
    { path: "/ui/functions/structural-test-site", testids: ["fn-site-start", "fn-site-stop", "fn-site-restart", "fn-site-tags"] },
  ];

  for (const { path, testids } of ACTION_COMMANDS) {
    test(`${path} offers real, initially-disabled update/lifecycle commands`, async ({ page }) => {
      await page.goto(path);
      for (const testid of testids) {
        const command = page.getByTestId(testid);
        await expect(command).toBeVisible();
        await expect(command).toBeDisabled();
      }
    });
  }
});

// The four resource detail blades pass 2 added. This lightweight suite has
// no identity provider (see the Subscriptions/Entra suites above), so each
// read reaches the enforcing simulator unauthenticated and surfaces a loud
// HTTP 401 rather than rendering as if the read had succeeded — the same
// unauthenticated-safe convention the rest of this file follows. Reads that
// only succeed once the shell's Essentials data loads are covered by the
// relying-party suite (ui/e2e/shauth-rps.mjs).
const DETAIL_BLADES = [
  { path: "/ui/container-apps/structural-test-job", parent: "Container App jobs", testid: "ca-job-error", command: "Run now", deleteTestId: "ca-job-delete" },
  { path: "/ui/functions/structural-test-site", parent: "Function Apps", testid: "fn-site-error", command: "Refresh", deleteTestId: "fn-site-delete" },
  { path: "/ui/acr/structuraltestregistry", parent: "Container registries", testid: "acr-registry-error", command: "Refresh", deleteTestId: "acr-registry-delete" },
  { path: "/ui/storage/structuraltestaccount", parent: "Storage accounts", testid: "storage-account-error", command: "Refresh", deleteTestId: "storage-account-delete" },
];

test.describe("Resource detail blades", () => {
  for (const blade of DETAIL_BLADES) {
    test(`${blade.path} carries the breadcrumb, command bar, and a loud unauthenticated error`, async ({ page }) => {
      await page.goto(blade.path);
      const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
      await expect(crumbs).toContainText(blade.parent);
      await expect(page.getByRole("toolbar", { name: "Commands" }).getByRole("button", { name: blade.command })).toBeVisible();
      await expect(page.getByTestId(blade.testid)).toBeVisible();
      await expect(page.getByTestId(blade.testid)).toContainText("HTTP 401");
    });
  }
});

// Every resource-DELETE flow this pass added (list-page multi-select Delete,
// detail-blade Delete) is real: a real Fluent confirm `Dialog`, a real ARM
// DELETE on confirm. Opening that dialog needs a selected row (list) or a
// successfully loaded resource (detail) — both require an authenticated
// cloud read, which this lightweight suite (no identity provider) never
// gets. So, matching the AWS console's identical constraint (see
// simulator-aws.spec.ts's "Resource header actions" / "Resource detail
// pages"), this suite pins only what's reachable without live data: the
// Delete command renders, and disabled, on every list and detail page. The
// dialog itself — structure, confirm→DELETE, error surfacing, and
// Escape-to-close — is proven in the mocked-fetch vitest suite
// (ResourceDeleteFlows.test.tsx); the authenticated
// select→confirm→delete→gone round trip belongs in the relying-party suite
// (ui/e2e/shauth-rps.mjs).
test.describe("Resource delete actions", () => {
  const LIST_DELETES = [
    { path: "/ui/acr", testid: "acr-delete" },
    { path: "/ui/storage", testid: "storage-delete" },
    { path: "/ui/container-apps", testid: "ca-delete" },
    { path: "/ui/functions", testid: "fn-delete" },
  ];

  for (const { path, testid } of LIST_DELETES) {
    test(`${path} offers a real, initially-disabled Delete command`, async ({ page }) => {
      await page.goto(path);
      const command = page.getByTestId(testid);
      await expect(command).toBeVisible();
      await expect(command).toHaveText("Delete");
      await expect(command).toBeDisabled();
    });
  }

  for (const blade of DETAIL_BLADES) {
    test(`${blade.path} offers a real, initially-disabled Delete command`, async ({ page }) => {
      await page.goto(blade.path);
      const command = page.getByTestId(blade.deleteTestId);
      await expect(command).toBeVisible();
      await expect(command).toHaveText("Delete");
      await expect(command).toBeDisabled();
    });
  }
});

// The service blades this pass added. Each is a real Azure Resource Manager
// blade — a subscription-wide List on the listing pane, the provider's own Get
// plus its sub-resource Lists on the resource pane — for a service the
// simulator implements and the menu previously marked "Not supported". This
// lightweight suite has no identity provider, so every read reaches the
// enforcing simulator unauthenticated: the structure below (Essentials, the
// blade's real columns, the command bar, and a loud HTTP 401) is what is
// assertable without live data, matching the convention the rest of this file
// follows. The authenticated read → render round trips run in the mocked-fetch
// vitest suite (ServiceBlades.test.tsx) and the relying-party suite.
test.describe("Service blades", () => {
  for (const service of BLADE_SERVICES) {
    test(`${service.path} renders Essentials, its real columns, and a Delete command`, async ({ page }) => {
      await page.goto(service.path);
      await expect(page.getByRole("region", { name: "Essentials" })).toBeVisible();
      // Fluent's TableSelectionCell precedes the first header cell, mapping it
      // to rowheader; the rest stay columnheaders.
      await expect(page.getByRole("rowheader", { name: "Name", exact: true })).toBeVisible();
      await expect(page.getByRole("columnheader", { name: service.column, exact: true })).toBeVisible();
      const remove = page.getByTestId(`${service.testid}-delete`);
      await expect(remove).toBeVisible();
      await expect(remove).toHaveText("Delete");
      await expect(remove).toBeDisabled();
    });

    test(`${service.path} opens a resource pane with a breadcrumb and a loud unauthenticated error`, async ({ page }) => {
      await page.goto(`${service.path}/structural-test-resource`);
      const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
      await expect(crumbs).toContainText(service.menu);
      await expect(crumbs).toContainText(service.detailKind);
      await expect(page.getByRole("toolbar", { name: "Commands" }).getByRole("button", { name: "Refresh" })).toBeVisible();
      const error = page.getByTestId(`${service.testid}-detail-error`);
      await expect(error).toBeVisible();
      await expect(error).toContainText("HTTP 401");
      // The blade's write and lifecycle commands stay visible but gated on a
      // resource that never loaded, rather than disappearing.
      await expect(page.getByTestId(`${service.testid}-tags`)).toBeDisabled();
      await expect(page.getByTestId(`${service.testid}-detail-delete`)).toBeDisabled();
    });
  }

  test("reaches every service blade from the menu and updates the breadcrumb", async ({ page }) => {
    await page.goto("/ui/");
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    for (const service of BLADE_SERVICES) {
      await page.getByRole("link", { name: service.menu, exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`${service.path}$`));
      await expect(crumbs).toContainText(service.menu);
    }
  });

  // The lifecycle actions Azure Resource Manager models as action POSTs on the
  // resource — a virtual machine's start/restart/power-off/deallocate, a
  // PostgreSQL flexible server's and a container group's start/stop/restart.
  const LIFECYCLE = [
    { path: "/ui/virtual-machines/structural-test-resource", testids: ["vm-start", "vm-restart", "vm-poweroff", "vm-deallocate"] },
    { path: "/ui/postgresql/structural-test-resource", testids: ["pg-start", "pg-restart", "pg-stop"] },
    { path: "/ui/container-instances/structural-test-resource", testids: ["aci-start", "aci-restart", "aci-stop"] },
    { path: "/ui/app-service/structural-test-resource", testids: ["webapp-start", "webapp-restart", "webapp-stop"] },
    { path: "/ui/containerapps/structural-test-resource", testids: ["capp-start", "capp-stop"] },
  ];

  for (const { path, testids } of LIFECYCLE) {
    test(`${path} offers real, initially-disabled lifecycle commands`, async ({ page }) => {
      await page.goto(path);
      for (const testid of testids) {
        const command = page.getByTestId(testid);
        await expect(command).toBeVisible();
        await expect(command).toBeDisabled();
      }
    });
  }
});

test.describe("Navigation", () => {
  test("reaches every service from the menu and updates the breadcrumb", async ({ page }) => {
    await page.goto("/ui/");
    const crumbs = page.getByRole("navigation", { name: "Breadcrumbs" });
    for (const service of SERVICES) {
      await page.getByRole("link", { name: service.menu, exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`${service.path}$`));
      await expect(crumbs).toContainText(service.menu);
    }
    await page.getByRole("link", { name: "Overview", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Simulator" })).toBeVisible();
  });
});

test.describe("Contrast", () => {
  // Measured against the surfaces the browser actually paints, walking up for
  // the first opaque background, rather than asserted from the palette. The
  // header sits at 4.53:1 — white on the portal's own header blue — so this
  // has little room to spare and is worth holding.
  test("every text role clears WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/container-apps");
    const results = await page.evaluate(() => {
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
      const selectors = [
        ".az-wordmark",
        ".az-breadcrumbs a",
        ".az-resource-title h1",
        ".az-resource-title p",
        ".az-command:not(:disabled)",
        ".az-service-link",
        ".az-service-link-active",
        ".az-service-group-toggle",
        ".az-essentials-pair dt",
        ".az-essentials-pair dd",
        ".az-table th",
        ".az-empty strong",
        ".az-empty p",
      ];
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
    });

    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBeGreaterThan(10);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  // The "Not supported" badge and its dimmer service-menu label are the one
  // surface this pass added; they get the same measured-not-assumed check
  // rather than trusting the palette comment in tokens.css.
  test("the not-supported badge and its service-menu label clear WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/not-supported/azure-kubernetes-service");
    const results = await page.evaluate(() => {
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
      const selectors = [".az-badge-unsupported", ".az-service-link-unsupported .az-service-label"];
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
    });

    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBe(2);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  // The header disclosure panel (Cloud Shell/Notifications/Help) is the
  // other surface this pass added — measured the same way rather than
  // trusted because it reuses existing tokens.
  test("the header disclosure panel's heading, body, and link clear WCAG AA in both themes", async ({ page }) => {
    await page.goto("/ui/");
    await page.getByTestId("az-help").click();
    const results = await page.evaluate(() => {
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
      const selectors = [".az-header-panel h2", ".az-header-panel p", ".az-header-panel a"];
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
    });

    for (const [theme, samples] of Object.entries(results)) {
      expect(samples.length).toBe(3);
      for (const sample of samples) {
        expect(sample.ratio, `${theme}: ${sample.selector} measured ${sample.ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

test.describe("ARIA landmarks and keyboard state", () => {
  test("exposes the header, service menu, breadcrumbs, and content as named landmarks", async ({ page }) => {
    await page.goto("/ui/container-apps");
    // <header> at the top level is the banner landmark; <main> is the main
    // landmark; both nav regions carry the accessible names the rest of the
    // suite already navigates by.
    await expect(page.getByRole("banner")).toBeVisible();
    await expect(page.getByRole("main")).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Service" })).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Breadcrumbs" })).toBeVisible();
    await expect(page.getByRole("region", { name: "Essentials" })).toBeVisible();
  });

  test("marks the active service with aria-current, and only the active one", async ({ page }) => {
    await page.goto("/ui/container-apps");
    const menu = page.getByRole("navigation", { name: "Service" });
    await expect(menu.getByRole("link", { name: "Container App jobs", exact: true })).toHaveAttribute("aria-current", "page");
    await expect(menu.getByRole("link", { name: "Container Apps", exact: true })).not.toHaveAttribute("aria-current", "page");
    await expect(menu.getByRole("link", { name: "Function Apps", exact: true })).not.toHaveAttribute("aria-current", "page");
  });

  // Fluent's real focus-visible signal is `data-fui-focus-visible`, set by
  // tabster's own keyboard-vs-pointer heuristic — armed only by genuine
  // keyboard navigation, not a programmatic `.focus()` call, matching how a
  // real keyboard user reaches the link. Fluent's `Link` draws its
  // indicator as an underline (`text-decoration`, doubled and recoloured),
  // not an `outline` — Fluent explicitly zeroes `outline-style` for Link and
  // uses this attribute-gated decoration instead, a real and differently
  // shaped but equally visible, non-colour-only indicator (Fluent's other
  // components, e.g. `Button`, draw an outline/box-shadow ring instead; this
  // test targets the service-menu Link the way the previous hand-built
  // version did).
  test("carries a visible, Fluent-driven focus indicator that clears 3:1 in both themes", async ({ page }) => {
    for (const theme of ["light", "dark"] as const) {
      await page.goto("/ui/");
      if (theme === "dark") {
        await page.evaluate(() => document.documentElement.classList.add("dark"));
      }
      const link = page.getByRole("navigation", { name: "Service" }).getByRole("link", { name: "Subscriptions" });
      let guard = 0;
      while (guard++ < 30) {
        const active = await page.evaluate(() => document.activeElement?.textContent?.trim());
        if (active === "Subscriptions") break;
        await page.keyboard.press("Tab");
      }
      expect(await link.getAttribute("data-fui-focus-visible")).not.toBeNull();
      const info = await link.evaluate((el) => {
        const style = getComputedStyle(el);
        return {
          textDecorationLine: style.textDecorationLine,
          decorationColor: style.textDecorationColor,
          textColor: style.color,
        };
      });
      expect(info.textDecorationLine).toContain("underline");
      const ratio = await page.evaluate(
        ({ decoration, background }) => {
          const parse = (c: string) => (c.match(/[\d.]+/g) ?? []).map(Number);
          const lin = (v: number) => {
            const n = v / 255;
            return n <= 0.04045 ? n / 12.92 : Math.pow((n + 0.055) / 1.055, 2.4);
          };
          const lum = (c: number[]) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
          const a = lum(parse(decoration));
          const b = lum(parse(background));
          return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
        },
        {
          decoration: info.decorationColor,
          background: await page
            .locator(".az-service-menu")
            .evaluate((node) => getComputedStyle(node).backgroundColor),
        },
      );
      // WCAG 1.4.11 non-text contrast: 3:1 for a focus indicator.
      expect(ratio, `${theme}: focus decoration measured ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(3);
    }
  });
});

test.describe("Automated accessibility audit", () => {
  // axe-core is a coarser net than the hand-measured contrast and landmark
  // assertions above — it catches the classes of defect those checks don't
  // aim at (missing form labels, invalid ARIA usage, duplicate IDs, list
  // structure). It runs against both themes and both the ordinary and the
  // not-supported page shapes, disabling only the colour-contrast rule
  // (already covered, more precisely, by the dedicated Contrast suite, which
  // walks up to the actually-painted background rather than axe's own
  // heuristic).
  //
  // Every Fluent component that manages focus (Popover, Toolbar, Table, …)
  // is built on tabster, which plants invisible "dummy" sentinel elements
  // (`<i data-tabster-dummy aria-hidden tabindex="0">`) at the edges of its
  // own focus-trap zones to detect Tab moving past the first/last real
  // focusable control. They are Fluent's own internal focus-management
  // implementation detail — present on every page this portal renders, not
  // authored content this portal's own markup controls — and axe's
  // `aria-hidden-focus` rule flags them because a *tabbable but
  // screen-reader-hidden* element is normally a real mistake. Real keyboard
  // and screen-reader users never actually land on one (tabster redirects
  // focus around them programmatically before it would ever land there), so
  // this excludes only those specific sentinel nodes from the audit rather
  // than disabling the rule outright.
  const TABSTER_DUMMY = "[data-tabster-dummy]";
  for (const theme of ["light", "dark"] as const) {
    for (const target of [
      "/ui/container-apps",
      "/ui/not-supported/azure-kubernetes-service",
      ...DETAIL_BLADES.map((blade) => blade.path),
      // Every service blade this pass added, listing pane and resource pane,
      // audited in both themes rather than sampled — the panes are generated
      // from one implementation, but each renders its own columns, Essentials,
      // sub-resource tables and command set.
      ...BLADE_SERVICES.map((service) => service.path),
      ...BLADE_SERVICES.map((service) => `${service.path}/structural-test-resource`),
    ]) {
      test(`${target} has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(target);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        const results = await new AxeBuilder({ page })
          .disableRules(["color-contrast"])
          .exclude(TABSTER_DUMMY)
          .analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }

    test(`an open header disclosure panel has no detectable violations (${theme})`, async ({ page }) => {
      await page.goto("/ui/");
      if (theme === "dark") {
        await page.evaluate(() => document.documentElement.classList.add("dark"));
      }
      await page.getByTestId("az-cloud-shell").click();
      const results = await new AxeBuilder({ page })
        .disableRules(["color-contrast"])
        .exclude(TABSTER_DUMMY)
        .analyze();
      expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
    });

    // The Create forms (Storage accounts, Container registries, Container App
    // jobs, Function Apps) are reachable without an identity provider — they
    // render before any cloud read — so each is axe-audited open, in both
    // themes, the same way rather than trusted because they reuse existing
    // Fluent Field/Input/Select/Switch/Button tokens.
    for (const { path, formTestId } of [
      { path: "/ui/storage", formTestId: "storage-create-form" },
      { path: "/ui/acr", formTestId: "acr-create-form" },
      { path: "/ui/functions", formTestId: "fn-create-form" },
      { path: "/ui/container-apps", formTestId: "ca-job-create-form" },
    ]) {
      test(`the ${formTestId} create form has no detectable violations (${theme})`, async ({ page }) => {
        await page.goto(path);
        if (theme === "dark") {
          await page.evaluate(() => document.documentElement.classList.add("dark"));
        }
        await page.getByRole("toolbar", { name: "Commands" }).getByRole("button", { name: "Create", exact: true }).click();
        await expect(page.getByTestId(formTestId)).toBeVisible();
        const results = await new AxeBuilder({ page })
          .disableRules(["color-contrast"])
          .exclude(TABSTER_DUMMY)
          .analyze();
        expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
      });
    }
  }
});
