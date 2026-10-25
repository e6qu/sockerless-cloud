import { useState, type ReactNode } from "react";
import { Link as RouterLink, useNavigate, useParams } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Checkbox,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Field,
  Input,
  OverlayDrawer,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
} from "@fluentui/react-components";
import { addClientSecret, createAppRegistration, deleteAppRegistration, fetchAppRegistration, fetchAppRegistrations, removeClientSecret, type MintedClientSecret } from "../api.js";
import { armGet } from "../portal/arm.js";
import { listSubscriptions, listTenants } from "../azure.js";
import { serviceById } from "../catalog.js";
import { Blade, CommandBar, ConfirmDialog, EmptyState, ErrorBar, Essentials, errorText, ServiceMenu, type Crumb } from "../portal/AzurePortal.js";
import { useConsoleConfig } from "../portal/AzureApp.js";
import { AzureIcon } from "../portal/icons.js";
import { useNotifications } from "../portal/notifications.js";
import { CertificatesAndSecrets } from "./CertificatesAndSecrets.js";

const MENU = [
  { label: "Overview", to: "/ui/entra", icon: "info" as const },
  { label: "Users", to: "/ui/entra/users", icon: "iam" as const },
  { label: "Groups", to: "/ui/entra/groups", icon: "iam" as const },
  { label: "App registrations", to: "/ui/entra/app-registrations", icon: "apps" as const },
];

function EntraBlade({ title, trail, children, testid }: { title: string; trail?: Crumb[]; children: ReactNode; testid?: string }) {
  const service = serviceById("entra");
  const tenants = useQuery({ queryKey: ["tenants"], queryFn: listTenants, staleTime: Infinity });
  const directory = tenants.data?.[0]?.displayName ?? "";
  return (
    <Blade
      title={title === "Overview" ? directory || "Microsoft Entra ID" : `${directory} | ${title}`}
      subtitle="Microsoft Entra ID"
      icon={{ name: service.icon, color: service.color }}
      trail={trail ?? [{ label: "Home", to: "/ui/home" }, { label: directory || "Microsoft Entra ID" }]}
      favoriteServiceId="entra"
      testid={testid}
    >
      <div className="az-resource-layout">
        <ServiceMenu label="Microsoft Entra ID menu" top={MENU} groups={[]} />
        <div className="az-resource-content">{children}</div>
      </div>
    </Blade>
  );
}

interface GraphList {
  value?: { id?: string; displayName?: string; userPrincipalName?: string; mail?: string; appId?: string; securityEnabled?: boolean; groupTypes?: string[] }[];
}

export function EntraOverviewPage() {
  const tenants = useQuery({ queryKey: ["tenants"], queryFn: listTenants, staleTime: Infinity });
  const users = useQuery({ queryKey: ["graph", "users"], queryFn: () => armGet<GraphList>("/v1.0/users", "graph") });
  const groups = useQuery({ queryKey: ["graph", "groups"], queryFn: () => armGet<GraphList>("/v1.0/groups", "graph") });
  const apps = useQuery({ queryKey: ["entra-apps"], queryFn: fetchAppRegistrations });
  const tenant = tenants.data?.[0];
  return (
    <EntraBlade title="Overview" testid="entra-overview">
      <Essentials
        left={[
          { label: "Name", value: tenant?.displayName ?? "" },
          { label: "Tenant ID", value: <code>{tenant?.tenantId}</code> },
          { label: "Primary domain", value: tenant?.defaultDomain ?? "" },
        ]}
        right={[
          { label: "Users", value: users.data ? String(users.data.value?.length ?? 0) : "" },
          { label: "Groups", value: groups.data ? String(groups.data.value?.length ?? 0) : "" },
          { label: "Applications", value: apps.data ? String(apps.data.length) : "" },
        ]}
      />
    </EntraBlade>
  );
}

