import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  deleteApiGatewayApi,
  fetchApiGatewayApis,
  fetchApiGatewayGateways,
  waitArOperation,
  type ApiGatewayApi,
  type ApiGatewayGateway,
} from "../api.js";
import { useProject } from "../console/project.js";

const columns: GcpColumn<ApiGatewayGateway>[] = [
  { id: "name", header: "Gateway ID", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
  {
    id: "displayName",
    header: "Display name",
    cell: (row) => row.displayName ?? "—",
    value: (row) => row.displayName ?? "",
  },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
  {
    id: "apiConfig",
    header: "API config",
    cell: (row) => (row.apiConfig ? shortName(row.apiConfig) : "—"),
    value: (row) => row.apiConfig ?? "",
  },
  {
    id: "hostname",
    header: "Gateway URL",
    cell: (row) => row.defaultHostname ?? "—",
    value: (row) => row.defaultHostname ?? "",
  },
];

// DeleteApiDialog runs the real projects.locations.apis.delete long-running
// operation and drives it to done through the v1 operations.get poll.
export function DeleteApiDialog({
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
    mutationFn: async () => waitArOperation(await deleteApiGatewayApi(project, name)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete API?" testId="apigateway-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> removes the API and every configuration under it. Gateways serving one
        of those configurations stop responding.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the API.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="apigateway-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function ApiGatewayPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<string | null>(null);
  const apis = useQuery({ queryKey: ["apigateway-apis", project], queryFn: () => fetchApiGatewayApis(project) });

  return (
    <>
      <GcpResourceTable<ApiGatewayGateway>
        title="API Gateway"
        description={`API Gateway lets you serve, secure and monitor an API backed by Cloud Run, Cloud Run functions or App Engine. Showing gateways in ${CONSOLE_REGION}.`}
        columns={columns}
        queryKey={["apigateway-gateways", project]}
        queryFn={() => fetchApiGatewayGateways(project)}
        filterPlaceholder="Filter gateways"
        resourceNoun="gateways"
        empty={{
          headline: "Create a gateway to serve your API",
          description: "A gateway serves one API configuration at a managed hostname.",
          primaryLabel: "Create gateway",
        }}
        rowKey={(row) => row.name}
      />
      <h2 className="gc-detail-heading">APIs</h2>
      <SubResourceTable<ApiGatewayApi>
        query={apis}
        testId="apigateway-apis-table"
        noun="APIs"
        emptyHeadline="This project has no APIs"
        emptyDescription="An API groups the configurations a gateway can serve."
        rowKey={(row) => row.name}
        columns={[
          { header: "API ID", cell: (row) => shortName(row.name) },
          { header: "Display name", cell: (row) => row.displayName ?? "—" },
          { header: "Status", cell: (row) => <GcpStatus status={row.state ?? "Unknown"} /> },
          { header: "Managed service", cell: (row) => row.managedService ?? "—" },
          { header: "Created", cell: (row) => formatTimestamp(row.createTime ?? "") },
          {
            header: "Actions",
            cell: (row) => (
              <button
                type="button"
                className="gc-button-text"
                data-testid={`apigateway-delete-${shortName(row.name)}`}
                aria-label={`Delete ${shortName(row.name)}`}
                onClick={() => setDeleting(shortName(row.name))}
              >
                Delete
              </button>
            ),
          },
        ]}
      />
      {deleting ? (
        <DeleteApiDialog
          name={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["apigateway-apis", project] });
          }}
        />
      ) : null}
    </>
  );
}
