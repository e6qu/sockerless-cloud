import { Link } from "react-router";
import { AzureResourceTable, type AzureColumn } from "../portal/index.js";
import { resourceGroupOf, locationLabel } from "../portal/format.js";
import { deleteArmResource, fetchArmResources, fetchResourceGroups, type ArmGenericResource } from "../api.js";
import type { ServiceBlade } from "../services.js";

// The listing blade every service in the catalog opens on: the provider's real
// subscription-wide Azure Resource Manager List operation, rendered through
// the same command bar, filter, sorting, paging, multi-select, and
// confirm-then-DELETE the hand-written blades use. Which operation it reads,
// which columns it shows, and what the delete destroys all come from the
// service's own descriptor (services.ts), so a blade is the real ARM
// operations it names rather than a page copied per resource type.

// listResources reads the blade's real List operation. Resource groups are the
// one entry ARM does not serve under /providers — they live at
// /subscriptions/{id}/resourceGroups — so the descriptor leaves `provider`
// empty and this reads that path instead.
function listResources(blade: ServiceBlade): Promise<ArmGenericResource[]> {
  const all = blade.provider ? fetchArmResources(blade.provider, blade.apiVersion) : fetchResourceGroups();
  return blade.filter ? all.then((resources) => resources.filter(blade.filter!)) : all;
}

function columnsFor(blade: ServiceBlade): AzureColumn<ArmGenericResource>[] {
  return blade.columns.map((column, index) => ({
    id: column.id,
    header: column.header,
    value: (row) => column.read(row),
    cell:
      index === 0
        ? (row) => <Link to={`/ui/${blade.slug}/${encodeURIComponent(row.name)}`}>{row.name}</Link>
        : column.id === "location"
          ? (row) => locationLabel(column.read(row))
          : (row) => column.read(row) || "—",
  }));
}

export function ServiceListPage({ blade }: { blade: ServiceBlade }) {
  return (
    <AzureResourceTable<ArmGenericResource>
      key={blade.slug}
      columns={columnsFor(blade)}
      queryKey={[blade.slug]}
      queryFn={() => listResources(blade)}
      filterPlaceholder="Filter by name"
      resourceNoun={blade.noun}
      emptyTitle={`No ${blade.noun} to display`}
      emptyDescription={`The ${blade.noun} in this directory's subscriptions appear here.`}
      rowKey={(row) => row.id}
      rowLabel={(row) => row.name}
      essentials={(rows) => [
        { label: "Resource type", value: blade.kind },
        { label: blade.kind, value: String(rows.length) },
        {
          label: "Resource groups",
          value: String(new Set(rows.map((row) => resourceGroupOf(row.id))).size || 0),
        },
        { label: "Locations", value: String(new Set(rows.map((row) => row.location).filter(Boolean)).size || 0) },
      ]}
      onDelete={async (rows) => {
        for (const row of rows) {
          await deleteArmResource(row.id, blade.apiVersion);
        }
      }}
      deleteWarning={blade.deleteWarning}
      deleteTestId={`${blade.testid}-delete`}
    />
  );
}
