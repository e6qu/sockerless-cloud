import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsPageHeader,
  AwsStatus,
} from "../console/index.js";
import { formatBytes, formatEpoch } from "../console/format.js";
import { fetchDynamoDBTable } from "../api.js";
import { DeleteTablesModal } from "./DynamoDBTablesPage.js";

// Amazon DynamoDB — Table detail. DescribeTable, the real operation the
// console's table overview reads.

export function DynamoDBTableDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const table = useQuery({ queryKey: ["dynamodb-table", name], queryFn: () => fetchDynamoDBTable(name) });

  return (
    <>
      <AwsPageHeader
        title={name}
        description="DynamoDB table in this account and Region."
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton data-testid="dynamodb-table-delete" disabled={!table.isSuccess} onClick={() => setDeleting(true)}>
              Delete
            </AwsButton>
          </SpaceBetween>
        }
      />
      <AwsContainer>
        {table.isError ? (
          <AwsErrorAlert testId="dynamodb-table-error">
            <strong>Could not load the table.</strong>{" "}
            {table.error instanceof Error ? table.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : table.isLoading ? (
          <AwsEmptyState title="Loading table…" loading />
        ) : table.data ? (
          <div data-testid="dynamodb-table-summary">
            <AwsKeyValue
              ariaLabel="Table details"
              items={[
                { label: "Status", value: <AwsStatus status={table.data.tableStatus} /> },
                { label: "Partition key", value: table.data.partitionKey || "–" },
                { label: "Sort key", value: table.data.sortKey || "–" },
                { label: "Capacity mode", value: table.data.billingMode },
                { label: "Item count", value: String(table.data.itemCount) },
                { label: "Table size", value: formatBytes(table.data.tableSizeBytes) },
                { label: "Created", value: formatEpoch(table.data.creationDateTime) },
                { label: "ARN", value: table.data.tableArn || "–" },
                {
                  label: "Global secondary indexes",
                  value: table.data.globalSecondaryIndexes.join(", ") || "None",
                },
              ]}
            />
          </div>
        ) : null}
      </AwsContainer>
      {deleting && table.data && (
        <DeleteTablesModal
          tables={[table.data]}
          clearSelection={() => navigate("/ui/dynamodb")}
          onClose={() => setDeleting(false)}
        />
      )}
    </>
  );
}
