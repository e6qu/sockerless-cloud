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
  deleteArmResource,
  fetchArmChildren,
  fetchArmResourceByName,
  fetchResourceGroup,
  fetchResourceGroupResources,
  postArmAction,
  updateResourceTags,
  type ArmGenericResource,
  type ResourceTags,
} from "../api.js";
import type { BladeSubResource, ServiceBlade } from "../services.js";

// The resource blade every service in the catalog opens a resource on: the
// resource's real Azure Resource Manager Get, its Essentials read straight off
// the properties the provider's REST specification documents, the real
// sub-resource List operations ARM nests under it, the lifecycle action POSTs
// ARM models for it, the tags PATCH, and the DELETE — all named by the
// service's own descriptor (services.ts).

// readResource issues the blade's real Get. Resource groups are ARM's one
// non-provider resource, so the descriptor leaves `provider` empty and this
// reads the resource-group path instead.
function readResource(blade: ServiceBlade, name: string): Promise<ArmGenericResource> {
  return blade.provider ? fetchArmResourceByName(blade.provider, blade.apiVersion, name) : fetchResourceGroup(name);
}

// readChildren issues one real sub-resource List. A resource group's contents
// come from Resources_ListByResourceGroup, which ARM serves at the group's own
// `/resources` path rather than under a provider.
function readChildren(
  blade: ServiceBlade,
  sub: BladeSubResource,
  resource: ArmGenericResource,
): Promise<ArmGenericResource[]> {
  if (sub.inline) return Promise.resolve(sub.inline(resource));
  if (!blade.provider && sub.path === "resources") return fetchResourceGroupResources(resource.id);
  return fetchArmChildren(resource.id, sub.path!, sub.apiVersion ?? blade.apiVersion);
}

function SubResourceTable({
  blade,
  sub,
  resource,
}: {
  blade: ServiceBlade;
  sub: BladeSubResource;
  resource: ArmGenericResource;
}) {
  const children = useQuery({
    queryKey: [blade.slug, "children", resource.id, sub.title],
    queryFn: () => readChildren(blade, sub, resource),
  });
  const colSpan = sub.columns.length;
  const testid = `${blade.testid}-${sub.title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "")}`;

  return (
    <section className="az-blade-section" aria-label={sub.title}>
      <Text as="h2" weight="semibold" block>
        {sub.title}
      </Text>
      <Table aria-label={sub.title} size="small" data-testid={testid}>
        <TableHeader>
          <TableRow>
            {sub.columns.map((column) => (
              <TableHeaderCell key={column.id}>{column.header}</TableHeaderCell>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {children.isError ? (
            <AzureTableErrorRow colSpan={colSpan} testid={`${testid}-error`}>
              <strong>Could not load {sub.title.toLowerCase()}.</strong>{" "}
              {children.error instanceof Error ? children.error.message : "Azure Resource Manager did not respond."}
            </AzureTableErrorRow>
          ) : children.isLoading ? (
            <AzureTableLoadingRow colSpan={colSpan} label={`Loading ${sub.title.toLowerCase()}…`} />
          ) : (children.data ?? []).length === 0 ? (
            <AzureTableEmptyRow colSpan={colSpan} title={sub.emptyTitle} description={sub.emptyDescription} />
          ) : (
            (children.data ?? []).map((child) => (
              <TableRow key={child.id || child.name}>
                {sub.columns.map((column) => (
                  <TableCell key={column.id}>{column.read(child) || "—"}</TableCell>
                ))}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </section>
  );
}

export function ServiceDetailPage({ blade }: { blade: ServiceBlade }) {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [editingTags, setEditingTags] = useState(false);

  const resource = useQuery({
    queryKey: [blade.slug, "detail", name],
    queryFn: () => readResource(blade, name),
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: [blade.slug, "detail", name] });
    void queryClient.invalidateQueries({ queryKey: [blade.slug] });
  };

  const remove = useMutation({
    mutationFn: () => deleteArmResource(resource.data!.id, blade.apiVersion),
    onSuccess: () => navigate(`/ui/${blade.slug}`),
  });

  const saveTags = useMutation({
    mutationFn: (tags: ResourceTags) => updateResourceTags(resource.data!.id, blade.apiVersion, tags),
    onSuccess: () => {
      setEditingTags(false);
      invalidate();
    },
  });

  const runAction = useMutation({
    mutationFn: (action: string) => postArmAction(resource.data!.id, action, blade.apiVersion),
    onSuccess: invalidate,
  });

  const loaded = resource.data;
  const busy = remove.isPending || saveTags.isPending || runAction.isPending;

  return (
    <>
      <AzureCommandBar
        commands={[
          ...(blade.actions ?? []).map((action) => ({
            label: action.label,
            icon: action.icon,
            testid: `${blade.testid}-${action.action.toLowerCase()}`,
            disabled: !loaded || busy,
            onSelect: () => runAction.mutate(action.action),
          })),
          {
            label: "Edit tags",
            icon: "tag",
            testid: `${blade.testid}-tags`,
            disabled: !loaded || busy,
            onSelect: () => setEditingTags((open) => !open),
          },
          {
            label: "Delete",
            icon: "delete",
            testid: `${blade.testid}-detail-delete`,
            disabled: !loaded || busy,
            onSelect: () => setDeleting(true),
          },
          { label: "Refresh", icon: "refresh", onSelect: () => void resource.refetch(), disabled: resource.isFetching },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      {loaded ? (
        <AzureConfirmDialog
          open={deleting}
          title={`Delete ${loaded.name}?`}
          busy={remove.isPending}
          testid={`${blade.testid}-detail-delete-dialog`}
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
          <Text as="p">{blade.deleteWarning}</Text>
        </AzureConfirmDialog>
      ) : null}
      <div className="az-main" data-testid={`${blade.testid}-detail`}>
        {resource.isError ? (
          <AzureErrorMessage testid={`${blade.testid}-detail-error`}>
            <strong>Could not load this {blade.detailKind.toLowerCase()}.</strong>{" "}
            {resource.error instanceof Error ? resource.error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : resource.isLoading || !loaded ? (
          <AzureEmptyState title={`Loading the ${blade.detailKind.toLowerCase()}…`} loading />
        ) : (
          <>
            <AzureEssentials
              properties={[
                { label: "Resource group", value: resourceGroupOf(loaded.id) },
                ...blade.essentials.map((entry) => ({
                  label: entry.header,
                  value:
                    entry.id === "provisioningState" || entry.id === "state" || entry.id === "status"
                      ? <AzureStatus status={entry.read(loaded) || "Unknown"} />
                      : entry.id === "location"
                        ? locationLabel(entry.read(loaded))
                        : entry.read(loaded) || "—",
                })),
                { label: "Tags", value: tagsSummary(loaded.tags) },
              ].filter((entry, index, entries) => entries.findIndex((other) => other.label === entry.label) === index)}
            />
            {runAction.isError ? (
              <AzureErrorMessage testid={`${blade.testid}-action-error`}>
                <strong>The action could not be completed.</strong>{" "}
                {runAction.error instanceof Error ? runAction.error.message : "Azure Resource Manager did not respond."}
              </AzureErrorMessage>
            ) : null}
            {editingTags ? (
              <TagsEditor
                tags={loaded.tags}
                busy={saveTags.isPending}
                testidPrefix={`${blade.testid}-tags`}
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
            {(blade.subResources ?? []).map((sub) => (
              <SubResourceTable key={sub.title} blade={blade} sub={sub} resource={loaded} />
            ))}
          </>
        )}
      </div>
    </>
  );
}
