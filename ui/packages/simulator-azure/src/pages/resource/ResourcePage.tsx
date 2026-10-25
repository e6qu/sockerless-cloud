import { useEffect, useState, type ReactNode } from "react";
import { Link as RouterLink, useNavigate, useParams } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Dialog, DialogActions, DialogBody, DialogContent, DialogSurface, DialogTitle, Text } from "@fluentui/react-components";
import type { ArmGenericResource } from "../../api.js";
import { apiVersionFor, deleteResource, getRawResource, getResource, listSubscriptions, parseResourceId, resourceAction } from "../../azure.js";
import { serviceById, SERVICE_BY_ID } from "../../catalog.js";
import { bladeForResource, browseRoute, RESOURCE_GROUP_TYPE, type BladeSubResource, type ServiceBlade } from "../../services.js";
import {
  Blade,
  CommandBar,
  ConfirmDialog,
  EmptyState,
  ErrorBar,
  Essentials,
  errorText,
  ServiceMenu,
  StatusBadge,
  type EssentialsProperty,
  type MenuGroup,
  type MenuItem,
  type PortalCommand,
} from "../../portal/AzurePortal.js";
import { useNotifications } from "../../portal/notifications.js";
import { recordResourceView } from "../../portal/preferences.js";
import { resourceRoute, splitResourcePath } from "../../portal/routes.js";
import { ActivityLogSection, ChildrenSection, IamSection, PropertiesSection, ResourceListSection, TagsSection } from "./sections.js";
import { ContainersSection, CorsSection } from "./storage.js";

export function sectionSlug(title: string): string {
  return title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "");
}

const STATE_IDS = new Set(["provisioningState", "state", "status"]);

