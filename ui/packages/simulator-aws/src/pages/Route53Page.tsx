import { useNavigate } from "react-router";
import { AwsButton, AwsResourceTable, AwsRowLink, type AwsColumn } from "../console/index.js";
import { fetchRoute53HostedZones, type Route53HostedZone } from "../api.js";

// Amazon Route 53 — Hosted zones. ListHostedZones on the real Route 53 REST-XML
// API (GET /2013-04-01/hostedzone).

const columns: AwsColumn<Route53HostedZone>[] = [
  {
    id: "name",
    header: "Domain name",
    cell: (row) => <AwsRowLink to={`/ui/route53/${encodeURIComponent(row.id)}`}>{row.name}</AwsRowLink>,
    value: (row) => row.name,
  },
  {
    id: "type",
    header: "Type",
    cell: (row) => (row.privateZone ? "Private" : "Public"),
    value: (row) => (row.privateZone ? "Private" : "Public"),
  },
  {
    id: "recordCount",
    header: "Record count",
    cell: (row) => String(row.resourceRecordSetCount),
    value: (row) => String(row.resourceRecordSetCount),
  },
  { id: "comment", header: "Description", cell: (row) => row.comment || "–", value: (row) => row.comment },
  { id: "id", header: "Hosted zone ID", cell: (row) => row.id, value: (row) => row.id },
];

export function Route53Page() {
  const navigate = useNavigate();
  return (
    <AwsResourceTable<Route53HostedZone>
      title="Hosted zones"
      description="Route 53 hosted zones in this account."
      columns={columns}
      queryKey={["route53-hosted-zones"]}
      queryFn={fetchRoute53HostedZones}
      filterPlaceholder="Find hosted zones"
      emptyTitle="No hosted zones"
      emptyDescription="No Route 53 hosted zones exist in this account."
      rowKey={(row) => row.id}
      tableTestId="route53-table"
      errorTestId="route53-error"
      actions={({ selected, refetch, isFetching }) => (
        <>
          <AwsButton
            data-testid="route53-view-hosted-zone"
            disabled={selected.length !== 1}
            onClick={() => navigate(`/ui/route53/${encodeURIComponent(selected[0].id)}`)}
          >
            View details
          </AwsButton>
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        </>
      )}
    />
  );
}
