import { AwsButton, AwsResourceTable, AwsRefreshButton, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  fetchCloudMapNamespaces,
  fetchCloudMapServices,
  type CloudMapNamespace,
  type CloudMapService,
} from "../api.js";

// AWS Cloud Map — Namespaces and Services. ListNamespaces and ListServices on
// the real Cloud Map API (X-Amz-Target Route53AutoNaming_v20170314.<Op>).

const namespaceColumns: AwsColumn<CloudMapNamespace>[] = [
  { id: "name", header: "Namespace name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "Namespace ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  {
    id: "serviceCount",
    header: "Service count",
    cell: (row) => String(row.serviceCount),
    value: (row) => String(row.serviceCount),
  },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  {
    id: "createDate",
    header: "Created",
    cell: (row) => formatEpoch(row.createDate),
    value: (row) => String(row.createDate),
  },
];

const serviceColumns: AwsColumn<CloudMapService>[] = [
  { id: "name", header: "Service name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "Service ID", cell: (row) => row.id, value: (row) => row.id },
  {
    id: "instanceCount",
    header: "Instance count",
    cell: (row) => String(row.instanceCount),
    value: (row) => String(row.instanceCount),
  },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  {
    id: "createDate",
    header: "Created",
    cell: (row) => formatEpoch(row.createDate),
    value: (row) => String(row.createDate),
  },
];

export function CloudMapPage({ view = "namespaces" }: { view?: "namespaces" | "services" }) {
  return (
    <>
      {view === "namespaces" && (
      <AwsResourceTable<CloudMapNamespace>
        title="Namespaces"
        description="AWS Cloud Map namespaces in this account and Region."
        columns={namespaceColumns}
        queryKey={["cloudmap-namespaces"]}
        queryFn={fetchCloudMapNamespaces}
        filterPlaceholder="Find namespaces"
        emptyTitle="No namespaces"
        emptyDescription="No AWS Cloud Map namespaces exist in this account and Region."
        rowKey={(row) => row.id}
        tableTestId="cloudmap-namespaces-table"
        errorTestId="cloudmap-namespaces-error"
        actions={({ refetch, isFetching }) => (
          <AwsRefreshButton onClick={refetch} loading={isFetching} />
        )}
      />
      )}
      {view === "services" && (
      <AwsResourceTable<CloudMapService>
        title="Services"
        description="The services registered across every namespace."
        columns={serviceColumns}
        queryKey={["cloudmap-services"]}
        queryFn={fetchCloudMapServices}
        filterPlaceholder="Find services"
        emptyTitle="No services"
        emptyDescription="No AWS Cloud Map services exist in this account and Region."
        rowKey={(row) => row.id}
        tableTestId="cloudmap-services-table"
        errorTestId="cloudmap-services-error"
        actions={({ refetch, isFetching }) => (
          <AwsRefreshButton onClick={refetch} loading={isFetching} />
        )}
      />
      )}
    </>
  );
}
