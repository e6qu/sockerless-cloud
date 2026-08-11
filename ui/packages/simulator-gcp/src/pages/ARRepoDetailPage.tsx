import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp, splitImageDigest } from "../console/format.js";
import { fetchARRepo, fetchARImages, type ARDockerImage } from "../api.js";
import { DeleteRepoDialog, EditRepoDialog } from "./ArtifactRegistryPage.js";
import { useProject } from "../console/project.js";

// ImagesTable is the Images tab's table, presentational so the digest/tag
// rendering is testable apart from the queries that feed it.
export function ImagesTable({ images }: { images: ARDockerImage[] }) {
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="ar-images-table">
        <thead>
          <tr>
            <th>Image</th>
            <th>Digest</th>
            <th>Tags</th>
            <th>Uploaded</th>
          </tr>
        </thead>
        <tbody>
          {images.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">No images</p>
                  <p className="gc-empty-description">
                    Images pushed to this repository appear here.
                  </p>
                </div>
              </td>
            </tr>
          ) : (
            images.map((image) => {
              const { path, digest } = splitImageDigest(image.name);
              return (
                <tr key={image.name}>
                  <td>{path}</td>
                  <td><code className="gc-code-inline">{digest.slice(0, 19)}…</code></td>
                  <td>{image.tags && image.tags.length > 0 ? image.tags.join(", ") : "—"}</td>
                  <td>{formatTimestamp(image.uploadTime ?? "")}</td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

export function ARRepoDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(false);
  const repo = useQuery({ queryKey: ["ar-repo", project, name], queryFn: () => fetchARRepo(project, name) });
  const images = useQuery({
    queryKey: ["ar-repo-images", project, name],
    queryFn: () => fetchARImages(project, name),
    enabled: Boolean(repo.data),
  });

  // Deletion addresses the repository by the id in the route, not the loaded
  // resource, so the action is available (and testable) even before the read
  // settles — the same way the list page's "Create repository" action
  // doesn't wait on a successful list read. Editing prefills the form from the
  // loaded resource, so its action appears once the read succeeds.
  const deleteAction = [{ label: "Delete", testId: "ar-repo-delete", onSelect: () => setDeleting(true) }];
  const headerActions = repo.data
    ? [{ label: "Edit", icon: "edit" as const, testId: "ar-repo-edit", onSelect: () => setEditing(true) }, ...deleteAction]
    : deleteAction;

  if (repo.isError) {
    return (
      <>
        <GcpPageHeader title={shortName(name)} description="Artifact Registry repository" actions={deleteAction} />
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this repository.</strong>{" "}
          {repo.error instanceof Error ? repo.error.message : "The simulator did not respond."}
        </div>
        {deleting ? (
          <DeleteRepoDialog
            project={project}
            repositoryId={name}
            onClose={() => setDeleting(false)}
            onDeleted={() => navigate("/ui/ar")}
          />
        ) : null}
      </>
    );
  }

  const data = repo.data;
  const labels = Object.entries(data?.labels ?? {});

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/ar">‹ Artifact Registry</Link>
      </div>
      <GcpPageHeader
        title={shortName(name)}
        description="Artifact Registry repository"
        actions={headerActions}
        onRefresh={() => { void repo.refetch(); void images.refetch(); }}
        refreshing={repo.isFetching || images.isFetching}
      />

      {repo.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading repository…</div>
      ) : (
        <GcpTabs
          label="Repository detail"
          tabs={[
            {
              id: "images",
              label: "Images",
              content: images.isError ? (
                <div className="gc-message gc-message-error" role="alert">
                  <strong>Couldn't load images.</strong>{" "}
                  {images.error instanceof Error ? images.error.message : "The simulator did not respond."}
                </div>
              ) : images.isLoading ? (
                <div className="gc-loading" role="status">Loading images…</div>
              ) : (
                <ImagesTable images={images.data ?? []} />
              ),
            },
            {
              id: "details",
              label: "Details",
              content: (
                <>
                  <dl className="gc-detail-grid">
                    {[
                      { label: "Format", value: data.format ?? "—" },
                      { label: "Mode", value: data.mode ?? "Standard" },
                      { label: "Location", value: "us-central1" },
                      { label: "Created", value: formatTimestamp(data.createTime ?? "") },
                      { label: "Updated", value: formatTimestamp(data.updateTime ?? "") },
                      { label: "Description", value: data.description ?? "—" },
                    ].map((property) => (
                      <div className="gc-detail-pair" key={property.label}>
                        <dt>{property.label}</dt>
                        <dd>{property.value}</dd>
                      </div>
                    ))}
                  </dl>
                  {labels.length > 0 ? (
                    <>
                      <h2 className="gc-detail-heading">Labels</h2>
                      <dl className="gc-detail-grid">
                        {labels.map(([key, value]) => (
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
        <DeleteRepoDialog
          project={project}
          repositoryId={name}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate("/ui/ar")}
        />
      ) : null}
      {editing && data ? (
        <EditRepoDialog
          project={project}
          repo={data}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void repo.refetch();
          }}
        />
      ) : null}
    </>
  );
}
