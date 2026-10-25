import { useEffect } from "react";
import { Link as RouterLink, useNavigate, useParams } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Table, TableBody, TableCell, TableHeader, TableHeaderCell, TableRow, Text } from "@fluentui/react-components";
import type { ArmGenericResource } from "../api.js";
import { listProviderPage, listResourceGroupsPage, listResourcesPage, listSubscriptions, parseResourceId, type Subscription } from "../azure.js";
import { serviceById } from "../catalog.js";
import { SERVICE_BLADES, bladeForResource, bladeType, type ServiceBlade } from "../services.js";
import { Blade, CommandBar, EmptyState, ErrorBar, errorText, StatusBadge } from "../portal/AzurePortal.js";
import { BrowseList, type BrowseColumn } from "../portal/BrowseList.js";
import { recordServiceUse } from "../portal/preferences.js";
import { resourceRoute } from "../portal/routes.js";
import { useDirectoryName } from "../portal/directory.js";
import { CREATE_SPECS } from "./create/specs.js";

const NAME_COLUMN: BrowseColumn = { id: "name", header: "Name", value: (resource) => resource.name };
const TYPE_COLUMN: BrowseColumn = {
  id: "type",
  header: "Type",
  value: (resource) => bladeForResource(resource.type, resource.kind)?.kind ?? resource.type,
};
const GROUP_COLUMN: BrowseColumn = { id: "resourceGroup", header: "Resource group", value: (resource) => parseResourceId(resource.id).resourceGroup ?? "" };
const LOCATION_COLUMN: BrowseColumn = { id: "location", header: "Location", value: (resource) => resource.location };
const SUBSCRIPTION_COLUMN: BrowseColumn = {
  id: "subscription",
  header: "Subscription",
  value: (resource, subscriptions: Subscription[]) => {
    const id = parseResourceId(resource.id).subscriptionId;
    return subscriptions.find((sub) => sub.subscriptionId === id)?.displayName ?? id;
  },
};
const STATE_COLUMNS = new Set(["provisioningState", "state", "status"]);

function bladeColumns(blade: ServiceBlade): BrowseColumn[] {
  return blade.columns
    .filter((column) => column.id !== "name" && column.id !== "location")
    .map((column) => ({
      id: column.id,
      header: column.header,
      value: (resource: ArmGenericResource) => column.read(resource),
      cell: STATE_COLUMNS.has(column.id) ? (resource: ArmGenericResource) => (column.read(resource) ? <StatusBadge status={column.read(resource)} /> : "—") : undefined,
    }));
}

function useServiceUse(id: string) {
  useEffect(() => recordServiceUse(id), [id]);
}

function ServiceBrowse({ blade }: { blade: ServiceBlade }) {
  const directory = useDirectoryName();
  const navigate = useNavigate();
  const service = serviceById(blade.slug);
  useServiceUse(blade.slug);
  const extra = bladeColumns(blade);
  const columns = [NAME_COLUMN, ...extra.filter((column) => column.id !== "type"), TYPE_COLUMN, GROUP_COLUMN, LOCATION_COLUMN, SUBSCRIPTION_COLUMN];
  const spec = CREATE_SPECS.find((entry) => entry.type.toLowerCase() === bladeType(blade).toLowerCase() && (entry.kind ?? "") === (blade.browseKind ?? ""));
  return (
    <Blade
      title={blade.label}
      subtitle={directory}
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: blade.label }]}
      favoriteServiceId={blade.slug}
      testid={`browse-${blade.testid}`}
    >
      <BrowseList
        source={{
          key: blade.slug,
          fetchPage: blade.provider
            ? (subscriptionId, _top, next) => listProviderPage(subscriptionId, blade.provider, blade.apiVersion, next)
            : (subscriptionId, top, next) => listResourceGroupsPage(subscriptionId, top, next),
        }}
        columns={columns}
        defaultColumns={blade.provider ? ["type", "resourceGroup", "location", "subscription", ...extra.slice(0, 1).map((column) => column.id)] : ["subscription", "location"]}
        noun={blade.noun}
        rowFilter={blade.filter}
        onCreate={spec ? () => navigate(spec.route) : undefined}
        emptyTitle={`No ${blade.noun} to display`}
        emptyDescription={spec ? `Create a ${blade.detailKind.toLowerCase()} to get started.` : `${blade.label} created through Azure Resource Manager appear here.`}
        testid={blade.testid}
      />
    </Blade>
  );
}

