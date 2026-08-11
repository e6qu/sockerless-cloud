import { AwsButton, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import { fetchKinesisStreams, type KinesisStream } from "../api.js";

// Amazon Kinesis Data Streams — Data streams. ListStreams for the names and
// DescribeStreamSummary for each stream's properties, the two real Kinesis
// operations the console's Data streams page reads.

const columns: AwsColumn<KinesisStream>[] = [
  { id: "name", header: "Stream name", cell: (row) => row.streamName, value: (row) => row.streamName },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "streamMode", header: "Capacity mode", cell: (row) => row.streamMode || "–", value: (row) => row.streamMode },
  {
    id: "openShardCount",
    header: "Open shards",
    cell: (row) => String(row.openShardCount),
    value: (row) => String(row.openShardCount),
  },
  {
    id: "retentionPeriodHours",
    header: "Data retention",
    cell: (row) => `${row.retentionPeriodHours} hours`,
    value: (row) => String(row.retentionPeriodHours),
  },
  {
    id: "creationTimestamp",
    header: "Created",
    cell: (row) => formatEpoch(row.creationTimestamp),
    value: (row) => String(row.creationTimestamp),
  },
];

export function KinesisPage() {
  return (
    <AwsResourceTable<KinesisStream>
      title="Data streams"
      description="Kinesis data streams in this account and Region."
      columns={columns}
      queryKey={["kinesis-streams"]}
      queryFn={fetchKinesisStreams}
      filterPlaceholder="Find data streams"
      emptyTitle="No data streams"
      emptyDescription="No Kinesis data streams exist in this account and Region."
      rowKey={(row) => row.streamName}
      tableTestId="kinesis-table"
      errorTestId="kinesis-error"
      actions={({ refetch, isFetching }) => (
        <AwsButton onClick={refetch} disabled={isFetching}>
          {isFetching ? "Refreshing…" : "Refresh"}
        </AwsButton>
      )}
    />
  );
}
