import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { formatBytes } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  createBigQueryDataset,
  deleteBigQueryDataset,
  fetchBigQueryDataset,
  fetchBigQueryDatasets,
  fetchBigQueryJobs,
  fetchBigQueryTables,
  type BigQueryDataset,
  type BigQueryJob,
  type BigQueryTable,
} from "../api.js";
import { useProject } from "../console/project.js";

// BigQuery dataset IDs are letters, digits and underscores only — the one
// Google Cloud identifier that rejects hyphens.
const DATASET_ID_PATTERN = /^[A-Za-z0-9_]{1,1024}$/;

// A BigQuery epoch-milliseconds field (creationTime, statistics.startTime)
// renders as a readable local date-time; the API sends it as a decimal string.
export function formatEpochMillis(value: string | undefined): string {
  if (!value) return "—";
  const millis = Number(value);
  if (!Number.isFinite(millis)) return value;
  return new Date(millis).toLocaleString();
}

export const datasetId = (dataset: BigQueryDataset): string =>
  dataset.datasetReference?.datasetId ?? dataset.id ?? "";

const columns: GcpColumn<BigQueryDataset>[] = [
  {
    id: "name",
    header: "Dataset ID",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/bigquery/${datasetId(row)}`}>
        {datasetId(row)}
      </Link>
    ),
    value: datasetId,
  },
  { id: "location", header: "Location", cell: (row) => row.location ?? "—", value: (row) => row.location ?? "" },
  {
    id: "friendlyName",
    header: "Display name",
    cell: (row) => row.friendlyName || "—",
    value: (row) => row.friendlyName ?? "",
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatEpochMillis(row.creationTime),
    value: (row) => row.creationTime ?? "",
  },
];

// CreateDatasetDialog runs the real bigquery.datasets.insert method, which
// answers synchronously with the created Dataset.
export function CreateDatasetDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [id, setId] = useState("");
  const [location, setLocation] = useState("US");

  const create = useMutation({
    mutationFn: () => createBigQueryDataset(project, id, location),
    onSuccess: onCreated,
  });

  return (
    <GcpDialog title="Create dataset" testId="bigquery-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Dataset ID
        <input type="text" value={id} data-testid="bigquery-create-id" onChange={(event) => setId(event.target.value)} />
        <p className="gc-field-hint">Letters, numbers and underscores only — hyphens are not allowed.</p>
      </label>
      <label className="gc-field">
        Location type
        <select
          value={location}
          data-testid="bigquery-create-location"
          onChange={(event) => setLocation(event.target.value)}
        >
          <option value="US">US (multi-region)</option>
          <option value="EU">EU (multi-region)</option>
          <option value="us-central1">us-central1</option>
        </select>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the dataset.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="bigquery-create-submit"
          disabled={!DATASET_ID_PATTERN.test(id) || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create dataset"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteDatasetDialog({
  dataset,
  onClose,
  onDeleted,
}: {
  dataset: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({ mutationFn: () => deleteBigQueryDataset(project, dataset), onSuccess: onDeleted });
  return (
    <GcpDialog title="Delete dataset?" testId="bigquery-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{dataset}</strong> permanently removes the dataset and every table in it.
        This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the dataset.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="bigquery-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function BigQueryPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["bigquery-datasets", project] });

  const columnsWithActions: GcpColumn<BigQueryDataset>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const id = datasetId(row);
        return (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`bigquery-delete-${id}`}
              aria-label={`Delete ${id}`}
              onClick={() => setDeleting(id)}
            >
              Delete
            </button>
          </span>
        );
      },
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<BigQueryDataset>
        title="BigQuery datasets"
        description="BigQuery is a serverless, highly scalable data warehouse. A dataset is the top-level container for your tables and views."
        actions={[
          { label: "Create dataset", icon: "add", primary: true, testId: "bigquery-create-dataset", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["bigquery-datasets", project]}
        queryFn={() => fetchBigQueryDatasets(project)}
        filterPlaceholder="Filter datasets"
        resourceNoun="datasets"
        empty={{
          headline: "Create a dataset to hold your tables",
          description: "A dataset groups tables, views and routines in one location.",
          primaryLabel: "Create dataset",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => datasetId(row)}
      />
      {creating ? (
        <CreateDatasetDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteDatasetDialog
          dataset={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}

export function BigQueryDatasetDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const dataset = useQuery({
    queryKey: ["bigquery-dataset", project, name],
    queryFn: () => fetchBigQueryDataset(project, name),
  });
  const tables = useQuery({
    queryKey: ["bigquery-tables", project, name],
    queryFn: () => fetchBigQueryTables(project, name),
  });
  const jobs = useQuery({ queryKey: ["bigquery-jobs", project], queryFn: () => fetchBigQueryJobs(project) });

  const data = dataset.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/bigquery">‹ BigQuery datasets</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="BigQuery dataset"
        actions={[{ label: "Delete", testId: "bigquery-dataset-delete", onSelect: () => setDeleting(true) }]}
        onRefresh={() => {
          void dataset.refetch();
          void tables.refetch();
          void jobs.refetch();
        }}
        refreshing={dataset.isFetching || tables.isFetching || jobs.isFetching}
      />
      {dataset.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this dataset.</strong>{" "}
          {dataset.error instanceof Error ? dataset.error.message : "The simulator did not respond."}
        </div>
      ) : dataset.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading dataset…</div>
      ) : (
        <GcpTabs
          label="Dataset detail"
          tabs={[
            {
              id: "tables",
              label: "Tables",
              content: (
                <SubResourceTable<BigQueryTable>
                  query={tables}
                  testId="bigquery-tables-table"
                  noun="tables"
                  emptyHeadline="This dataset has no tables"
                  emptyDescription="Tables created in this dataset appear here."
                  rowKey={(row) => row.id ?? row.tableReference?.tableId ?? ""}
                  columns={[
                    { header: "Table ID", cell: (row) => row.tableReference?.tableId ?? "—" },
                    { header: "Type", cell: (row) => row.type ?? "—" },
                    { header: "Rows", cell: (row) => row.numRows ?? "—" },
                    { header: "Size", cell: (row) => formatBytes(row.numBytes) },
                    { header: "Created", cell: (row) => formatEpochMillis(row.creationTime) },
                  ]}
                />
              ),
            },
            {
              id: "jobs",
              label: "Project jobs",
              content: (
                <SubResourceTable<BigQueryJob>
                  query={jobs}
                  testId="bigquery-jobs-table"
                  noun="jobs"
                  emptyHeadline="This project has no BigQuery jobs"
                  emptyDescription="Query, load, extract and copy jobs appear here."
                  rowKey={(row) => row.id ?? row.jobReference?.jobId ?? ""}
                  columns={[
                    { header: "Job ID", cell: (row) => row.jobReference?.jobId ?? "—" },
                    { header: "Type", cell: (row) => row.configuration?.jobType ?? "—" },
                    {
                      header: "State",
                      cell: (row) => <GcpStatus status={row.status?.state ?? row.state ?? "Unknown"} />,
                    },
                    { header: "Started", cell: (row) => formatEpochMillis(row.statistics?.startTime) },
                    { header: "Error", cell: (row) => row.status?.errorResult?.message ?? "—" },
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
                    { label: "Dataset ID", value: data.id ?? "—" },
                    { label: "Location", value: data.location ?? "—" },
                    { label: "Display name", value: data.friendlyName || "—" },
                    { label: "Description", value: data.description || "—" },
                    { label: "Created", value: formatEpochMillis(data.creationTime) },
                    { label: "Last modified", value: formatEpochMillis(data.lastModifiedTime) },
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
      {deleting ? (
        <DeleteDatasetDialog
          dataset={name}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate("/ui/bigquery")}
        />
      ) : null}
    </>
  );
}
