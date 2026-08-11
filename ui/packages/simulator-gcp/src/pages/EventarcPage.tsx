import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  deleteEventarcTrigger,
  fetchEventarcProviders,
  fetchEventarcTrigger,
  fetchEventarcTriggers,
  waitArOperation,
  type EventarcProvider,
  type EventarcTrigger,
} from "../api.js";
import { useProject } from "../console/project.js";

// A trigger's event type is the `type` event filter — the attribute Eventarc
// matches every incoming CloudEvent against.
export function triggerEventType(trigger: EventarcTrigger): string {
  return trigger.eventFilters?.find((filter) => filter.attribute === "type")?.value ?? "—";
}

// The destination is one of the oneof arms the API models; the real console
// shows the target resource rather than the wrapper.
export function triggerDestination(trigger: EventarcTrigger): string {
  const run = trigger.destination?.cloudRun;
  if (run?.service) return `Cloud Run: ${run.service}${run.region ? ` (${run.region})` : ""}`;
  if (trigger.destination?.cloudFunction) return `Cloud Run function: ${shortName(trigger.destination.cloudFunction)}`;
  return "—";
}

const columns: GcpColumn<EventarcTrigger>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/eventarc/${shortName(row.name)}`}>
        {shortName(row.name)}
      </Link>
    ),
    value: (row) => shortName(row.name),
  },
  { id: "eventType", header: "Event type", cell: triggerEventType, value: triggerEventType },
  { id: "destination", header: "Destination", cell: triggerDestination, value: triggerDestination },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.createTime ?? ""),
    value: (row) => row.createTime ?? "",
  },
];

// DeleteTriggerDialog runs the real projects.locations.triggers.delete
// long-running operation and drives it to done through the v1 operations.get
// poll the other v1 collections use.
export function DeleteTriggerDialog({
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
    mutationFn: async () => waitArOperation(await deleteEventarcTrigger(project, name)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete trigger?" testId="eventarc-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> stops routing matching events to its destination. This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the trigger.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="eventarc-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function EventarcPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<string | null>(null);
  const providers = useQuery({
    queryKey: ["eventarc-providers", project],
    queryFn: () => fetchEventarcProviders(project),
  });

  const columnsWithActions: GcpColumn<EventarcTrigger>[] = [
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
              data-testid={`eventarc-delete-${id}`}
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
      <GcpResourceTable<EventarcTrigger>
        title="Eventarc triggers"
        description={`Eventarc routes events from Google Cloud sources and third parties to your services. Showing triggers in ${CONSOLE_REGION}.`}
        columns={columnsWithActions}
        queryKey={["eventarc-triggers", project]}
        queryFn={() => fetchEventarcTriggers(project)}
        filterPlaceholder="Filter triggers"
        resourceNoun="triggers"
        empty={{
          headline: "Create a trigger to route events",
          description: "A trigger matches events by their attributes and delivers them to a destination.",
          primaryLabel: "Create trigger",
        }}
        rowKey={(row) => row.name}
      />
      <h2 className="gc-detail-heading">Event providers</h2>
      <SubResourceTable<EventarcProvider>
        query={providers}
        testId="eventarc-providers-table"
        noun="event providers"
        emptyHeadline="No event providers are available in this region"
        emptyDescription="Providers publish the event types a trigger can match."
        rowKey={(row) => row.name}
        columns={[
          { header: "Provider", cell: (row) => row.displayName ?? shortName(row.name) },
          { header: "Provider ID", cell: (row) => shortName(row.name) },
          {
            header: "Event types",
            cell: (row) => (row.eventTypes ?? []).map((eventType) => eventType.type).join(", ") || "—",
          },
        ]}
      />
      {deleting ? (
        <DeleteTriggerDialog
          name={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["eventarc-triggers", project] });
          }}
        />
      ) : null}
    </>
  );
}

export function EventarcTriggerDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const [deleting, setDeleting] = useState(false);
  const trigger = useQuery({
    queryKey: ["eventarc-trigger", project, name],
    queryFn: () => fetchEventarcTrigger(project, name),
  });

  const data = trigger.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/eventarc">‹ Eventarc triggers</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Eventarc trigger"
        actions={[{ label: "Delete", testId: "eventarc-trigger-delete", onSelect: () => setDeleting(true) }]}
        onRefresh={() => void trigger.refetch()}
        refreshing={trigger.isFetching}
      />
      {trigger.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this trigger.</strong>{" "}
          {trigger.error instanceof Error ? trigger.error.message : "The simulator did not respond."}
        </div>
      ) : trigger.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading trigger…</div>
      ) : (
        <GcpTabs
          label="Trigger detail"
          tabs={[
            {
              id: "details",
              label: "Details",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Event type", value: triggerEventType(data) },
                    { label: "Destination", value: triggerDestination(data) },
                    { label: "Service account", value: data.serviceAccount ?? "—" },
                    { label: "Transport topic", value: data.transport?.pubsub?.topic ?? "—" },
                    { label: "Trigger UID", value: data.uid ?? "—" },
                    { label: "Created", value: formatTimestamp(data.createTime ?? "") },
                    { label: "Updated", value: formatTimestamp(data.updateTime ?? "") },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
            {
              id: "filters",
              label: "Event filters",
              content: (
                <div className="gc-table-wrap">
                  <table className="gc-table" data-testid="eventarc-filters-table">
                    <thead>
                      <tr>
                        <th>Attribute</th>
                        <th>Value</th>
                      </tr>
                    </thead>
                    <tbody>
                      {(data.eventFilters ?? []).length === 0 ? (
                        <tr>
                          <td className="gc-table-state" colSpan={2}>
                            <div className="gc-empty">
                              <p className="gc-empty-headline">This trigger has no event filters</p>
                              <p className="gc-empty-description">
                                Filters narrow the events the trigger matches by their CloudEvent attributes.
                              </p>
                            </div>
                          </td>
                        </tr>
                      ) : (
                        (data.eventFilters ?? []).map((filter) => (
                          <tr key={`${filter.attribute}-${filter.value}`}>
                            <td>{filter.attribute ?? "—"}</td>
                            <td>{filter.value ?? "—"}</td>
                          </tr>
                        ))
                      )}
                    </tbody>
                  </table>
                </div>
              ),
            },
          ]}
        />
      )}
      {deleting ? (
        <DeleteTriggerDialog
          name={name}
          onClose={() => setDeleting(false)}
          onDeleted={() => {
            setDeleting(false);
            void trigger.refetch();
          }}
        />
      ) : null}
    </>
  );
}