function JsonViewDialog({ id, apiVersion, open, onClose }: { id: string; apiVersion: string; open: boolean; onClose: () => void }) {
  const raw = useQuery({ queryKey: ["raw", id, apiVersion], queryFn: () => getRawResource(id, apiVersion), enabled: open });
  return (
    <Dialog open={open} onOpenChange={(_, data) => !data.open && onClose()}>
      <DialogSurface className="az-json-surface">
        <DialogBody>
          <DialogTitle>Resource JSON</DialogTitle>
          <DialogContent className="az-dialog-content">
            <Text size={200} block>
              Resource ID: <code>{id}</code> · API version {apiVersion}
            </Text>
            {raw.isError ? <ErrorBar title="The resource could not be read.">{errorText(raw.error)}</ErrorBar> : null}
            <pre className="az-json" tabIndex={0} aria-label="Resource JSON">
              {raw.data ? JSON.stringify(raw.data, null, 2) : "Loading…"}
            </pre>
          </DialogContent>
          <DialogActions>
            <Button onClick={onClose}>Close</Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

interface SectionDef {
  slug: string;
  label: string;
  group?: string;
  render: () => ReactNode;
}

function OverviewSection({
  resource,
  blade,
  apiVersion,
  onDeleted,
}: {
  resource: ArmGenericResource;
  blade?: ServiceBlade;
  apiVersion: string;
  onDeleted: () => void;
}) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { track } = useNotifications();
  const parsed = parseResourceId(resource.id);
  const isGroup = parsed.type === RESOURCE_GROUP_TYPE;
  const [deleting, setDeleting] = useState(false);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [json, setJson] = useState(false);
  const [actionError, setActionError] = useState<string>();
  const [busyAction, setBusyAction] = useState<string>();
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  const subscription = subscriptions.data?.find((sub) => sub.subscriptionId === parsed.subscriptionId);
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["resource", resource.id] });

  const runAction = async (label: string, action: string) => {
    setBusyAction(action);
    setActionError(undefined);
    try {
      await track(
        { progress: `${label} ${resource.name}`, success: `${label} ${resource.name} succeeded`, failure: `${label} ${resource.name} failed`, description: resource.id },
        () => resourceAction(resource.id, action, apiVersion),
      );
      refresh();
    } catch (error) {
      setActionError(errorText(error));
    } finally {
      setBusyAction(undefined);
    }
  };

  const confirmDelete = async () => {
    setDeleteBusy(true);
    setDeleteError(undefined);
    try {
      await track(
        { progress: `Deleting ${resource.name}`, success: `Deleted ${resource.name}`, failure: `Failed to delete ${resource.name}`, description: resource.id },
        () => deleteResource(resource.id, resource.kind),
      );
      setDeleting(false);
      onDeleted();
    } catch (error) {
      setDeleteError(errorText(error));
    } finally {
      setDeleteBusy(false);
    }
  };

  const commands: PortalCommand[] = [
    ...(isGroup ? [{ label: "Create", icon: "add" as const, onSelect: () => navigate("/ui/create") }] : []),
    ...(blade?.actions ?? []).map((action) => ({
      label: action.label,
      icon: action.icon,
      disabled: busyAction !== undefined,
      testid: `action-${action.action.toLowerCase()}`,
      onSelect: () => void runAction(action.label, action.action),
    })),
    { label: isGroup ? "Delete resource group" : "Delete", icon: "delete", onSelect: () => setDeleting(true), testid: "resource-delete" },
    { label: "Refresh", icon: "refresh", onSelect: refresh, testid: "resource-refresh" },
  ];

  const value = (entry: ServiceBlade["essentials"][number]) => {
    const text = entry.read(resource);
    return STATE_IDS.has(entry.id) && text ? <StatusBadge status={text} /> : text;
  };
  const left: EssentialsProperty[] = [
    ...(isGroup ? [] : [{ label: "Resource group", value: <RouterLink to={resourceRoute(`/subscriptions/${parsed.subscriptionId}/resourceGroups/${parsed.resourceGroup}`)}>{parsed.resourceGroup}</RouterLink> }]),
    { label: "Location", value: resource.location },
    { label: "Subscription", value: <RouterLink to={resourceRoute(`/subscriptions/${parsed.subscriptionId}`)}>{subscription?.displayName ?? parsed.subscriptionId}</RouterLink> },
    { label: "Subscription ID", value: parsed.subscriptionId },
    {
      label: "Tags",
      value: (
        <span className="az-essentials-tags">
          {Object.entries(resource.tags).map(([key, tag]) => (
            <span key={key} className="az-tag-chip">
              {tag ? `${key} : ${tag}` : key}
            </span>
          ))}
          <RouterLink to={resourceRoute(resource.id, "tags")}>{Object.keys(resource.tags).length ? "Edit" : "Add tags"}</RouterLink>
        </span>
      ),
    },
  ];
  const right: EssentialsProperty[] = (blade?.essentials ?? []).filter((entry) => entry.id !== "location" && entry.id !== "id").map((entry) => ({ label: entry.header, value: value(entry) }));

  return (
    <>
      <CommandBar commands={commands} />
      {actionError ? <ErrorBar title="The action could not be completed.">{actionError}</ErrorBar> : null}
      <Essentials left={left} right={right} onJsonView={() => setJson(true)} />
      <JsonViewDialog id={resource.id} apiVersion={apiVersion} open={json} onClose={() => setJson(false)} />
      {isGroup ? <ResourceListSection groupId={resource.id} /> : null}
      <ConfirmDialog
        open={deleting}
        title={isGroup ? `Delete a resource group` : `Delete ${resource.name}`}
        busy={deleteBusy}
        testid="resource-delete-dialog"
        error={deleteError}
        confirmText={isGroup ? resource.name : undefined}
        onConfirm={() => void confirmDelete()}
        onCancel={() => {
          setDeleting(false);
          setDeleteError(undefined);
        }}
      >
        <Text block>{blade?.deleteWarning ?? "Deleting this resource is permanent. This action can't be undone."}</Text>
      </ConfirmDialog>
    </>
  );
}

