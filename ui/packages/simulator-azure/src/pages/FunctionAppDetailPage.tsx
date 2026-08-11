import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Table, TableHeader, TableRow, TableHeaderCell, TableBody, TableCell, Text } from "@fluentui/react-components";
import {
  AzureCommandBar,
  AzureConfirmDialog,
  AzureEssentials,
  AzureStatus,
  AzureErrorMessage,
  AzureEmptyState,
  TagsEditor,
} from "../portal/index.js";
import { AzureTableErrorRow, AzureTableLoadingRow, AzureTableEmptyRow } from "../portal/AzureTable.js";
import { resourceGroupOf, locationLabel, tagsSummary } from "../portal/format.js";
import {
  deleteFunctionApp,
  fetchFunctionApp,
  fetchFunctionAppSettings,
  fetchFunctions,
  restartFunctionApp,
  startFunctionApp,
  stopFunctionApp,
  updateFunctionAppTags,
} from "../api.js";

// The Function App blade: the real Microsoft.Web/sites Essentials, the
// site's application settings (read through the real `/list` action, since
// Microsoft.Web serves current app-setting values only that way), and the
// deployed functions — all real Azure Resource Manager reads.
export function FunctionAppDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [editingTags, setEditingTags] = useState(false);

  const site = useQuery({ queryKey: ["fn-site", name], queryFn: () => fetchFunctionApp(name) });
  const siteId = site.data?.id;
  const invalidateSite = () => void queryClient.invalidateQueries({ queryKey: ["fn-site", name] });
  const remove = useMutation({
    mutationFn: () => deleteFunctionApp(siteId!),
    onSuccess: () => navigate("/ui/functions"),
  });
  const start = useMutation({ mutationFn: () => startFunctionApp(siteId!), onSuccess: invalidateSite });
  const stop = useMutation({ mutationFn: () => stopFunctionApp(siteId!), onSuccess: invalidateSite });
  const restart = useMutation({ mutationFn: () => restartFunctionApp(siteId!), onSuccess: invalidateSite });
  const saveTags = useMutation({
    mutationFn: (tags: Record<string, string>) => updateFunctionAppTags(siteId!, tags, site.data!.httpsOnly),
    onSuccess: () => {
      setEditingTags(false);
      invalidateSite();
      void queryClient.invalidateQueries({ queryKey: ["fn-sites"] });
    },
  });
  const lifecycleError = start.error ?? stop.error ?? restart.error;
  const settings = useQuery({
    queryKey: ["fn-site-settings", siteId],
    queryFn: () => fetchFunctionAppSettings(siteId!),
    enabled: Boolean(siteId),
  });
  const functions = useQuery({
    queryKey: ["fn-site-functions", siteId],
    queryFn: () => fetchFunctions(siteId!),
    enabled: Boolean(siteId),
  });

  return (
    <>
      <AzureCommandBar
        commands={[
          {
            label: "Start",
            icon: "play",
            testid: "fn-site-start",
            disabled: !siteId || start.isPending,
            onSelect: () => start.mutate(),
          },
          {
            label: "Stop",
            icon: "stop",
            testid: "fn-site-stop",
            disabled: !siteId || stop.isPending,
            onSelect: () => stop.mutate(),
          },
          {
            label: "Restart",
            icon: "refresh",
            testid: "fn-site-restart",
            disabled: !siteId || restart.isPending,
            onSelect: () => restart.mutate(),
          },
          {
            label: "Edit tags",
            icon: "tag",
            testid: "fn-site-tags",
            disabled: !siteId || saveTags.isPending,
            onSelect: () => setEditingTags((current) => !current),
          },
          {
            label: "Delete",
            icon: "delete",
            testid: "fn-site-delete",
            disabled: !siteId || remove.isPending,
            onSelect: () => setDeleting(true),
          },
          {
            label: "Refresh",
            icon: "refresh",
            testid: "fn-site-refresh",
            onSelect: () => {
              void site.refetch();
              void settings.refetch();
              void functions.refetch();
            },
            disabled: site.isFetching,
          },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      {site.data ? (
        <AzureConfirmDialog
          open={deleting}
          title={`Delete ${site.data.name}?`}
          busy={remove.isPending}
          testid="fn-site-delete-dialog"
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
            Deleting a Function App is permanent and removes every function deployed to it. This action can&rsquo;t
            be undone.
          </Text>
        </AzureConfirmDialog>
      ) : null}
      <div className="az-main" data-testid="fn-site-detail">
        {site.isError ? (
          <AzureErrorMessage testid="fn-site-error">
            <strong>Could not load this Function App.</strong>{" "}
            {site.error instanceof Error ? site.error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : site.isLoading || !site.data ? (
          <AzureEmptyState title="Loading the Function App…" loading />
        ) : (
          <>
            <AzureEssentials
              properties={[
                { label: "Resource group", value: resourceGroupOf(site.data.id) },
                { label: "Location", value: locationLabel(site.data.location) },
                { label: "Kind", value: site.data.kind || "—" },
                { label: "Status", value: <AzureStatus status={site.data.state || (site.data.enabled ? "Running" : "Stopped")} /> },
                { label: "Default domain", value: site.data.defaultHostName ? <code>{site.data.defaultHostName}</code> : "—" },
                { label: "HTTPS only", value: site.data.httpsOnly ? "Enabled" : "Disabled" },
                { label: "Tags", value: tagsSummary(site.data.tags) },
              ]}
            />

            {lifecycleError ? (
              <AzureErrorMessage testid="fn-site-action-error">
                <strong>The Function App operation failed.</strong>{" "}
                {lifecycleError instanceof Error ? lifecycleError.message : "Azure Resource Manager did not respond."}
              </AzureErrorMessage>
            ) : null}
            {editingTags ? (
              <TagsEditor
                tags={site.data.tags}
                busy={saveTags.isPending}
                testidPrefix="fn-site-tags"
                error={
                  saveTags.isError ? (
                    <>
                      <strong>The tags could not be saved.</strong>{" "}
                      {saveTags.error instanceof Error ? saveTags.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(tags) => saveTags.mutate(tags)}
                onDismiss={() => setEditingTags(false)}
              />
            ) : null}

            <section className="az-blade-section" aria-label="Application settings">
              <Text as="h2" weight="semibold" block>
                Application settings
              </Text>
              <Table aria-label="Application settings" size="small" data-testid="fn-site-settings">
                <TableHeader>
                  <TableRow>
                    <TableHeaderCell>Name</TableHeaderCell>
                    <TableHeaderCell>Value</TableHeaderCell>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {settings.isError ? (
                    <AzureTableErrorRow colSpan={2}>
                      <strong>Could not load application settings.</strong>{" "}
                      {settings.error instanceof Error ? settings.error.message : "Azure Resource Manager did not respond."}
                    </AzureTableErrorRow>
                  ) : settings.isLoading ? (
                    <AzureTableLoadingRow colSpan={2} label="Loading application settings…" />
                  ) : Object.keys(settings.data ?? {}).length === 0 ? (
                    <AzureTableEmptyRow colSpan={2} title="No application settings" />
                  ) : (
                    Object.entries(settings.data ?? {}).map(([key, value]) => (
                      <TableRow key={key}>
                        <TableCell>
                          <code>{key}</code>
                        </TableCell>
                        <TableCell>
                          <code>{value}</code>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </section>

            <section className="az-blade-section" aria-label="Functions">
              <Text as="h2" weight="semibold" block>
                Functions
              </Text>
              <Table aria-label="Functions" size="small" data-testid="fn-site-functions">
                <TableHeader>
                  <TableRow>
                    <TableHeaderCell>Name</TableHeaderCell>
                    <TableHeaderCell>Language</TableHeaderCell>
                    <TableHeaderCell>Status</TableHeaderCell>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {functions.isError ? (
                    <AzureTableErrorRow colSpan={3}>
                      <strong>Could not load functions.</strong>{" "}
                      {functions.error instanceof Error ? functions.error.message : "Azure Resource Manager did not respond."}
                    </AzureTableErrorRow>
                  ) : functions.isLoading ? (
                    <AzureTableLoadingRow colSpan={3} label="Loading functions…" />
                  ) : (functions.data ?? []).length === 0 ? (
                    <AzureTableEmptyRow
                      colSpan={3}
                      title="No functions to display"
                      description="Functions deployed to this app appear here."
                    />
                  ) : (
                    (functions.data ?? []).map((fn) => (
                      <TableRow key={fn.name}>
                        <TableCell>{fn.name}</TableCell>
                        <TableCell>{fn.language || "—"}</TableCell>
                        <TableCell>
                          <AzureStatus status={fn.isDisabled ? "Disabled" : "Enabled"} />
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </section>
          </>
        )}
      </div>
    </>
  );
}
