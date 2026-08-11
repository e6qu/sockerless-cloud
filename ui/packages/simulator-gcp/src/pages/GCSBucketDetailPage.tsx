import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp, formatBytes } from "../console/format.js";
import { fetchGCSBucket, fetchGCSObjects, type GCSObject } from "../api.js";
import { DeleteBucketDialog, EditBucketDialog } from "./GCSBucketsPage.js";

// ObjectsTable is the Objects tab's table, presentational so the size/type
// rendering is testable apart from the queries that feed it.
export function ObjectsTable({ objects }: { objects: GCSObject[] }) {
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="gcs-objects-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Size</th>
            <th>Type</th>
            <th>Storage class</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {objects.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={5}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">This bucket has no objects</p>
                  <p className="gc-empty-description">
                    Objects uploaded to this bucket appear here.
                  </p>
                </div>
              </td>
            </tr>
          ) : (
            objects.map((object) => (
              <tr key={object.name}>
                <td>{object.name}</td>
                <td>{formatBytes(object.size)}</td>
                <td>{object.contentType ?? "—"}</td>
                <td>{object.storageClass ?? "—"}</td>
                <td>{formatTimestamp(object.timeCreated ?? "")}</td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

export function GCSBucketDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(false);
  const bucket = useQuery({ queryKey: ["gcs-bucket", name], queryFn: () => fetchGCSBucket(name) });
  const objects = useQuery({
    queryKey: ["gcs-bucket-objects", name],
    queryFn: () => fetchGCSObjects(name),
    enabled: Boolean(bucket.data),
  });

  // Deletion addresses the bucket by the name in the route, not the loaded
  // resource, so the action is available (and testable) even before the read
  // settles — the same way the list page's "Create bucket" action doesn't
  // wait on a successful list read. Editing prefills the form from the loaded
  // resource, so its action appears once the read succeeds.
  const deleteAction = [{ label: "Delete", testId: "gcs-bucket-delete", onSelect: () => setDeleting(true) }];
  const headerActions = bucket.data
    ? [{ label: "Edit", icon: "edit" as const, testId: "gcs-bucket-edit", onSelect: () => setEditing(true) }, ...deleteAction]
    : deleteAction;

  if (bucket.isError) {
    return (
      <>
        <GcpPageHeader title={shortName(name)} description="Cloud Storage bucket" actions={deleteAction} />
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this bucket.</strong>{" "}
          {bucket.error instanceof Error ? bucket.error.message : "The simulator did not respond."}
        </div>
        {deleting ? (
          <DeleteBucketDialog name={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/gcs")} />
        ) : null}
      </>
    );
  }

  const data = bucket.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/gcs">‹ Cloud Storage</Link>
      </div>
      <GcpPageHeader
        title={shortName(name)}
        description="Cloud Storage bucket"
        actions={headerActions}
        onRefresh={() => { void bucket.refetch(); void objects.refetch(); }}
        refreshing={bucket.isFetching || objects.isFetching}
      />

      {bucket.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading bucket…</div>
      ) : (
        <GcpTabs
          label="Bucket detail"
          tabs={[
            {
              id: "objects",
              label: "Objects",
              content: objects.isError ? (
                <div className="gc-message gc-message-error" role="alert">
                  <strong>Couldn't load objects.</strong>{" "}
                  {objects.error instanceof Error ? objects.error.message : "The simulator did not respond."}
                </div>
              ) : objects.isLoading ? (
                <div className="gc-loading" role="status">Loading objects…</div>
              ) : (
                <ObjectsTable objects={objects.data ?? []} />
              ),
            },
            {
              id: "details",
              label: "Details",
              content: (
                <>
                  <dl className="gc-detail-grid">
                    {[
                      { label: "Location", value: data.location ?? "—" },
                      { label: "Storage class", value: data.storageClass ?? "—" },
                      { label: "Created", value: formatTimestamp(data.timeCreated ?? "") },
                      { label: "Updated", value: formatTimestamp(data.updated ?? "") },
                      { label: "Bucket ID", value: data.id ?? "—" },
                    ].map((property) => (
                      <div className="gc-detail-pair" key={property.label}>
                        <dt>{property.label}</dt>
                        <dd>{property.value}</dd>
                      </div>
                    ))}
                  </dl>
                  {Object.keys(data.labels ?? {}).length > 0 ? (
                    <>
                      <h2 className="gc-detail-heading">Labels</h2>
                      <dl className="gc-detail-grid" data-testid="gcs-bucket-labels">
                        {Object.entries(data.labels ?? {}).map(([key, value]) => (
                          <div className="gc-detail-pair" key={key}>
                            <dt>{key}</dt>
                            <dd>{value}</dd>
                          </div>
                        ))}
                      </dl>
                    </>
                  ) : null}
                </>
              ),
            },
          ]}
        />
      )}
      {deleting ? (
        <DeleteBucketDialog name={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/gcs")} />
      ) : null}
      {editing && data ? (
        <EditBucketDialog
          bucket={data}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void bucket.refetch();
          }}
        />
      ) : null}
    </>
  );
}
