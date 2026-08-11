import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatBytes, formatEpoch } from "../console/format.js";
import {
  createDynamoDBTable,
  deleteDynamoDBTable,
  fetchDynamoDBTables,
  type DynamoDBTable,
} from "../api.js";

// Amazon DynamoDB — Tables. ListTables plus a DescribeTable per table (DynamoDB
// has no operation that answers every table's properties at once), CreateTable
// and DeleteTable for the header actions.

const columns: AwsColumn<DynamoDBTable>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <AwsRowLink to={`/ui/dynamodb/${encodeURIComponent(row.tableName)}`}>{row.tableName}</AwsRowLink>,
    value: (row) => row.tableName,
  },
  {
    id: "status",
    header: "Status",
    cell: (row) => <AwsStatus status={row.tableStatus} />,
    value: (row) => row.tableStatus,
  },
  {
    id: "partitionKey",
    header: "Partition key",
    cell: (row) => row.partitionKey || "–",
    value: (row) => row.partitionKey,
  },
  { id: "sortKey", header: "Sort key", cell: (row) => row.sortKey || "–", value: (row) => row.sortKey },
  { id: "billingMode", header: "Capacity mode", cell: (row) => row.billingMode, value: (row) => row.billingMode },
  { id: "itemCount", header: "Item count", cell: (row) => String(row.itemCount), value: (row) => String(row.itemCount) },
  {
    id: "tableSizeBytes",
    header: "Size",
    cell: (row) => formatBytes(row.tableSizeBytes),
    value: (row) => String(row.tableSizeBytes),
  },
  {
    id: "creationDateTime",
    header: "Created",
    cell: (row) => formatEpoch(row.creationDateTime),
    value: (row) => String(row.creationDateTime),
  },
];

const KEY_TYPES = [
  { label: "String", value: "S" },
  { label: "Number", value: "N" },
  { label: "Binary", value: "B" },
];

// The table-name shape real DynamoDB enforces on CreateTable: 3–255 characters
// of letters, numbers, and the separators `_` `-` `.`.
const DDB_TABLE_NAME_PATTERN = /^[A-Za-z0-9_.-]+$/;
function isValidTableName(name: string): boolean {
  return name.length >= 3 && name.length <= 255 && DDB_TABLE_NAME_PATTERN.test(name);
}

function CreateTableModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [tableName, setTableName] = useState("");
  const [partitionKey, setPartitionKey] = useState("");
  const [partitionKeyType, setPartitionKeyType] = useState("S");
  const [sortKey, setSortKey] = useState("");
  const [sortKeyType, setSortKeyType] = useState("S");
  const create = useMutation({
    mutationFn: () =>
      createDynamoDBTable({
        tableName: tableName.trim(),
        partitionKey: partitionKey.trim(),
        partitionKeyType: partitionKeyType as "S" | "N" | "B",
        sortKey: sortKey.trim() || undefined,
        sortKeyType: sortKeyType as "S" | "N" | "B",
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["dynamodb-tables"] });
      onClose();
    },
  });
  const valid = isValidTableName(tableName.trim()) && partitionKey.trim().length > 0;
  return (
    <AwsModal
      title="Create table"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="dynamodb-create-table-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create table"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>A table is created in on-demand capacity mode, the default DynamoDB applies when no throughput is specified.</p>
        <FormField label="Table name" constraintText="3–255 characters. Letters, numbers, and the separators _ - .">
          <Input
            value={tableName}
            onChange={(event) => setTableName(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "dynamodb-table-name-input" }}
          />
        </FormField>
        <FormField label="Partition key">
          <Input
            value={partitionKey}
            onChange={(event) => setPartitionKey(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "dynamodb-partition-key-input" }}
          />
        </FormField>
        <FormField label="Partition key type">
          <Select
            selectedOption={KEY_TYPES.find((option) => option.value === partitionKeyType) ?? KEY_TYPES[0]}
            options={KEY_TYPES}
            ariaLabel="Partition key type"
            onChange={(event) => setPartitionKeyType(event.detail.selectedOption.value ?? "S")}
            data-testid="dynamodb-partition-key-type"
          />
        </FormField>
        <FormField label="Sort key - optional">
          <Input
            value={sortKey}
            onChange={(event) => setSortKey(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "dynamodb-sort-key-input" }}
          />
        </FormField>
        {sortKey.trim() && (
          <FormField label="Sort key type">
            <Select
              selectedOption={KEY_TYPES.find((option) => option.value === sortKeyType) ?? KEY_TYPES[0]}
              options={KEY_TYPES}
              ariaLabel="Sort key type"
              onChange={(event) => setSortKeyType(event.detail.selectedOption.value ?? "S")}
              data-testid="dynamodb-sort-key-type"
            />
          </FormField>
        )}
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the table.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function DeleteTablesModal({
  tables,
  onClose,
  clearSelection,
}: {
  tables: DynamoDBTable[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const table of tables) {
        await deleteDynamoDBTable(table.tableName);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["dynamodb-tables"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={tables.length === 1 ? `Delete ${tables[0].tableName}?` : `Delete ${tables.length} tables?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="dynamodb-delete-table-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a table is permanent and deletes every item it holds.</p>
      <ul>
        {tables.map((table) => (
          <li key={table.tableName}>
            <code>{table.tableName}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function DynamoDBTablesPage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ tables: DynamoDBTable[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<DynamoDBTable>
        title="Tables"
        description="DynamoDB tables in this account and Region."
        columns={columns}
        queryKey={["dynamodb-tables"]}
        queryFn={fetchDynamoDBTables}
        filterPlaceholder="Find tables"
        emptyTitle="No tables"
        emptyDescription="No DynamoDB tables exist in this account and Region."
        rowKey={(row) => row.tableName}
        tableTestId="dynamodb-tables-table"
        errorTestId="dynamodb-tables-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="dynamodb-view-table"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/dynamodb/${encodeURIComponent(selected[0].tableName)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="dynamodb-delete-table"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ tables: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="dynamodb-create-table" onClick={() => setCreating(true)}>
              Create table
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateTableModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteTablesModal
          tables={deleting.tables}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
