import { AwsButton, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { fetchBudgets, type Budget } from "../api.js";

// AWS Budgets — Budgets. DescribeBudgets on the real Budgets API. Budgets is
// scoped to an account id, and the console learns its own the way every AWS
// client does — a GetCallerIdentity call to the Security Token Service.

const columns: AwsColumn<Budget>[] = [
  { id: "budgetName", header: "Budget name", cell: (row) => row.budgetName, value: (row) => row.budgetName },
  { id: "budgetType", header: "Budget type", cell: (row) => row.budgetType, value: (row) => row.budgetType },
  { id: "timeUnit", header: "Period", cell: (row) => row.timeUnit, value: (row) => row.timeUnit },
  {
    id: "limit",
    header: "Budgeted",
    cell: (row) => (row.limitAmount ? `${row.limitAmount} ${row.limitUnit}` : "–"),
    value: (row) => row.limitAmount,
  },
  {
    id: "actual",
    header: "Current spend",
    cell: (row) => (row.actualAmount ? `${row.actualAmount} ${row.limitUnit}` : "–"),
    value: (row) => row.actualAmount,
  },
];

export function BudgetsPage() {
  return (
    <AwsResourceTable<Budget>
      title="Budgets"
      description="AWS Budgets defined for this account."
      columns={columns}
      queryKey={["budgets"]}
      queryFn={fetchBudgets}
      filterPlaceholder="Find budgets"
      emptyTitle="No budgets"
      emptyDescription="No budgets are defined for this account."
      rowKey={(row) => row.budgetName}
      tableTestId="budgets-table"
      errorTestId="budgets-error"
      actions={({ refetch, isFetching }) => (
        <AwsButton onClick={refetch} disabled={isFetching}>
          {isFetching ? "Refreshing…" : "Refresh"}
        </AwsButton>
      )}
    />
  );
}
