import { Link, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  fetchComputeFirewalls,
  fetchComputeNetwork,
  fetchComputeNetworks,
  fetchComputeSubnetworks,
  type ComputeFirewall,
  type ComputeNetwork,
  type ComputeSubnetwork,
} from "../api.js";
import { useProject } from "../console/project.js";

const lastSegment = (value: string | undefined): string => (value ? shortName(value) : "—");

// A firewall rule's allow/deny list is a list of protocol/ports pairs; the
// real console renders each as "tcp:80,443", the same notation gcloud takes.
export function formatFirewallActions(actions: ComputeFirewall["allowed"]): string {
  if (!actions || actions.length === 0) return "—";
  return actions
    .map((action) => (action.ports?.length ? `${action.IPProtocol}:${action.ports.join(",")}` : action.IPProtocol ?? ""))
    .join(" ");
}

const columns: GcpColumn<ComputeNetwork>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/vpc/${row.name}`}>
        {row.name}
      </Link>
    ),
    value: (row) => row.name,
  },
  {
    id: "subnetMode",
    header: "Subnet creation mode",
    cell: (row) => (row.autoCreateSubnetworks ? "Automatic" : "Custom"),
    value: (row) => (row.autoCreateSubnetworks ? "Automatic" : "Custom"),
  },
  {
    id: "routingMode",
    header: "Dynamic routing mode",
    cell: (row) => row.routingConfig?.routingMode ?? "—",
    value: (row) => row.routingConfig?.routingMode ?? "",
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.creationTimestamp ?? ""),
    value: (row) => row.creationTimestamp ?? "",
  },
];

export function VpcNetworkPage() {
  const { project } = useProject();
  return (
    <GcpResourceTable<ComputeNetwork>
      title="VPC networks"
      description="A Virtual Private Cloud network is a virtual version of a physical network, implemented inside Google's production network."
      columns={columns}
      queryKey={["compute-networks", project]}
      queryFn={() => fetchComputeNetworks(project)}
      filterPlaceholder="Filter VPC networks"
      resourceNoun="VPC networks"
      empty={{
        headline: "Create a VPC network to get started",
        description: "VPC networks connect your Compute Engine instances, Cloud Run services and other resources.",
        primaryLabel: "Create VPC network",
      }}
      rowKey={(row) => row.name}
    />
  );
}

export function VpcNetworkDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const network = useQuery({
    queryKey: ["compute-network", project, name],
    queryFn: () => fetchComputeNetwork(project, name),
  });
  // compute.subnetworks.list is region-scoped; the console reads the region it
  // is configured for, the same coordinate every other regional read uses.
  const subnets = useQuery({
    queryKey: ["compute-subnetworks", project, CONSOLE_REGION],
    queryFn: () => fetchComputeSubnetworks(project, CONSOLE_REGION),
    select: (all: ComputeSubnetwork[]) => all.filter((subnet) => shortName(subnet.network ?? "") === name),
  });
  const firewalls = useQuery({
    queryKey: ["compute-firewalls", project],
    queryFn: () => fetchComputeFirewalls(project),
    select: (all: ComputeFirewall[]) => all.filter((rule) => shortName(rule.network ?? "") === name),
  });

  const data = network.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/vpc">‹ VPC networks</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="VPC network"
        onRefresh={() => {
          void network.refetch();
          void subnets.refetch();
          void firewalls.refetch();
        }}
        refreshing={network.isFetching || subnets.isFetching || firewalls.isFetching}
      />
      {network.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this network.</strong>{" "}
          {network.error instanceof Error ? network.error.message : "The simulator did not respond."}
        </div>
      ) : network.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading network…</div>
      ) : (
        <GcpTabs
          label="VPC network detail"
          tabs={[
            {
              id: "subnets",
              label: "Subnets",
              content: (
                <SubResourceTable<ComputeSubnetwork>
                  query={subnets}
                  testId="vpc-subnets-table"
                  noun="subnets"
                  emptyHeadline={`This network has no subnets in ${CONSOLE_REGION}`}
                  emptyDescription="Subnets created in this region appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Name", cell: (row) => row.name },
                    { header: "Region", cell: (row) => lastSegment(row.region) },
                    { header: "IP address range", cell: (row) => row.ipCidrRange ?? "—" },
                    { header: "Gateway", cell: (row) => row.gatewayAddress ?? "—" },
                    {
                      header: "Private Google Access",
                      cell: (row) => (row.privateIpGoogleAccess ? "On" : "Off"),
                    },
                  ]}
                />
              ),
            },
            {
              id: "firewalls",
              label: "Firewall rules",
              content: (
                <SubResourceTable<ComputeFirewall>
                  query={firewalls}
                  testId="vpc-firewalls-table"
                  noun="firewall rules"
                  emptyHeadline="This network has no firewall rules"
                  emptyDescription="Rules that apply to this network appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Name", cell: (row) => row.name },
                    { header: "Direction", cell: (row) => row.direction ?? "—" },
                    { header: "Priority", cell: (row) => row.priority ?? "—" },
                    {
                      header: "Enforcement",
                      cell: (row) => <GcpStatus status={row.disabled ? "Disabled" : "Enabled"} />,
                    },
                    { header: "Source ranges", cell: (row) => (row.sourceRanges ?? []).join(", ") || "—" },
                    {
                      header: "Protocols and ports",
                      cell: (row) =>
                        row.denied?.length
                          ? `deny ${formatFirewallActions(row.denied)}`
                          : formatFirewallActions(row.allowed),
                    },
                  ]}
                />
              ),
            },
            {
              id: "details",
              label: "Details",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Network ID", value: data.id ?? "—" },
                    { label: "Subnet creation mode", value: data.autoCreateSubnetworks ? "Automatic" : "Custom" },
                    { label: "Dynamic routing mode", value: data.routingConfig?.routingMode ?? "—" },
                    { label: "Created", value: formatTimestamp(data.creationTimestamp ?? "") },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
          ]}
        />
      )}
    </>
  );
}
