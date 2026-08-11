import { useQuery } from "@tanstack/react-query";
import { useParams } from "react-router";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsPageHeader,
  AwsResourceTable,
  type AwsColumn,
} from "../console/index.js";
import { fetchRoute53HostedZones, fetchRoute53RecordSets, type Route53RecordSet } from "../api.js";

// Amazon Route 53 — Hosted zone detail. ListHostedZones for the summary (there
// is no GetHostedZone read this page needs beyond what the list already
// carries) and ListResourceRecordSets for the records table.

const recordColumns: AwsColumn<Route53RecordSet>[] = [
  { id: "name", header: "Record name", cell: (row) => row.name, value: (row) => row.name },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "ttl", header: "TTL (seconds)", cell: (row) => (row.ttl ? String(row.ttl) : "–"), value: (row) => String(row.ttl) },
  {
    id: "values",
    header: "Value",
    cell: (row) => row.values.join(", ") || "–",
    value: (row) => row.values.join(", "),
  },
];

export function Route53HostedZoneDetailPage() {
  const { hostedZoneId = "" } = useParams();
  const zones = useQuery({ queryKey: ["route53-hosted-zones"], queryFn: fetchRoute53HostedZones });
  const zone = zones.data?.find((candidate) => candidate.id === hostedZoneId);

  return (
    <>
      <AwsPageHeader title={hostedZoneId} description="Route 53 hosted zone in this account." />
      <AwsContainer>
        {zones.isError ? (
          <AwsErrorAlert testId="route53-zone-error">
            <strong>Could not load the hosted zone.</strong>{" "}
            {zones.error instanceof Error ? zones.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : zones.isLoading ? (
          <AwsEmptyState title="Loading hosted zone…" loading />
        ) : zone ? (
          <div data-testid="route53-zone-summary">
            <AwsKeyValue
              ariaLabel="Hosted zone details"
              items={[
                { label: "Domain name", value: zone.name },
                { label: "Type", value: zone.privateZone ? "Private hosted zone" : "Public hosted zone" },
                { label: "Record count", value: String(zone.resourceRecordSetCount) },
                { label: "Description", value: zone.comment || "–" },
              ]}
            />
          </div>
        ) : (
          <AwsEmptyState
            title="Hosted zone not found"
            description={`No hosted zone with ID ${hostedZoneId} exists in this account.`}
          />
        )}
      </AwsContainer>
      <AwsResourceTable<Route53RecordSet>
        title="Records"
        headingVariant="h2"
        description="Resource record sets in this hosted zone."
        columns={recordColumns}
        queryKey={["route53-records", hostedZoneId]}
        queryFn={() => fetchRoute53RecordSets(hostedZoneId)}
        filterPlaceholder="Find records"
        emptyTitle="No records"
        emptyDescription="This hosted zone has no resource record sets."
        rowKey={(row) => `${row.name}|${row.type}`}
        tableTestId="route53-records-table"
        errorTestId="route53-records-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
    </>
  );
}
