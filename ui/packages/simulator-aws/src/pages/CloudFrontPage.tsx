import { AwsButton, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import { fetchCloudFrontDistributions, type CloudFrontDistribution } from "../api.js";

// Amazon CloudFront — Distributions. ListDistributions on the real CloudFront
// REST-XML API (GET /2020-05-31/distribution).

const columns: AwsColumn<CloudFrontDistribution>[] = [
  { id: "id", header: "Distribution ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "domainName", header: "Domain name", cell: (row) => row.domainName, value: (row) => row.domainName },
  {
    id: "aliases",
    header: "Alternate domain names",
    cell: (row) => row.aliases.join(", ") || "–",
    value: (row) => row.aliases.join(", "),
  },
  {
    id: "origins",
    header: "Origins",
    cell: (row) => row.origins.join(", ") || "–",
    value: (row) => row.origins.join(", "),
  },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  {
    id: "enabled",
    header: "Enabled",
    cell: (row) => (row.enabled ? "Yes" : "No"),
    value: (row) => (row.enabled ? "Yes" : "No"),
  },
  {
    id: "lastModifiedTime",
    header: "Last modified",
    cell: (row) => formatTimestamp(row.lastModifiedTime),
    value: (row) => row.lastModifiedTime,
  },
];

export function CloudFrontPage() {
  return (
    <AwsResourceTable<CloudFrontDistribution>
      title="Distributions"
      description="CloudFront distributions in this account."
      columns={columns}
      queryKey={["cloudfront-distributions"]}
      queryFn={fetchCloudFrontDistributions}
      filterPlaceholder="Find distributions"
      emptyTitle="No distributions"
      emptyDescription="No CloudFront distributions exist in this account."
      rowKey={(row) => row.id}
      tableTestId="cloudfront-table"
      errorTestId="cloudfront-error"
      actions={({ refetch, isFetching }) => (
        <AwsButton onClick={refetch} disabled={isFetching}>
          {isFetching ? "Refreshing…" : "Refresh"}
        </AwsButton>
      )}
    />
  );
}