function childSections(blade: ServiceBlade, resource: ArmGenericResource, apiVersion: string): SectionDef[] {
  return (blade.subResources ?? []).map((sub: BladeSubResource) => ({
    slug: sectionSlug(sub.title),
    label: sub.title,
    group: sub.group ?? "Settings",
    render: () => <ChildrenSection key={sub.title} sub={sub} resource={resource} apiVersion={sub.apiVersion ?? apiVersion} />,
  }));
}

function ArmResourceBlade({ id, section }: { id: string; section: string[] }) {
  const navigate = useNavigate();
  const parsed = parseResourceId(id);
  const version = useQuery({ queryKey: ["api-version", parsed.type, parsed.subscriptionId], queryFn: () => apiVersionFor(id) });
  const resource = useQuery({
    queryKey: ["resource", id, version.data],
    queryFn: () => getResource(id, version.data!),
    enabled: Boolean(version.data),
  });
  const blade = bladeForResource(parsed.type, resource.data?.kind ?? "");
  const service = blade ? SERVICE_BY_ID.get(blade.slug) : undefined;
  useEffect(() => {
    if (resource.data) recordResourceView({ id: resource.data.id || id, name: resource.data.name, type: resource.data.type || parsed.type });
  }, [resource.data, id, parsed.type]);

  const kind = blade?.kind ?? parsed.type;
  const trail = [
    { label: "Home", to: "/ui/home" },
    ...(blade ? [{ label: blade.label, to: browseRoute(blade) }] : []),
    { label: parsed.name },
  ];
  const sectionName = section[0] ?? "overview";
  const base = (slug: string) => resourceRoute(id, slug);

  if (version.isError || resource.isError) {
    return (
      <Blade title={parsed.name} subtitle={kind} trail={trail}>
        <ErrorBar title={`The ${kind.toLowerCase()} could not be loaded.`} testid="resource-error">
          {errorText(version.error ?? resource.error)}
        </ErrorBar>
      </Blade>
    );
  }
  if (!resource.data || !version.data) {
    return (
      <Blade title={parsed.name} subtitle={kind} trail={trail}>
        <EmptyState title={`Loading ${parsed.name}…`} loading />
      </Blade>
    );
  }
  const loaded = resource.data;
  const apiVersion = version.data;
  const isStorage = parsed.type.toLowerCase() === "microsoft.storage/storageaccounts";
  const sections: SectionDef[] = [
    { slug: "overview", label: "Overview", render: () => <OverviewSection resource={loaded} blade={blade} apiVersion={apiVersion} onDeleted={() => navigate(blade ? browseRoute(blade) : "/ui/browse/all")} /> },
    { slug: "activitylog", label: "Activity log", render: () => <ActivityLogSection scope={loaded.id} /> },
    { slug: "users", label: "Access control (IAM)", render: () => <IamSection scope={loaded.id} /> },
    { slug: "tags", label: "Tags", render: () => <TagsSection scope={loaded.id} /> },
    ...(isStorage
      ? [
          { slug: "containers", label: "Containers", group: "Data storage", render: () => <ContainersSection account={loaded} path={section.slice(1)} /> },
          { slug: "cors", label: "Resource sharing (CORS)", group: "Settings", render: () => <CorsSection account={loaded} /> },
        ]
      : []),
    ...(blade ? childSections(blade, loaded, apiVersion) : []),
    { slug: "properties", label: "Properties", group: "Settings", render: () => <PropertiesSection resource={loaded} apiVersion={apiVersion} /> },
  ];
  const top: MenuItem[] = sections.filter((entry) => !entry.group).map((entry) => ({ label: entry.label, to: base(entry.slug), icon: iconFor(entry.slug) }));
  const groupNames = Array.from(new Set(sections.filter((entry) => entry.group).map((entry) => entry.group!)));
  const groups: MenuGroup[] = groupNames.map((group) => ({
    label: group,
    items: sections.filter((entry) => entry.group === group).map((entry) => ({ label: entry.label, to: base(entry.slug), icon: iconFor(entry.slug) })),
  }));
  const current = sections.find((entry) => entry.slug === sectionName) ?? sections[0];

  return (
    <Blade
      title={current.slug === "overview" ? loaded.name : `${loaded.name} | ${current.label}`}
      subtitle={kind}
      icon={service ? { name: service.icon, color: service.color } : { name: "resource", color: "#0f6cbd" }}
      trail={trail}
      testid="resource-blade"
    >
      <div className="az-resource-layout">
        <ServiceMenu label={`${loaded.name} menu`} top={top} groups={groups} />
        <div className="az-resource-content" data-testid={`section-${current.slug}`}>
          {current.render()}
        </div>
      </div>
    </Blade>
  );
}

