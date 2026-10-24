import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { Copyable } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpResourceDetail } from "../console/GcpResourceDetail.js";
import { ResourceTableView } from "../console/GcpTable.js";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { LabelsEditor, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  CONSOLE_REGION,
  cloudRunServiceRequest,
  deleteCloudRunService,
  fetchCloudRunRevisions,
  fetchCloudRunService,
  fetchCloudRunServices,
  sendRequest,
  waitRunOperation,
  type CloudRunRevision,
  type CloudRunService,
  type CrmOperation,
} from "../api.js";
import { LogsTab } from "./LogsTab.js";

// A Cloud Run resource is ready when its Ready condition succeeded; the
// console reads the terminal condition rather than inventing a status.
export function serviceState(service: Pick<CloudRunService, "terminalCondition" | "reconciling">): string {
  if (service.reconciling) return "Deploying";
  const condition = service.terminalCondition;
  if (!condition) return "Unknown";
  if (condition.state === "CONDITION_SUCCEEDED") return "Ready";
  if (condition.state === "CONDITION_FAILED") return "Failed";
  if (condition.state === "CONDITION_PENDING" || condition.state === "CONDITION_RECONCILING") return "Deploying";
  return condition.state ?? "Unknown";
}

const INGRESS_LABELS: Record<string, string> = {
  INGRESS_TRAFFIC_ALL: "All",
  INGRESS_TRAFFIC_INTERNAL_ONLY: "Internal",
  INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER: "Internal and Cloud Load Balancing",
};

const regionOf = (name: string): string => name.split("/")[3] ?? CONSOLE_REGION;

function ServiceName({ service }: { service: CloudRunService }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(`/ui/run/detail/${regionOf(service.name)}/${shortName(service.name)}`)}>
      {shortName(service.name)}
    </Link>
  );
}

const columns: GcpColumn<CloudRunService>[] = [
  {
    id: "status",
    header: "Status",
    cell: (row) => <GcpStatus status={serviceState(row)} />,
    value: serviceState,
  },
  { id: "name", header: "Name", cell: (row) => <ServiceName service={row} />, value: (row) => shortName(row.name) },
  { id: "deploymentType", header: "Deployment type", cell: () => "Container", value: () => "Container" },
  { id: "region", header: "Region", cell: (row) => regionOf(row.name), value: (row) => regionOf(row.name) },
  {
    id: "ingress",
    header: "Ingress",
    cell: (row) => INGRESS_LABELS[row.ingress ?? ""] ?? row.ingress ?? "—",
    value: (row) => INGRESS_LABELS[row.ingress ?? ""] ?? row.ingress ?? "",
  },
  { id: "lastDeployed", header: "Last deployed", cell: (row) => formatTimestamp(row.updateTime ?? ""), value: (row) => row.updateTime ?? "" },
  { id: "deployedBy", header: "Deployed by", cell: (row) => row.lastModifier ?? "—", value: (row) => row.lastModifier ?? "" },
];