function GraphTable({ path, label, columns, testid }: { path: string; label: string; columns: { header: string; value: (row: NonNullable<GraphList["value"]>[number]) => string }[]; testid: string }) {
  const rows = useQuery({ queryKey: ["graph", path], queryFn: () => armGet<GraphList>(path, "graph") });
  return (
    <>
      <CommandBar commands={[{ label: "Refresh", icon: "refresh", onSelect: () => void rows.refetch() }]} />
      {rows.isError ? <ErrorBar title={`The ${label.toLowerCase()} could not be listed.`}>{errorText(rows.error)}</ErrorBar> : null}
      <Table aria-label={label} size="small" className="az-grid" data-testid={testid}>
        <TableHeader>
          <TableRow>
            {columns.map((column) => (
              <TableHeaderCell key={column.header}>{column.header}</TableHeaderCell>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {(rows.data?.value ?? []).map((row) => (
            <TableRow key={row.id}>
              {columns.map((column) => (
                <TableCell key={column.header}>{column.value(row) || "—"}</TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {rows.isLoading ? <EmptyState title={`Loading ${label.toLowerCase()}…`} loading /> : null}
    </>
  );
}

export function EntraUsersPage() {
  return (
    <EntraBlade title="Users" testid="entra-users">
      <GraphTable
        path="/v1.0/users"
        label="Users"
        testid="entra-users-table"
        columns={[
          { header: "Display name", value: (row) => row.displayName ?? "" },
          { header: "User principal name", value: (row) => row.userPrincipalName ?? "" },
          { header: "Mail", value: (row) => row.mail ?? "" },
          { header: "Object ID", value: (row) => row.id ?? "" },
        ]}
      />
    </EntraBlade>
  );
}

export function EntraGroupsPage() {
  return (
    <EntraBlade title="Groups" testid="entra-groups">
      <GraphTable
        path="/v1.0/groups"
        label="Groups"
        testid="entra-groups-table"
        columns={[
          { header: "Name", value: (row) => row.displayName ?? "" },
          { header: "Object ID", value: (row) => row.id ?? "" },
          { header: "Group type", value: (row) => (row.groupTypes?.includes("Unified") ? "Microsoft 365" : "Security") },
        ]}
      />
    </EntraBlade>
  );
}

/** App registrations: the directory's applications, with New registration
 *  (an application and its service principal) and Delete. */
export function AppRegistrationsPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { track } = useNotifications();
  const apps = useQuery({ queryKey: ["entra-apps"], queryFn: fetchAppRegistrations });
  const [registering, setRegistering] = useState(false);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [deleting, setDeleting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const register = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const app = await track({ progress: `Registering ${name}`, success: `Registered ${name}`, failure: `Failed to register ${name}`, description: "App registrations" }, () => createAppRegistration(name.trim()));
      void queryClient.invalidateQueries({ queryKey: ["entra-apps"] });
      setRegistering(false);
      setName("");
      navigate(`/ui/entra/app-registrations/${app.id}`);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track({ progress: `Deleting ${selected.length} applications`, success: `Deleted ${selected.length} applications`, failure: "Failed to delete applications", description: "App registrations" }, async () => {
        for (const id of selected) await deleteAppRegistration(id);
      });
      setSelected([]);
      setDeleting(false);
      void queryClient.invalidateQueries({ queryKey: ["entra-apps"] });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const rows = apps.data ?? [];
  return (
    <EntraBlade title="App registrations" testid="entra-apps">
      <CommandBar
        commands={[
          { label: "New registration", icon: "add", onSelect: () => setRegistering(true), testid: "entra-new-registration" },
          { label: "Refresh", icon: "refresh", onSelect: () => void apps.refetch() },
          { label: "Delete", icon: "delete", disabled: selected.length === 0, onSelect: () => setDeleting(true), testid: "entra-delete" },
        ]}
      />
      {apps.isError ? <ErrorBar title="Could not load app registrations.">{errorText(apps.error)}</ErrorBar> : null}
      {error && !registering && !deleting ? <ErrorBar title="The directory operation failed.">{error}</ErrorBar> : null}
      <Table aria-label="App registrations" size="small" className="az-grid" data-testid="entra-apps-table">
        <TableHeader>
          <TableRow>
            <TableHeaderCell className="az-grid-select"><span className="sr-only">Select</span></TableHeaderCell>
            <TableHeaderCell>Display name</TableHeaderCell>
            <TableHeaderCell>Application (client) ID</TableHeaderCell>
            <TableHeaderCell>Object ID</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.id} data-testid="entra-app-row">
              <TableCell className="az-grid-select">
                <Checkbox aria-label={`Select ${row.displayName}`} checked={selected.includes(row.id)} onChange={(_, data) => setSelected(data.checked ? [...selected, row.id] : selected.filter((id) => id !== row.id))} />
              </TableCell>
              <TableCell>
                <RouterLink to={`/ui/entra/app-registrations/${row.id}`}>{row.displayName}</RouterLink>
              </TableCell>
              <TableCell>
                <code>{row.appId}</code>
              </TableCell>
              <TableCell>
                <code>{row.id}</code>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {apps.isLoading ? <EmptyState title="Loading app registrations…" loading /> : null}
      {apps.data && rows.length === 0 ? <EmptyState title="No app registrations to display" /> : null}
      <OverlayDrawer open={registering} position="end" size="medium" onOpenChange={(_, data) => !data.open && setRegistering(false)} aria-label="Register an application">
        <DrawerHeader>
          <DrawerHeaderTitle action={<Button appearance="subtle" aria-label="Close" icon={<AzureIcon name="dismiss" />} onClick={() => setRegistering(false)} />}>Register an application</DrawerHeaderTitle>
        </DrawerHeader>
        <DrawerBody className="az-drawer-form">
          <Field label="Name" required hint="The user-facing display name for this application (this can be changed later).">
            <Input value={name} onChange={(_, data) => setName(data.value)} data-testid="entra-app-name-input" />
          </Field>
          <Text size={200} block>
            Supported account types: Accounts in this organizational directory only (single tenant).
          </Text>
          {error ? <ErrorBar title="The registration failed.">{error}</ErrorBar> : null}
        </DrawerBody>
        <DrawerFooter>
          <Button appearance="primary" disabled={!name.trim() || busy} onClick={() => void register()} data-testid="entra-register-submit">
            Register
          </Button>
          <Button onClick={() => setRegistering(false)}>Cancel</Button>
        </DrawerFooter>
      </OverlayDrawer>
      <ConfirmDialog open={deleting} title="Delete app registrations" busy={busy} testid="entra-delete-dialog" error={deleting ? error : undefined} onConfirm={() => void remove()} onCancel={() => setDeleting(false)}>
        <Text block>Deleting an application removes its service principal and credentials. Clients using it can no longer sign in.</Text>
      </ConfirmDialog>
    </EntraBlade>
  );
}

function CliUsage({ appId, tenant, subscription, cloud }: { appId: string; tenant: string; subscription: string; cloud: string }) {
  return (
    <section className="az-section" aria-label="Connect with the Azure CLI">
      <Text as="h2" size={400} weight="semibold" block>
        Connect with the Azure CLI
      </Text>
      <Text block size={200}>
        Use the application (client) ID with a client secret from this page. The token request below is the request <code>az login --service-principal</code> sends.
      </Text>
      <pre className="az-json" data-testid="entra-cli-usage">
        {`# Acquire an Azure Resource Manager token with the client secret
TOKEN=$(curl -s -X POST "${cloud}/${tenant}/oauth2/v2.0/token" \\
  -d grant_type=client_credentials \\
  -d client_id=${appId} \\
  -d "client_secret=<client-secret-value>" \\
  -d scope=https://management.azure.com/.default | jq -r .access_token)

# Read Azure Resource Manager as the service principal
az rest --method get \\
  --url "${cloud}/subscriptions/${subscription}/resourcegroups?api-version=2021-04-01" \\
  --headers "Authorization=Bearer $TOKEN"`}
      </pre>
    </section>
  );
}

/** An app registration: Overview and Certificates & secrets. */
export function AppRegistrationPage() {
  const { objectId = "", section = "overview" } = useParams();
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const config = useConsoleConfig();
  const app = useQuery({ queryKey: ["entra-app", objectId], queryFn: () => fetchAppRegistration(objectId) });
  const tenants = useQuery({ queryKey: ["tenants"], queryFn: listTenants, staleTime: Infinity });
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  const [minted, setMinted] = useState<MintedClientSecret | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const service = serviceById("app-registrations");
  const tenant = config.data?.federationTenant || tenants.data?.[0]?.tenantId || "";
  const cloud = config.data?.cloudApiEndpoint || window.location.origin;
  const name = app.data?.displayName ?? objectId;
  const base = `/ui/entra/app-registrations/${objectId}`;
  const mutate = async (operation: () => Promise<void>) => {
    setBusy(true);
    setError(undefined);
    try {
      await operation();
      void queryClient.invalidateQueries({ queryKey: ["entra-app", objectId] });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Blade
      title={section === "credentials" ? `${name} | Certificates & secrets` : name}
      subtitle="App registration"
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: "App registrations", to: "/ui/entra/app-registrations" }, { label: name }]}
      testid="entra-app"
    >
      <div className="az-resource-layout">
        <ServiceMenu
          label={`${name} menu`}
          top={[{ label: "Overview", to: base, icon: "info" }]}
          groups={[{ label: "Manage", items: [{ label: "Certificates & secrets", to: `${base}/credentials`, icon: "key_vault" }] }]}
        />
        <div className="az-resource-content">
          {app.isError ? <ErrorBar title="Could not load the app registration." testid="entra-app-error">{errorText(app.error)}</ErrorBar> : null}
          {error ? <ErrorBar title="The credential operation failed." testid="entra-secret-error">{error}</ErrorBar> : null}
          {!app.data ? (
            app.isLoading ? <EmptyState title="Loading the app registration…" loading /> : null
          ) : section === "credentials" ? (
            <CertificatesAndSecrets
              credentials={app.data.passwordCredentials}
              minted={minted}
              busy={busy}
              onCreate={(description, endDateTime) =>
                void mutate(async () => {
                  setMinted(await track({ progress: "Adding client secret", success: "Added client secret", failure: "Failed to add client secret", description: name }, () => addClientSecret(objectId, description, endDateTime)));
                })
              }
              onRemove={(keyId) =>
                void mutate(async () => {
                  await removeClientSecret(objectId, keyId);
                  setMinted((current) => (current?.keyId === keyId ? null : current));
                })
              }
            />
          ) : (
            <>
              <CommandBar commands={[{ label: "Refresh", icon: "refresh", onSelect: () => void app.refetch() }]} />
              <Essentials
                left={[
                  { label: "Display name", value: <span data-testid="entra-app-name">{app.data.displayName}</span> },
                  { label: "Application (client) ID", value: <code data-testid="entra-app-client-id">{app.data.appId}</code> },
                  { label: "Object ID", value: <code>{app.data.id}</code> },
                ]}
                right={[
                  { label: "Directory (tenant) ID", value: <code data-testid="entra-app-tenant-id">{tenant}</code> },
                  { label: "Supported account types", value: app.data.signInAudience || "My organization only" },
                  { label: "Client credentials", value: <RouterLink to={`${base}/credentials`}>{`${app.data.passwordCredentials.length} secret(s)`}</RouterLink> },
                ]}
              />
              <CliUsage appId={app.data.appId} tenant={tenant} subscription={subscriptions.data?.[0]?.subscriptionId ?? "<subscription-id>"} cloud={cloud} />
            </>
          )}
        </div>
      </div>
    </Blade>
  );
}