function iconFor(slug: string) {
  switch (slug) {
    case "overview":
      return "info" as const;
    case "activitylog":
      return "activity" as const;
    case "users":
      return "iam" as const;
    case "tags":
      return "tag" as const;
    case "properties":
      return "properties" as const;
    case "containers":
      return "folder" as const;
    case "cors":
      return "link" as const;
    default:
      return "document" as const;
  }
}

function SubscriptionBlade({ id, section }: { id: string; section: string[] }) {
  const parsed = parseResourceId(id);
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  const subscription = subscriptions.data?.find((sub) => sub.subscriptionId === parsed.subscriptionId);
  const service = serviceById("subscriptions");
  const name = subscription?.displayName ?? parsed.subscriptionId;
  const base = (slug: string) => resourceRoute(id, slug);
  const sectionName = section[0] ?? "overview";
  const sections: SectionDef[] = [
    {
      slug: "overview",
      label: "Overview",
      render: () => (
        <Essentials
          left={[
            { label: "Subscription ID", value: parsed.subscriptionId },
            { label: "Directory", value: subscription?.tenantId ?? "" },
          ]}
          right={[{ label: "Status", value: subscription ? <StatusBadge status={subscription.state === "Enabled" ? "Active" : subscription.state} /> : "" }]}
        />
      ),
    },
    { slug: "activitylog", label: "Activity log", render: () => <ActivityLogSection scope={id} /> },
    { slug: "users", label: "Access control (IAM)", render: () => <IamSection scope={id} /> },
    { slug: "tags", label: "Tags", render: () => <TagsSection scope={id} /> },
    { slug: "resourcegroups", label: "Resource groups", group: "Settings", render: () => <ResourceGroupsOfSubscription subscriptionId={parsed.subscriptionId} /> },
  ];
  const current = sections.find((entry) => entry.slug === sectionName) ?? sections[0];
  return (
    <Blade
      title={current.slug === "overview" ? name : `${name} | ${current.label}`}
      subtitle="Subscription"
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: "Subscriptions", to: service.route }, { label: name }]}
      testid="subscription-blade"
    >
      <div className="az-resource-layout">
        <ServiceMenu
          label={`${name} menu`}
          top={sections.filter((entry) => !entry.group).map((entry) => ({ label: entry.label, to: base(entry.slug), icon: iconFor(entry.slug) }))}
          groups={[{ label: "Settings", items: sections.filter((entry) => entry.group).map((entry) => ({ label: entry.label, to: base(entry.slug), icon: "resource_group" as const })) }]}
        />
        <div className="az-resource-content" data-testid={`section-${current.slug}`}>
          {current.render()}
        </div>
      </div>
    </Blade>
  );
}

function ResourceGroupsOfSubscription({ subscriptionId }: { subscriptionId: string }) {
  return <ResourceListSection groupId={`/subscriptions/${subscriptionId}`} groupsOnly />;
}

/** `resource/{resourceId}/{section}`. */
export function ResourcePage() {
  const params = useParams();
  const { id, section } = splitResourcePath(params["*"] ?? "");
  const parsed = parseResourceId(id);
  if (parsed.type === "Microsoft.Resources/subscriptions") return <SubscriptionBlade key={id} id={id} section={section} />;
  return <ArmResourceBlade key={id} id={id} section={section} />;
}