function DeleteServicesDialog({
  services,
  onClose,
  onDeleted,
}: {
  services: { region: string; name: string }[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const { track } = useOperations();
  const subject = services.length === 1 ? `service "${services[0].name}"` : `${services.length} services`;
  const remove = useMutation({
    mutationFn: () =>
      track(
        `Delete ${subject}`,
        () =>
          Promise.all(
            services.map(async ({ region, name }) => waitRunOperation(await deleteCloudRunService(project, region, name))),
          ),
        `Deleted ${subject}`,
      ),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="run-service-delete-dialog" onClose={onClose}>
      <p>Deleting a service removes its revisions and stops serving its URL. This can't be undone.</p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete {subject}.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="gc-button-primary gc-button-danger"
          data-testid="run-service-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudRunServicesPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<{ region: string; name: string }[] | null>(null);
  const refs = (rows: CloudRunService[]) => rows.map((row) => ({ region: regionOf(row.name), name: shortName(row.name) }));
  return (
    <>
      <GcpResourceTable<CloudRunService>
        title="Services"
        actions={[{ label: "Deploy container", icon: "add", to: "/ui/run/create", testId: "run-create-service" }]}
        selectionActions={(selected) => [{ label: "Delete", icon: "delete", onSelect: () => setDeleting(refs(selected)) }]}
        rowActions={(row) => [{ label: "Delete", onSelect: () => setDeleting(refs([row])) }]}
        columns={columns}
        queryKey={["cloudrun-services", project]}
        queryFn={() => fetchCloudRunServices(project)}
        resourceNoun="services"
        testId="run-services-table"
        empty={{
          headline: "No services",
          description: "Cloud Run services serve requests from a container that scales with traffic.",
        }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
      />
      {deleting ? (
        <DeleteServicesDialog
          services={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["cloudrun-services", project] });
          }}
        />
      ) : null}
    </>
  );
}

function RevisionsTab({ region, name }: { region: string; name: string }) {
  const { project } = useProject();
  const revisions = useQuery({
    queryKey: ["cloudrun-revisions", project, region, name],
    queryFn: () => fetchCloudRunRevisions(project, region, name),
  });
  return (
    <ResourceTableView<CloudRunRevision>
      rows={revisions.data}
      isLoading={revisions.isLoading}
      error={revisions.error}
      tableId="run-revisions-table"
      resourceNoun="revisions"
      rowKey={(row) => row.name}
      columns={[
        {
          id: "name",
          header: "Name",
          cell: (row) => <GcpStatus status={revisionState(row)} label={shortName(row.name)} />,
          value: (row) => shortName(row.name),
        },
        {
          id: "image",
          header: "Container image",
          cell: (row) => row.containers?.[0]?.image ?? "—",
          value: (row) => row.containers?.[0]?.image ?? "",
        },
        { id: "deployed", header: "Deployed", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
      ]}
      empty={{ headline: "No revisions", description: "Each deployment of the service creates a revision." }}
    />
  );
}

function revisionState(revision: CloudRunRevision): string {
  const ready = revision.conditions?.find((condition) => condition.type === "Ready");
  if (!ready) return "Unknown";
  return ready.state === "CONDITION_SUCCEEDED" ? "Ready" : ready.state === "CONDITION_FAILED" ? "Failed" : "Deploying";
}

export function CloudRunServiceDetailPage() {
  const { region = CONSOLE_REGION, name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const [deleting, setDeleting] = useState(false);
  return (
    <>
      <GcpResourceDetail<CloudRunService>
        title={name}
        backTo="/ui/run"
        queryKey={["cloudrun-service", project, region, name]}
        queryFn={() => fetchCloudRunService(project, region, name)}
        actions={[{ label: "Delete", icon: "delete", testId: "run-service-delete", onSelect: () => setDeleting(true) }]}
        properties={(service) => [
          { label: "Status", value: <GcpStatus status={serviceState(service)} /> },
          { label: "URL", value: service.uri ? <Copyable value={service.uri} /> : "—" },
          { label: "Region", value: region },
          { label: "Container image", value: service.template?.containers?.[0]?.image ?? "—" },
          { label: "Container port", value: service.template?.containers?.[0]?.ports?.[0]?.containerPort ?? "—" },
          { label: "Ingress", value: INGRESS_LABELS[service.ingress ?? ""] ?? service.ingress ?? "—" },
          {
            label: "Scaling",
            value: `Min ${service.template?.scaling?.minInstanceCount ?? 0}, max ${service.template?.scaling?.maxInstanceCount ?? "default"}`,
          },
          { label: "Service account", value: service.template?.serviceAccount ?? "—" },
          { label: "Latest ready revision", value: shortName(service.latestReadyRevision ?? "") || "—" },
          { label: "Created", value: formatTimestamp(service.createTime ?? "") },
          { label: "Last deployed by", value: service.lastModifier ?? "—" },
        ]}
        tabs={() => [
          { id: "revisions", label: "Revisions", content: <RevisionsTab region={region} name={name} /> },
          {
            id: "logs",
            label: "Logs",
            content: (
              <LogsTab
                testId="run-service-logs"
                filter={`resource.type="cloud_run_revision" AND resource.labels.service_name="${name}"`}
              />
            ),
          },
        ]}
      />
      {deleting ? (
        <DeleteServicesDialog
          services={[{ region, name }]}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate(path("/ui/run"))}
        />
      ) : null}
    </>
  );
}

const SERVICE_NAME_PATTERN = /^[a-z](?:[a-z0-9-]{0,47}[a-z0-9])?$/;

export function CloudRunServiceCreatePage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const [image, setImage] = useState("");
  const [name, setName] = useState("");
  const [region, setRegion] = useState(CONSOLE_REGION);
  const [minInstances, setMinInstances] = useState(0);
  const [maxInstances, setMaxInstances] = useState(100);
  const [ingress, setIngress] = useState("INGRESS_TRAFFIC_ALL");
  const [port, setPort] = useState(8080);
  const [env, setEnv] = useState<LabelPair[]>([]);
  const nameError = name && !SERVICE_NAME_PATTERN.test(name)
    ? "Use up to 49 lowercase letters, numbers or hyphens, starting with a letter and not ending with a hyphen."
    : null;
  const request = cloudRunServiceRequest(project, region, name, {
    image,
    port,
    minInstances,
    maxInstances,
    ingress,
    env: Object.entries(pairsToLabels(env)).map(([key, value]) => ({ name: key, value })),
  });
  const create = useMutation({
    mutationFn: () =>
      track(
        `Create service "${name}"`,
        async () => waitRunOperation(await sendRequest<CrmOperation>(request)),
        `Created service "${name}"`,
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cloudrun-services", project] });
      navigate(path(`/ui/run/detail/${region}/${name}`));
    },
  });
  return (
    <CreatePage
      title="Create service"
      backTo="/ui/run"
      testId="run-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(image && name && !nameError && minInstances <= maxInstances)}
      error={create.error}
      errorTitle="Couldn't create the service."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Deploy one revision from an existing container image">
        <Field label="Container image URL" hint="For example, us-docker.pkg.dev/cloudrun/container/hello">
          <input type="text" value={image} data-testid="run-create-image" onChange={(event) => setImage(event.target.value)} />
        </Field>
      </FormSection>
      <FormSection title="Configure">
        <Field label="Service name" error={nameError}>
          <input type="text" value={name} data-testid="run-create-name" onChange={(event) => setName(event.target.value)} />
        </Field>
        <Field label="Region">
          <select value={region} onChange={(event) => setRegion(event.target.value)}>
            <option value={CONSOLE_REGION}>{CONSOLE_REGION}</option>
          </select>
        </Field>
      </FormSection>
      <FormSection title="Service scaling">
        <div className="gc-field-row">
          <Field label="Minimum number of instances">
            <input type="number" min={0} value={minInstances} onChange={(event) => setMinInstances(Number(event.target.value))} />
          </Field>
          <Field label="Maximum number of instances">
            <input type="number" min={1} value={maxInstances} onChange={(event) => setMaxInstances(Number(event.target.value))} />
          </Field>
        </div>
      </FormSection>
      <FormSection title="Ingress">
        <fieldset className="gc-radio-group">
          <legend className="gc-visually-hidden">Ingress</legend>
          {Object.entries(INGRESS_LABELS).map(([value, label]) => (
            <label className="gc-radio" key={value}>
              <input type="radio" name="ingress" checked={ingress === value} onChange={() => setIngress(value)} />
              {label}
            </label>
          ))}
        </fieldset>
      </FormSection>
      <FormSection title="Container">
        <Field label="Container port" hint="Requests are sent to the container on this port.">
          <input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(Number(event.target.value))} />
        </Field>
        <LabelsEditor
          pairs={env}
          onChange={setEnv}
          idPrefix="run-create-env"
          title="Environment variables"
          addLabel="Add variable"
          entryNoun="Variable"
        />
      </FormSection>
    </CreatePage>
  );
}
