import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  makeStyles,
  tokens,
  Field,
  Select,
  Switch,
  Table,
  TableHeader,
  TableRow,
  TableHeaderCell,
  TableBody,
  TableCell,
  Button,
  Text,
} from "@fluentui/react-components";
import {
  AzureCommandBar,
  AzureConfirmDialog,
  AzureEssentials,
  AzureStatus,
  AzureErrorMessage,
  AzureEmptyState,
  AzureWarningMessage,
  TagsEditor,
} from "../portal/index.js";
import { AzureTableErrorRow, AzureTableLoadingRow, AzureTableEmptyRow } from "../portal/AzureTable.js";
import { resourceGroupOf, locationLabel, tagsSummary } from "../portal/format.js";
import {
  acrRegistrySkus,
  deleteACRRegistry,
  fetchACRRegistry,
  fetchACRRepositories,
  fetchACRTags,
  updateACRRegistry,
  updateResourceTags,
  type ACRRegistryDetail,
  API_VERSION,
} from "../api.js";

const useStyles = makeStyles({
  form: {
    backgroundColor: tokens.colorNeutralBackground1,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: "14px 16px",
    margin: "12px 0",
    display: "flex",
    flexDirection: "column",
    gap: "10px",
    maxWidth: "480px",
  },
  actions: { display: "flex", gap: "8px" },
});

