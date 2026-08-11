import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  createDnsZone,
  deleteDnsZone,
  fetchDnsRecordSets,
  fetchDnsZone,
  fetchDnsZones,
  type DnsManagedZone,
  type DnsResourceRecordSet,
} from "../api.js";
import { useProject } from "../console/project.js";

// Cloud DNS's resource-name contract for a managed zone: up to 63 characters
// of lowercase letters, digits and hyphens, starting with a letter.
const ZONE_NAME_PATTERN = /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

const columns: GcpColumn<DnsManagedZone>[] = [
  {
    id: "name",
    header: "Zone name",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/dns/${row.name}`}>
        {row.name}
      </Link>
    ),
    value: (row) => row.name,
  },
  { id: "dnsName", header: "DNS name", cell: (row) => row.dnsName ?? "—", value: (row) => row.dnsName ?? "" },
  { id: "visibility", header: "Zone type", cell: (row) => row.visibility ?? "—", value: (row) => row.visibility ?? "" },
  {
    id: "description",
    header: "Description",
    cell: (row) => row.description || "—",
    value: (row) => row.description ?? "",
  },
];

// CreateZoneDialog creates a zone through the real dns.managedZones.create
// method, which — unlike the long-running services — answers synchronously
// with the created ManagedZone.
export function CreateZoneDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [name, setName] = useState("");
  const [dnsName, setDnsName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState("public");

  const create = useMutation({
    mutationFn: () => createDnsZone(project, { name, dnsName, description, visibility }),
    onSuccess: onCreated,
  });

  // Cloud DNS requires a fully-qualified DNS name, which ends in a dot.
  const valid = ZONE_NAME_PATTERN.test(name) && /^[a-z0-9.-]+\.$/i.test(dnsName);

  return (
    <GcpDialog title="Create a DNS zone" testId="dns-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Zone name
        <input type="text" value={name} data-testid="dns-create-name" onChange={(event) => setName(event.target.value)} />
        <p className="gc-field-hint">Up to 63 lowercase letters, numbers or hyphens; must start with a letter.</p>
      </label>
      <label className="gc-field">
        DNS name
        <input
          type="text"
          value={dnsName}
          data-testid="dns-create-dnsname"
          onChange={(event) => setDnsName(event.target.value)}
        />
        <p className="gc-field-hint">A fully-qualified domain name ending in a dot, e.g. example.com.</p>
      </label>
      <label className="gc-field">
        Zone type
        <select
          value={visibility}
          data-testid="dns-create-visibility"
          onChange={(event) => setVisibility(event.target.value)}
        >
          <option value="public">Public</option>
          <option value="private">Private</option>
        </select>
      </label>
      <label className="gc-field">
        Description
        <input
          type="text"
          value={description}
          data-testid="dns-create-description"
          onChange={(event) => setDescription(event.target.value)}
        />
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the zone.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="dns-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteZoneDialog({
  name,
  onClose,
  onDeleted,
}: {
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({ mutationFn: () => deleteDnsZone(project, name), onSuccess: onDeleted });
  return (
    <GcpDialog title="Delete DNS zone?" testId="dns-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> permanently removes the zone and its record sets. This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the zone.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="dns-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudDnsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["dns-zones", project] });

  const columnsWithActions: GcpColumn<DnsManagedZone>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => (
        <span className="gc-row-actions">
          <button
            type="button"
            className="gc-button-text"
            data-testid={`dns-delete-${row.name}`}
            aria-label={`Delete ${row.name}`}
            onClick={() => setDeleting(row.name)}
          >
            Delete
          </button>
        </span>
      ),
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<DnsManagedZone>
        title="Cloud DNS"
        description="Cloud DNS is a scalable, reliable and managed authoritative Domain Name System service."
        actions={[
          { label: "Create zone", icon: "add", primary: true, testId: "dns-create-zone", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["dns-zones", project]}
        queryFn={() => fetchDnsZones(project)}
        filterPlaceholder="Filter zones"
        resourceNoun="zones"
        empty={{
          headline: "Create a zone to publish your DNS records",
          description: "A managed zone holds the record sets for one DNS name.",
          primaryLabel: "Create zone",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateZoneDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteZoneDialog
          name={deleting}
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

export function CloudDnsZoneDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const zone = useQuery({ queryKey: ["dns-zone", project, name], queryFn: () => fetchDnsZone(project, name) });
  const records = useQuery({
    queryKey: ["dns-rrsets", project, name],
    queryFn: () => fetchDnsRecordSets(project, name),
  });

  const data = zone.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/dns">‹ Cloud DNS</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Cloud DNS managed zone"
        actions={[{ label: "Delete", testId: "dns-zone-delete", onSelect: () => setDeleting(true) }]}
        onRefresh={() => {
          void zone.refetch();
          void records.refetch();
        }}
        refreshing={zone.isFetching || records.isFetching}
      />
      {zone.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this zone.</strong>{" "}
          {zone.error instanceof Error ? zone.error.message : "The simulator did not respond."}
        </div>
      ) : zone.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading zone…</div>
      ) : (
        <GcpTabs
          label="Zone detail"
          tabs={[
            {
              id: "records",
              label: "Record sets",
              content: (
                <SubResourceTable<DnsResourceRecordSet>
                  query={records}
                  testId="dns-rrsets-table"
                  noun="record sets"
                  emptyHeadline="This zone has no record sets"
                  emptyDescription="Record sets added to this zone appear here."
                  rowKey={(row) => `${row.name}/${row.type}`}
                  columns={[
                    { header: "DNS name", cell: (row) => row.name },
                    { header: "Type", cell: (row) => row.type },
                    { header: "TTL (seconds)", cell: (row) => row.ttl ?? "—" },
                    { header: "Data", cell: (row) => (row.rrdatas ?? []).join(", ") || "—" },
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
                    { label: "DNS name", value: data.dnsName ?? "—" },
                    { label: "Zone type", value: data.visibility ?? "—" },
                    { label: "Description", value: data.description || "—" },
                    { label: "Zone ID", value: data.id ?? "—" },
                    { label: "Created", value: formatTimestamp(data.creationTime ?? "") },
                    { label: "Name servers", value: (data.nameServers ?? []).join(", ") || "—" },
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
        <DeleteZoneDialog name={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/dns")} />
      ) : null}
    </>
  );
}
