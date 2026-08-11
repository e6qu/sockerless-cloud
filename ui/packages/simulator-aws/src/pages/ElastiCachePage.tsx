import { AwsButton, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { fetchElastiCacheClusters, type ElastiCacheCluster } from "../api.js";

// Amazon ElastiCache — Caches. DescribeCacheClusters on the real ElastiCache
// Query API (Version 2015-02-02).

const columns: AwsColumn<ElastiCacheCluster>[] = [
  { id: "id", header: "Cache name", cell: (row) => row.cacheClusterId, value: (row) => row.cacheClusterId },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "engine", header: "Engine", cell: (row) => row.engine, value: (row) => row.engine },
  { id: "engineVersion", header: "Engine version", cell: (row) => row.engineVersion, value: (row) => row.engineVersion },
  { id: "nodeType", header: "Node type", cell: (row) => row.cacheNodeType, value: (row) => row.cacheNodeType },
  {
    id: "numCacheNodes",
    header: "Nodes",
    cell: (row) => String(row.numCacheNodes),
    value: (row) => String(row.numCacheNodes),
  },
  {
    id: "zone",
    header: "Availability Zone",
    cell: (row) => row.preferredAvailabilityZone || "–",
    value: (row) => row.preferredAvailabilityZone,
  },
];

export function ElastiCachePage() {
  return (
    <AwsResourceTable<ElastiCacheCluster>
      title="Caches"
      description="ElastiCache clusters in this account and Region."
      columns={columns}
      queryKey={["elasticache-clusters"]}
      queryFn={fetchElastiCacheClusters}
      filterPlaceholder="Find caches"
      emptyTitle="No caches"
      emptyDescription="No ElastiCache clusters exist in this account and Region."
      rowKey={(row) => row.cacheClusterId}
      tableTestId="elasticache-table"
      errorTestId="elasticache-error"
      actions={({ refetch, isFetching }) => (
        <AwsButton onClick={refetch} disabled={isFetching}>
          {isFetching ? "Refreshing…" : "Refresh"}
        </AwsButton>
      )}
    />
  );
}