// ACRRegistryUpdateForm is the registry "Update" blade: the SKU and admin-user
// toggle the real Microsoft.ContainerRegistry PATCH edits.
export function ACRRegistryUpdateForm({
  registry,
  busy,
  error,
  onSave,
  onDismiss,
}: {
  registry: ACRRegistryDetail;
  busy: boolean;
  error?: React.ReactNode;
  onSave: (skuName: string, adminUserEnabled: boolean) => void;
  onDismiss: () => void;
}) {
  const styles = useStyles();
  const [skuName, setSkuName] = useState(registry.skuName || acrRegistrySkus[0]);
  const [adminUserEnabled, setAdminUserEnabled] = useState(registry.adminUserEnabled);
  return (
    <form
      className={styles.form}
      data-testid="acr-update-form"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(skuName, adminUserEnabled);
      }}
    >
      <Text as="h2" weight="semibold">
        Update container registry
      </Text>
      <Field label="SKU">
        <Select data-testid="acr-update-sku" value={skuName} onChange={(event) => setSkuName(event.target.value)}>
          {acrRegistrySkus.map((sku) => (
            <option key={sku} value={sku}>
              {sku}
            </option>
          ))}
        </Select>
      </Field>
      <Switch
        data-testid="acr-update-admin"
        label="Admin user"
        checked={adminUserEnabled}
        onChange={(_, data) => setAdminUserEnabled(data.checked)}
      />
      {error ? <AzureErrorMessage testid="acr-update-error">{error}</AzureErrorMessage> : null}
      <div className={styles.actions}>
        <Button type="submit" appearance="primary" data-testid="acr-update-save" disabled={busy}>
          {busy ? "Saving…" : "Save"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// The Container registry blade: the real Microsoft.ContainerRegistry
// Essentials, and its repositories/tags read from the registry's own data
// plane (the ACR REST convenience API, `/acr/v1/_catalog` and
// `/acr/v1/{repo}/_tags`) using admin credentials minted through the real
// `listCredentials` ARM action — the same admin-user flow
// `az acr repository list` and `docker login` use, not a synthetic reader.
export function ACRRegistryDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [selectedRepo, setSelectedRepo] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [panel, setPanel] = useState<"config" | "tags" | null>(null);

  const registry = useQuery({ queryKey: ["acr-registry", name], queryFn: () => fetchACRRegistry(name) });
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["acr-registry", name] });
    void queryClient.invalidateQueries({ queryKey: ["acr-registries"] });
  };
  const remove = useMutation({
    mutationFn: () => deleteACRRegistry(registry.data!.id),
    onSuccess: () => navigate("/ui/acr"),
  });
  const update = useMutation({
    mutationFn: ({ skuName, adminUserEnabled }: { skuName: string; adminUserEnabled: boolean }) =>
      updateACRRegistry(registry.data!.id, { skuName, adminUserEnabled }),
    onSuccess: () => {
      setPanel(null);
      invalidate();
    },
  });
  const saveTags = useMutation({
    mutationFn: (tags: Record<string, string>) =>
      updateResourceTags(registry.data!.id, API_VERSION.registries, tags),
    onSuccess: () => {
      setPanel(null);
      invalidate();
    },
  });
  const repositories = useQuery({
    queryKey: ["acr-repositories", registry.data?.id],
    queryFn: () => fetchACRRepositories(registry.data!),
    enabled: Boolean(registry.data),
  });
  const tags = useQuery({
    queryKey: ["acr-tags", registry.data?.id, selectedRepo],
    queryFn: () => fetchACRTags(registry.data!, selectedRepo!),
    enabled: Boolean(registry.data) && Boolean(selectedRepo),
  });

  return (
    <>
      <AzureCommandBar
        commands={[
          {
            label: "Update",
            icon: "settings",
            testid: "acr-registry-update",
            disabled: !registry.data || update.isPending,
            onSelect: () => setPanel((current) => (current === "config" ? null : "config")),
          },
          {
            label: "Edit tags",
            icon: "tag",
            testid: "acr-registry-tags",
            disabled: !registry.data || saveTags.isPending,
            onSelect: () => setPanel((current) => (current === "tags" ? null : "tags")),
          },
          {
            label: "Delete",
            icon: "delete",
            testid: "acr-registry-delete",
            disabled: !registry.data || remove.isPending,
            onSelect: () => setDeleting(true),
          },
          {
            label: "Refresh",
            icon: "refresh",
            onSelect: () => {
              void registry.refetch();
              void repositories.refetch();
              if (selectedRepo) void tags.refetch();
            },
            disabled: registry.isFetching,
          },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      {registry.data ? (
        <AzureConfirmDialog
          open={deleting}
          title={`Delete ${registry.data.name}?`}
          busy={remove.isPending}
          testid="acr-registry-delete-dialog"
          error={
            remove.isError ? (
              <>
                <strong>Could not delete.</strong>{" "}
                {remove.error instanceof Error ? remove.error.message : "Azure Resource Manager did not respond."}
              </>
            ) : undefined
          }
          onConfirm={() => remove.mutate()}
          onCancel={() => setDeleting(false)}
        >
          <Text as="p">
            Deleting a container registry is permanent and removes every repository and image it holds. This action
            can&rsquo;t be undone.
          </Text>
        </AzureConfirmDialog>
      ) : null}
      <div className="az-main" data-testid="acr-registry-detail">
        {registry.isError ? (
          <AzureErrorMessage testid="acr-registry-error">
            <strong>Could not load this container registry.</strong>{" "}
            {registry.error instanceof Error ? registry.error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : registry.isLoading || !registry.data ? (
          <AzureEmptyState title="Loading the registry…" loading />
        ) : (
          <>
            <AzureEssentials
              properties={[
                { label: "Resource group", value: resourceGroupOf(registry.data.id) },
                { label: "Location", value: locationLabel(registry.data.location) },
                { label: "Login server", value: <code>{registry.data.loginServer || "—"}</code> },
                { label: "SKU", value: registry.data.skuTier || registry.data.skuName || "—" },
                { label: "Admin user", value: registry.data.adminUserEnabled ? "Enabled" : "Disabled" },
                { label: "Provisioning state", value: <AzureStatus status={registry.data.provisioningState || "Unknown"} /> },
                { label: "Tags", value: tagsSummary(registry.data.tags) },
              ]}
            />

            {panel === "config" ? (
              <ACRRegistryUpdateForm
                registry={registry.data}
                busy={update.isPending}
                error={
                  update.isError ? (
                    <>
                      <strong>The registry could not be updated.</strong>{" "}
                      {update.error instanceof Error ? update.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(skuName, adminUserEnabled) => update.mutate({ skuName, adminUserEnabled })}
                onDismiss={() => setPanel(null)}
              />
            ) : null}
            {panel === "tags" ? (
              <TagsEditor
                tags={registry.data.tags}
                busy={saveTags.isPending}
                testidPrefix="acr-registry-tags"
                error={
                  saveTags.isError ? (
                    <>
                      <strong>The tags could not be saved.</strong>{" "}
                      {saveTags.error instanceof Error ? saveTags.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(tags) => saveTags.mutate(tags)}
                onDismiss={() => setPanel(null)}
              />
            ) : null}

            <section className="az-blade-section" aria-label="Repositories">
              <Text as="h2" weight="semibold" block>
                Repositories
              </Text>
              {!registry.data.adminUserEnabled ? (
                <AzureWarningMessage testid="acr-admin-disabled">
                  Enable the admin user on this registry to browse its repositories from this console.
                </AzureWarningMessage>
              ) : (
                <Table aria-label="Repositories" size="small" data-testid="acr-repositories">
                  <TableHeader>
                    <TableRow>
                      <TableHeaderCell>Repository</TableHeaderCell>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {repositories.isError ? (
                      <AzureTableErrorRow colSpan={1}>
                        <strong>Could not reach the registry's repository catalog.</strong>{" "}
                        {repositories.error instanceof Error ? repositories.error.message : "The registry did not respond."}
                      </AzureTableErrorRow>
                    ) : repositories.isLoading ? (
                      <AzureTableLoadingRow colSpan={1} label="Loading repositories…" />
                    ) : (repositories.data ?? []).length === 0 ? (
                      <AzureTableEmptyRow
                        colSpan={1}
                        title="No repositories to display"
                        description="Images pushed to this registry appear here."
                      />
                    ) : (
                      (repositories.data ?? []).map((repo) => (
                        <TableRow key={repo} appearance={selectedRepo === repo ? "neutral" : undefined}>
                          <TableCell>
                            <Button
                              appearance="subtle"
                              data-testid="acr-repository-row"
                              aria-pressed={selectedRepo === repo}
                              onClick={() => setSelectedRepo((current) => (current === repo ? null : repo))}
                            >
                              {repo}
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              )}
            </section>

            {selectedRepo ? (
              <section className="az-blade-section" aria-label={`Tags for ${selectedRepo}`}>
                <Text as="h2" weight="semibold" block>
                  Tags — {selectedRepo}
                </Text>
                <Table aria-label={`Tags for ${selectedRepo}`} size="small" data-testid="acr-tags">
                  <TableHeader>
                    <TableRow>
                      <TableHeaderCell>Tag</TableHeaderCell>
                      <TableHeaderCell>Digest</TableHeaderCell>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {tags.isError ? (
                      <AzureTableErrorRow colSpan={2}>
                        <strong>Could not load tags.</strong>{" "}
                        {tags.error instanceof Error ? tags.error.message : "The registry did not respond."}
                      </AzureTableErrorRow>
                    ) : tags.isLoading ? (
                      <AzureTableLoadingRow colSpan={2} label="Loading tags…" />
                    ) : (tags.data ?? []).length === 0 ? (
                      <AzureTableEmptyRow colSpan={2} title="No tags to display" />
                    ) : (
                      (tags.data ?? []).map((tag) => (
                        <TableRow key={tag.name}>
                          <TableCell>{tag.name}</TableCell>
                          <TableCell>
                            <code>{tag.digest}</code>
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </section>
            ) : null}
          </>
        )}
      </div>
    </>
  );
}
