import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName } from "../console/format.js";
import {
  CONSOLE_REGION,
  deleteVpcConnector,
  fetchVpcConnectors,
  waitArOperation,
  type VpcAccessConnector,
} from "../api.js";
import { useProject } from "../console/project.js";

const columns: GcpColumn<VpcAccessConnector>[] = [
  { id: "name", header: "Name", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
  { id: "network", header: "Network", cell: (row) => shortName(row.network ?? "") || "—", value: (row) => row.network ?? "" },
  { id: "range", header: "IP range", cell: (row) => row.ipCidrRange ?? "—", value: (row) => row.ipCidrRange ?? "" },
  {
    id: "machineType",
    header: "Machine type",
    cell: (row) => row.machineType ?? "—",
    value: (row) => row.machineType ?? "",
  },
  {
    id: "instances",
    header: "Instances",
    cell: (row) => `${row.minInstances ?? "—"}–${row.maxInstances ?? "—"}`,
    value: (row) => `${row.minInstances ?? ""}-${row.maxInstances ?? ""}`,
  },
];

// DeleteConnectorDialog runs the real projects.locations.connectors.delete
// long-running operation and drives it to completion through the same
// operations.get poll (waitArOperation) the other v1 collections use.
export function DeleteConnectorDialog({
  name,
  onClose,
  onDeleted,
}: {
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({
    mutationFn: async () => waitArOperation(await deleteVpcConnector(project, name)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete connector?" testId="vpcaccess-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> removes the Serverless VPC Access connector. Services still routing
        egress through it lose access to the VPC network.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the connector.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="vpcaccess-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function VpcAccessPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<string | null>(null);

  const columnsWithActions: GcpColumn<VpcAccessConnector>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const id = shortName(row.name);
        return (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`vpcaccess-delete-${id}`}
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
      <GcpResourceTable<VpcAccessConnector>
        title="Serverless VPC Access"
        description={`Serverless VPC Access connectors let Cloud Run, Cloud Run functions and App Engine reach resources inside a VPC network. Showing connectors in ${CONSOLE_REGION}.`}
        columns={columnsWithActions}
        queryKey={["vpc-connectors", project]}
        queryFn={() => fetchVpcConnectors(project)}
        filterPlaceholder="Filter connectors"
        resourceNoun="connectors"
        empty={{
          headline: "Create a connector to reach your VPC network",
          description: "A connector gives serverless workloads a route into a VPC network's internal IP ranges.",
          primaryLabel: "Create connector",
        }}
        rowKey={(row) => row.name}
      />
      {deleting ? (
        <DeleteConnectorDialog
          name={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["vpc-connectors", project] });
          }}
        />
      ) : null}
    </>
  );
}
