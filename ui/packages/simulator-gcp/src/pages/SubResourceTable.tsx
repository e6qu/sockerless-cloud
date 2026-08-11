import { type ReactNode } from "react";
import { type UseQueryResult } from "@tanstack/react-query";

export interface SubResourceColumn<T> {
  header: string;
  cell: (row: T) => ReactNode;
}

// SubResourceTable is the console's secondary table: the plain table a
// resource-detail tab (Cloud Storage's Objects, Artifact Registry's Images)
// puts under its own heading, as opposed to the filterable, sortable,
// paginated GcpResourceTable a product's top-level list page uses. Presented
// with the query that feeds it so the loading, error and empty states read the
// same on every tab — the API's own error message on failure, an illustrated
// empty state otherwise — rather than each caller reinventing them.
export function SubResourceTable<T>({
  query,
  columns,
  rowKey,
  testId,
  noun,
  emptyHeadline,
  emptyDescription,
}: {
  query: UseQueryResult<T[]>;
  columns: SubResourceColumn<T>[];
  rowKey: (row: T) => string;
  testId: string;
  noun: string;
  emptyHeadline: string;
  emptyDescription: string;
}) {
  if (query.isError) {
    return (
      <div className="gc-message gc-message-error" role="alert">
        <strong>Couldn't load {noun}.</strong>{" "}
        {query.error instanceof Error ? query.error.message : "The simulator did not respond."}
      </div>
    );
  }
  if (query.isLoading) {
    return <div className="gc-loading" role="status">Loading {noun}…</div>;
  }
  const rows = query.data ?? [];
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid={testId}>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.header}>{column.header}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={columns.length}>
                <div className="gc-empty">
                  <svg
                    className="gc-empty-illustration"
                    viewBox="0 0 160 96"
                    role="img"
                    aria-label="No resources illustration"
                  >
                    <path
                      d="M44 74 a20 20 0 0 1 2 -40 a26 26 0 0 1 50 -6 a18 18 0 0 1 18 22 a16 16 0 0 1 -4 24 z"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="2"
                      strokeDasharray="6 6"
                      strokeLinejoin="round"
                    />
                  </svg>
                  <p className="gc-empty-headline">{emptyHeadline}</p>
                  <p className="gc-empty-description">{emptyDescription}</p>
                </div>
              </td>
            </tr>
          ) : (
            rows.map((row) => (
              <tr key={rowKey(row)}>
                {columns.map((column) => (
                  <td key={column.header}>{column.cell(row)}</td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
