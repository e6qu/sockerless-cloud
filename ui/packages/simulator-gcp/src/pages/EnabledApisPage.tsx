import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName } from "../console/format.js";
import { disableService, enableService, fetchEnabledServices, waitArOperation, type EnabledService } from "../api.js";
import { useProject } from "../console/project.js";

// A Google Cloud service name is a DNS hostname ("compute.googleapis.com").
const SERVICE_NAME_PATTERN = /^[a-z0-9-]+(\.[a-z0-9-]+)+$/;

const columns: GcpColumn<EnabledService>[] = [
  {
    id: "name",
    header: "Service",
    cell: (row) => row.config?.title ?? shortName(row.name),
    value: (row) => row.config?.title ?? shortName(row.name),
  },
  { id: "id", header: "Service name", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
];

// EnableServiceDialog runs the real services.enable long-running operation —
// the same call `gcloud services enable` makes — and drives it to done through
// the v1 operations.get poll.
export function EnableServiceDialog({ onClose, onEnabled }: { onClose: () => void; onEnabled: () => void }) {
  const { project } = useProject();
  const [service, setService] = useState("");
  const enable = useMutation({
    mutationFn: async () => waitArOperation(await enableService(project, service)),
    onSuccess: onEnabled,
  });
  return (
    <GcpDialog title="Enable an API" testId="apis-enable-dialog" onClose={onClose}>
      <label className="gc-field">
        Service name
        <input
          type="text"
          value={service}
          data-testid="apis-enable-name"
          onChange={(event) => setService(event.target.value)}
        />
        <p className="gc-field-hint">The service's API hostname, e.g. run.googleapis.com.</p>
      </label>
      {enable.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't enable the API.</strong>{" "}
          {enable.error instanceof Error ? enable.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="apis-enable-submit"
          disabled={!SERVICE_NAME_PATTERN.test(service) || enable.isPending}
          onClick={() => enable.mutate()}
        >
          {enable.isPending ? "Enabling…" : "Enable"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DisableServiceDialog({
  service,
  onClose,
  onDisabled,
}: {
  service: string;
  onClose: () => void;
  onDisabled: () => void;
}) {
  const { project } = useProject();
  const disable = useMutation({
    mutationFn: async () => waitArOperation(await disableService(project, service)),
    onSuccess: onDisabled,
  });
  return (
    <GcpDialog title="Disable API?" testId="apis-disable-dialog" onClose={onClose}>
      <p>
        Disabling <strong>{service}</strong> stops this project calling it. Resources that depend on the API
        stop working until it is enabled again.
      </p>
      {disable.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't disable the API.</strong>{" "}
          {disable.error instanceof Error ? disable.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="apis-disable-confirm"
          disabled={disable.isPending}
          onClick={() => disable.mutate()}
        >
          {disable.isPending ? "Disabling…" : "Disable"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function EnabledApisPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [enabling, setEnabling] = useState(false);
  const [disabling, setDisabling] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["enabled-services", project] });

  const columnsWithActions: GcpColumn<EnabledService>[] = [
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
              data-testid={`apis-disable-${id}`}
              aria-label={`Disable ${id}`}
              disabled={row.state !== "ENABLED"}
              onClick={() => setDisabling(id)}
            >
              Disable
            </button>
          </span>
        );
      },
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<EnabledService>
        title="Enabled APIs & services"
        description="Service Usage records which Google Cloud APIs this project may call. Enabling an API grants the project access to it."
        actions={[
          { label: "Enable APIs and services", icon: "add", primary: true, testId: "apis-enable", onSelect: () => setEnabling(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["enabled-services", project]}
        queryFn={() => fetchEnabledServices(project)}
        filterPlaceholder="Filter APIs"
        resourceNoun="APIs"
        empty={{
          headline: "No APIs have been enabled on this project",
          description: "Enable an API to let this project call it.",
          primaryLabel: "Enable APIs and services",
          onPrimary: () => setEnabling(true),
        }}
        rowKey={(row) => row.name}
      />
      {enabling ? (
        <EnableServiceDialog
          onClose={() => setEnabling(false)}
          onEnabled={() => {
            setEnabling(false);
            refresh();
          }}
        />
      ) : null}
      {disabling ? (
        <DisableServiceDialog
          service={disabling}
          onClose={() => setDisabling(null)}
          onDisabled={() => {
            setDisabling(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}