/** `browse/{Namespace}/{type}` and `browse/{Namespace}/{type}/kind/{kind}`. */
export function ServiceBrowsePage() {
  const { namespace = "", type = "", kind = "" } = useParams();
  const fullType = `${namespace}/${type}`.toLowerCase();
  const blade = SERVICE_BLADES.find((entry) => entry.provider.toLowerCase() === fullType && (entry.browseKind ?? "") === kind);
  if (!blade) {
    return (
      <Blade title="Not found" trail={[{ label: "Home", to: "/ui/home" }]}>
        <EmptyState title="The console has no page for this resource type." description={`${namespace}/${type}`} />
      </Blade>
    );
  }
  return <ServiceBrowse key={blade.slug} blade={blade} />;
}

export function ResourceGroupsPage() {
  const blade = SERVICE_BLADES.find((entry) => !entry.provider)!;
  return <ServiceBrowse blade={blade} />;
}

export function AllResourcesPage() {
  const directory = useDirectoryName();
  const navigate = useNavigate();
  const service = serviceById("all-resources");
  useServiceUse("all-resources");
  return (
    <Blade
      title="All resources"
      subtitle={directory}
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: "All resources" }]}
      favoriteServiceId="all-resources"
      testid="browse-all"
    >
      <BrowseList
        source={{ key: "all-resources", fetchPage: (subscriptionId, top, next) => listResourcesPage(subscriptionId, { top, next }) }}
        columns={[NAME_COLUMN, TYPE_COLUMN, GROUP_COLUMN, LOCATION_COLUMN, SUBSCRIPTION_COLUMN]}
        defaultColumns={["type", "resourceGroup", "location", "subscription"]}
        noun="resources"
        typeFilter
        typeLabel={(resource) => bladeForResource(resource.type, resource.kind)?.kind ?? resource.type}
        onCreate={() => navigate("/ui/create")}
        emptyTitle="No resources to display"
        emptyDescription="Try changing or clearing your filters, or create a resource."
        testid="all"
      />
    </Blade>
  );
}

export function SubscriptionsPage() {
  const directory = useDirectoryName();
  const queryClient = useQueryClient();
  const service = serviceById("subscriptions");
  useServiceUse("subscriptions");
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  return (
    <Blade
      title="Subscriptions"
      subtitle={directory}
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: "Subscriptions" }]}
      favoriteServiceId="subscriptions"
      testid="browse-subscriptions"
    >
      <CommandBar commands={[{ label: "Refresh", icon: "refresh", onSelect: () => void queryClient.invalidateQueries({ queryKey: ["subscriptions"] }) }]} />
      {subscriptions.isError ? <ErrorBar title="The subscriptions could not be listed.">{errorText(subscriptions.error)}</ErrorBar> : null}
      <Text size={200} block className="az-records" role="status">
        {subscriptions.data ? `Showing ${subscriptions.data.length} subscriptions.` : "Loading…"}
      </Text>
      <Table aria-label="Subscriptions" size="small" className="az-grid" data-testid="subscriptions-table">
        <TableHeader>
          <TableRow>
            <TableHeaderCell>Subscription name</TableHeaderCell>
            <TableHeaderCell>Subscription ID</TableHeaderCell>
            <TableHeaderCell>My role</TableHeaderCell>
            <TableHeaderCell>Status</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(subscriptions.data ?? []).map((sub) => (
            <TableRow key={sub.subscriptionId}>
              <TableCell>
                <RouterLink to={resourceRoute(sub.id)}>{sub.displayName}</RouterLink>
              </TableCell>
              <TableCell>{sub.subscriptionId}</TableCell>
              <TableCell>
                <RouterLink to={resourceRoute(sub.id, "users")}>View access</RouterLink>
              </TableCell>
              <TableCell>
                <StatusBadge status={sub.state === "Enabled" ? "Active" : sub.state} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Blade>
  );
}
