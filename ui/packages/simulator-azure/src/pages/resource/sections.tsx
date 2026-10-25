import { useMemo, useState } from "react";
import { Link as RouterLink } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Checkbox,
  Dropdown,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Field,
  Input,
  Option,
  OverlayDrawer,
  Radio,
  RadioGroup,
  Tab,
  TabList,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
} from "@fluentui/react-components";
import type { ArmGenericResource } from "../../api.js";
import {
  createRoleAssignment,
  deleteRoleAssignment,
  getPrincipal,
  getScopeTags,
  listActivityLog,
  listChildren,
  listPrincipals,
  listResourceGroupsPage,
  listResourcesPage,
  listRoleAssignments,
  listRoleDefinitions,
  parseResourceId,
  putScopeTags,
  type RoleAssignment,
} from "../../azure.js";
import { armWrite } from "../../portal/arm.js";
import { bladeForResource, type BladeSubResource } from "../../services.js";
import {
  CommandBar,
  ConfirmDialog,
  EmptyState,
  ErrorBar,
  errorText,
  formatTime,
  InfoBar,
  StatusBadge,
} from "../../portal/AzurePortal.js";
import { BrowseList } from "../../portal/BrowseList.js";
import { AzureIcon } from "../../portal/icons.js";
import { useNotifications } from "../../portal/notifications.js";
import { resourceRoute } from "../../portal/routes.js";

/** A resource group's resources, or a subscription's resource groups. */
export function ResourceListSection({ groupId, groupsOnly }: { groupId: string; groupsOnly?: boolean }) {
  const parsed = parseResourceId(groupId);
  const typeLabel = (resource: ArmGenericResource) => bladeForResource(resource.type, resource.kind)?.kind ?? resource.type;
  return (
    <section aria-label={groupsOnly ? "Resource groups" : "Resources"} className="az-section">
      <Text as="h2" size={400} weight="semibold" block>
        {groupsOnly ? "Resource groups" : "Resources"}
      </Text>
      <BrowseList
        source={{
          key: `${groupsOnly ? "groups-of" : "resources-of"}:${groupId}`,
          fetchPage: async (subscriptionId, top, next) => {
            if (subscriptionId !== parsed.subscriptionId) return { value: [] };
            return groupsOnly
              ? listResourceGroupsPage(subscriptionId, top, next)
              : listResourcesPage(subscriptionId, { top, next, filter: `resourceGroup eq '${parsed.resourceGroup}'` });
          },
        }}
        columns={[
          { id: "name", header: "Name", value: (resource) => resource.name },
          { id: "type", header: "Type", value: typeLabel },
          { id: "location", header: "Location", value: (resource) => resource.location },
        ]}
        defaultColumns={["type", "location"]}
        noun={groupsOnly ? "resource groups" : "resources"}
        typeFilter={!groupsOnly}
        typeLabel={typeLabel}
        emptyTitle={groupsOnly ? "No resource groups to display" : "No resources to display"}
        emptyDescription={groupsOnly ? "Create a resource group to organize resources." : "Resources you create in this resource group appear here."}
        testid={groupsOnly ? "subscription-groups" : "group-resources"}
      />
    </section>
  );
}

interface TagRow {
  name: string;
  value: string;
}

/** Tags: name/value pairs, written with the Tags API at the scope. */
export function TagsSection({ scope }: { scope: string }) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const tags = useQuery({ queryKey: ["tags", scope], queryFn: () => getScopeTags(scope) });
  const [draft, setDraft] = useState<TagRow[] | null>(null);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const rows = draft ?? [...Object.entries(tags.data ?? {}).map(([name, value]) => ({ name, value })), { name: "", value: "" }];
  const setRow = (index: number, change: Partial<TagRow>) => {
    const next = rows.map((row, position) => (position === index ? { ...row, ...change } : row));
    if (index === rows.length - 1 && (change.name || change.value)) next.push({ name: "", value: "" });
    setDraft(next);
  };
  const duplicate = rows.filter((row) => row.name).map((row) => row.name.toLowerCase()).find((name, index, all) => all.indexOf(name) !== index);
  const apply = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track(
        { progress: "Updating tags", success: "Updated tags", failure: "Failed to update tags", description: scope },
        () => putScopeTags(scope, Object.fromEntries(rows.filter((row) => row.name).map((row) => [row.name, row.value]))),
      );
      setDraft(null);
      void queryClient.invalidateQueries({ queryKey: ["tags", scope] });
      void queryClient.invalidateQueries({ queryKey: ["resource", scope] });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label="Tags" className="az-section">
      <Text block size={200} className="az-form-help">
        Tags are name/value pairs that enable you to categorize resources and view consolidated billing by applying the same tag to multiple resources and resource groups.
      </Text>
      {tags.isError ? <ErrorBar title="The tags could not be read.">{errorText(tags.error)}</ErrorBar> : null}
      <div className="az-tags-grid" role="group" aria-label="Tags">
        <div className="az-tags-row az-tags-head" aria-hidden>
          <span>Name</span>
          <span />
          <span>Value</span>
        </div>
        {rows.map((row, index) => (
          <div className="az-tags-row" key={index}>
            <Input aria-label={`Tag ${index + 1} name`} value={row.name} onChange={(_, data) => setRow(index, { name: data.value })} />
            <span aria-hidden>:</span>
            <Input aria-label={`Tag ${index + 1} value`} value={row.value} onChange={(_, data) => setRow(index, { value: data.value })} />
            {index < rows.length - 1 ? (
              <Button appearance="subtle" aria-label={`Delete tag ${row.name}`} icon={<AzureIcon name="delete" />} onClick={() => setDraft(rows.filter((_, position) => position !== index))} />
            ) : null}
          </div>
        ))}
      </div>
      {duplicate ? <ErrorBar title={`The tag name '${duplicate}' is used more than once.`} /> : null}
      {error ? <ErrorBar title="The tags could not be saved.">{error}</ErrorBar> : null}
      <div className="az-form-actions">
        <Button appearance="primary" disabled={busy || draft === null || Boolean(duplicate)} onClick={() => void apply()} data-testid="tags-apply">
          {busy ? "Saving…" : "Apply"}
        </Button>
        <Button disabled={busy || draft === null} onClick={() => setDraft(null)}>
          Discard changes
        </Button>
      </div>
    </section>
  );
}

function scopeLabel(assignmentScope: string, scope: string): string {
  if (assignmentScope.toLowerCase() === scope.toLowerCase()) return "This resource";
  const parsed = parseResourceId(assignmentScope);
  if (parsed.type === "Microsoft.Resources/subscriptions") return "Subscription (Inherited)";
  if (parsed.resourceGroup && parsed.type === "Microsoft.Resources/resourceGroups") return `Resource group (Inherited)`;
  return assignmentScope;
}

function PrincipalCell({ assignment }: { assignment: RoleAssignment }) {
  const principal = useQuery({
    queryKey: ["principal", assignment.principalId],
    queryFn: () => getPrincipal(assignment.principalId, assignment.principalType),
    retry: false,
  });
  return (
    <span className="az-principal">
      <Text weight="semibold" block>
        {principal.data?.displayName ?? assignment.principalId}
      </Text>
      <Text size={200} block>
        {principal.data?.detail || assignment.principalId}
      </Text>
    </span>
  );
}

function AddRoleAssignment({ scope, open, onClose }: { scope: string; open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const subscriptionScope = `/subscriptions/${parseResourceId(scope).subscriptionId}`;
  const roles = useQuery({ queryKey: ["role-definitions", subscriptionScope], queryFn: () => listRoleDefinitions(subscriptionScope), enabled: open });
  const principals = useQuery({ queryKey: ["principals"], queryFn: listPrincipals, enabled: open });
  const [roleId, setRoleId] = useState("");
  const [assignTo, setAssignTo] = useState<"directory" | "identity">("directory");
  const [member, setMember] = useState("");
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const candidates = (principals.data ?? []).filter((principal) =>
    assignTo === "identity" ? principal.principalType === "ServicePrincipal" : true,
  );
  const visible = candidates.filter((principal) => !filter || `${principal.displayName} ${principal.detail}`.toLowerCase().includes(filter.toLowerCase()));
  const chosen = candidates.find((principal) => principal.id === member);
  const role = roles.data?.find((entry) => entry.id === roleId);
  const assign = async () => {
    if (!role || !chosen) return;
    setBusy(true);
    setError(undefined);
    try {
      await track(
        { progress: "Adding role assignment", success: "Added role assignment", failure: "Failed to add role assignment", description: `${chosen.displayName} was added as ${role.roleName}.` },
        () => createRoleAssignment(scope, role.id, chosen.id, chosen.principalType),
      );
      void queryClient.invalidateQueries({ queryKey: ["role-assignments", scope] });
      setRoleId("");
      setMember("");
      onClose();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <OverlayDrawer open={open} position="end" size="medium" onOpenChange={(_, data) => !data.open && onClose()} aria-label="Add role assignment">
      <DrawerHeader>
        <DrawerHeaderTitle action={<Button appearance="subtle" aria-label="Close" icon={<AzureIcon name="dismiss" />} onClick={onClose} />}>Add role assignment</DrawerHeaderTitle>
      </DrawerHeader>
      <DrawerBody className="az-drawer-form">
        <Field label="Role" required>
          <Dropdown placeholder="Select a role" value={role?.roleName ?? ""} selectedOptions={roleId ? [roleId] : []} onOptionSelect={(_, data) => setRoleId(data.optionValue ?? "")}>
            {(roles.data ?? []).map((entry) => (
              <Option key={entry.id} value={entry.id} text={entry.roleName}>
                {entry.roleName}
              </Option>
            ))}
          </Dropdown>
        </Field>
        {role ? (
          <Text size={200} block>
            {role.description}
          </Text>
        ) : null}
        <Field label="Assign access to">
          <RadioGroup value={assignTo} onChange={(_, data) => setAssignTo(data.value as "directory" | "identity")}>
            <Radio value="directory" label="User, group, or service principal" />
            <Radio value="identity" label="Managed identity" />
          </RadioGroup>
        </Field>
        <Field label="Members" required>
          <Input value={filter} onChange={(_, data) => setFilter(data.value)} placeholder="Search by name or ID" aria-label="Search members" />
        </Field>
        <div className="az-member-list" role="radiogroup" aria-label="Members">
          {principals.isLoading ? <EmptyState title="Loading members…" loading /> : null}
          {visible.map((principal) => (
            <Checkbox
              key={principal.id}
              checked={member === principal.id}
              onChange={(_, data) => setMember(data.checked ? principal.id : "")}
              label={`${principal.displayName} (${principal.principalType === "ServicePrincipal" ? "Service principal" : principal.principalType})${principal.detail ? ` · ${principal.detail}` : ""}`}
            />
          ))}
        </div>
        {error ? <ErrorBar title="The role assignment could not be added.">{error}</ErrorBar> : null}
      </DrawerBody>
      <DrawerFooter>
        <Button appearance="primary" disabled={!role || !chosen || busy} onClick={() => void assign()} data-testid="iam-assign">
          {busy ? "Assigning…" : "Review + assign"}
        </Button>
        <Button onClick={onClose}>Cancel</Button>
      </DrawerFooter>
    </OverlayDrawer>
  );
}

/** Access control (IAM): the role assignments that apply at the scope,
 *  inherited ones included, with Add and Remove. */
export function IamSection({ scope }: { scope: string }) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const [tab, setTab] = useState("assignments");
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [removing, setRemoving] = useState(false);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [removeError, setRemoveError] = useState<string>();
  const subscriptionScope = `/subscriptions/${parseResourceId(scope).subscriptionId}`;
  const assignments = useQuery({ queryKey: ["role-assignments", scope], queryFn: () => listRoleAssignments(scope) });
  const roles = useQuery({ queryKey: ["role-definitions", subscriptionScope], queryFn: () => listRoleDefinitions(subscriptionScope) });
  const roleName = (id: string) => roles.data?.find((role) => role.id.toLowerCase() === id.toLowerCase() || id.toLowerCase().endsWith(`/${role.name.toLowerCase()}`))?.roleName ?? id.split("/").pop();
  const applicable = useMemo(
    () => (assignments.data ?? []).filter((assignment) => scope.toLowerCase().startsWith((assignment.scope || "").toLowerCase())),
    [assignments.data, scope],
  );
  const remove = async () => {
    setRemoveBusy(true);
    setRemoveError(undefined);
    try {
      await track(
        { progress: "Removing role assignments", success: "Removed role assignments", failure: "Failed to remove role assignments", description: `${selected.length} role assignments` },
        () => Promise.all(selected.map((id) => deleteRoleAssignment(id))),
      );
      setSelected([]);
      setRemoving(false);
      void queryClient.invalidateQueries({ queryKey: ["role-assignments", scope] });
    } catch (err) {
      setRemoveError(errorText(err));
    } finally {
      setRemoveBusy(false);
    }
  };
  return (
    <section aria-label="Access control (IAM)" className="az-section">
      <CommandBar
        commands={[
          { label: "Add role assignment", icon: "add", onSelect: () => setAdding(true), testid: "iam-add" },
          { label: "Remove", icon: "delete", disabled: selected.length === 0, onSelect: () => setRemoving(true), testid: "iam-remove" },
          { label: "Refresh", icon: "refresh", onSelect: () => void queryClient.invalidateQueries({ queryKey: ["role-assignments", scope] }) },
        ]}
      />
      <TabList selectedValue={tab} onTabSelect={(_, data) => setTab(String(data.value))} aria-label="Access control">
        <Tab value="assignments">Role assignments</Tab>
        <Tab value="roles">Roles</Tab>
      </TabList>
      {assignments.isError ? <ErrorBar title="The role assignments could not be listed.">{errorText(assignments.error)}</ErrorBar> : null}
      {tab === "assignments" ? (
        <Table aria-label="Role assignments" size="small" className="az-grid" data-testid="role-assignments">
          <TableHeader>
            <TableRow>
              <TableHeaderCell className="az-grid-select"><span className="sr-only">Select</span></TableHeaderCell>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Type</TableHeaderCell>
              <TableHeaderCell>Role</TableHeaderCell>
              <TableHeaderCell>Scope</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {applicable.map((assignment) => (
              <TableRow key={assignment.id}>
                <TableCell className="az-grid-select">
                  <Checkbox
                    aria-label={`Select role assignment ${assignment.name}`}
                    checked={selected.includes(assignment.id)}
                    disabled={assignment.scope.toLowerCase() !== scope.toLowerCase()}
                    onChange={(_, data) => setSelected(data.checked ? [...selected, assignment.id] : selected.filter((id) => id !== assignment.id))}
                  />
                </TableCell>
                <TableCell>
                  <PrincipalCell assignment={assignment} />
                </TableCell>
                <TableCell>{assignment.principalType === "ServicePrincipal" ? "App" : assignment.principalType}</TableCell>
                <TableCell>{roleName(assignment.roleDefinitionId)}</TableCell>
                <TableCell>{scopeLabel(assignment.scope, scope)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ) : (
        <Table aria-label="Roles" size="small" className="az-grid">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Description</TableHeaderCell>
              <TableHeaderCell>Type</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(roles.data ?? []).map((role) => (
              <TableRow key={role.id}>
                <TableCell>{role.roleName}</TableCell>
                <TableCell>{role.description}</TableCell>
                <TableCell>{role.roleType === "BuiltInRole" ? "BuiltInRole" : role.roleType}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {tab === "assignments" && assignments.data && applicable.length === 0 ? <EmptyState title="No role assignments at this scope" /> : null}
      <AddRoleAssignment scope={scope} open={adding} onClose={() => setAdding(false)} />
      <ConfirmDialog
        open={removing}
        title="Remove role assignments"
        confirmLabel="Yes"
        busyLabel="Removing…"
        busy={removeBusy}
        testid="iam-remove-dialog"
        error={removeError}
        onConfirm={() => void remove()}
        onCancel={() => setRemoving(false)}
      >
        <Text block>Do you want to remove the {selected.length} selected role assignments?</Text>
      </ConfirmDialog>
    </section>
  );
}

const TIMESPANS = [
  { label: "Last hour", ms: 60 * 60 * 1000 },
  { label: "Last 6 hours", ms: 6 * 60 * 60 * 1000 },
  { label: "Last 24 hours", ms: 24 * 60 * 60 * 1000 },
  { label: "Last week", ms: 7 * 24 * 60 * 60 * 1000 },
];

/** Activity log: the scope's Azure Resource Manager write, delete and action
 *  events over a timespan. */
export function ActivityLogSection({ scope }: { scope: string }) {
  const [timespan, setTimespan] = useState(TIMESPANS[1]);
  const events = useQuery({ queryKey: ["activity-log", scope, timespan.ms], queryFn: () => listActivityLog(scope, timespan.ms) });
  return (
    <section aria-label="Activity log" className="az-section">
      <CommandBar commands={[{ label: "Refresh", icon: "refresh", onSelect: () => void events.refetch(), testid: "activity-refresh" }]} />
      <div className="az-filters">
        <Field label="Timespan" orientation="horizontal">
          <Dropdown
            value={timespan.label}
            selectedOptions={[timespan.label]}
            onOptionSelect={(_, data) => setTimespan(TIMESPANS.find((entry) => entry.label === data.optionValue) ?? TIMESPANS[1])}
          >
            {TIMESPANS.map((entry) => (
              <Option key={entry.label} value={entry.label}>
                {entry.label}
              </Option>
            ))}
          </Dropdown>
        </Field>
      </div>
      {events.isError ? <ErrorBar title="The activity log could not be read.">{errorText(events.error)}</ErrorBar> : null}
      <Table aria-label="Activity log" size="small" className="az-grid" data-testid="activity-log">
        <TableHeader>
          <TableRow>
            <TableHeaderCell>Operation name</TableHeaderCell>
            <TableHeaderCell>Status</TableHeaderCell>
            <TableHeaderCell>Time stamp</TableHeaderCell>
            <TableHeaderCell>Event initiated by</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(events.data ?? []).map((event) => (
            <TableRow key={event.eventDataId}>
              <TableCell>{event.operationName}</TableCell>
              <TableCell>
                <StatusBadge status={event.status} />
              </TableCell>
              <TableCell>{formatTime(event.eventTimestamp)}</TableCell>
              <TableCell>{event.caller}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {events.isLoading ? <EmptyState title="Loading the activity log…" loading /> : null}
      {events.data && events.data.length === 0 ? <EmptyState title="No events to display" description="Change the timespan to see older events." /> : null}
    </section>
  );
}

/** A child collection ARM nests under the resource: its list, and where the
 *  descriptor allows it, create and delete. */
export function ChildrenSection({ sub, resource, apiVersion }: { sub: BladeSubResource; resource: ArmGenericResource; apiVersion: string }) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const key = ["children", resource.id, sub.title];
  const children = useQuery({
    queryKey: key,
    queryFn: () => (sub.inline ? Promise.resolve(sub.inline(resource)) : listChildren(resource.id, sub.path!, apiVersion)),
  });
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [selected, setSelected] = useState<string[]>([]);
  const [deleting, setDeleting] = useState(false);
  const create = sub.create;
  const nameError = name && create && !create.namePattern.test(name) ? create.nameRule : undefined;
  const testid = `child-${sub.title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
  const refresh = () => void queryClient.invalidateQueries({ queryKey: key });

  const submit = async () => {
    if (!create || !name || nameError) return;
    setBusy(true);
    setError(undefined);
    const fieldValues = Object.fromEntries(create.fields.map((field) => [field.id, values[field.id] ?? field.defaultValue]));
    try {
      await track(
        { progress: `Creating ${create.noun.toLowerCase()} ${name}`, success: `Created ${create.noun.toLowerCase()} ${name}`, failure: `Failed to create ${create.noun.toLowerCase()} ${name}`, description: resource.name },
        () => armWrite("PUT", `${resource.id}/${sub.path}/${name}?api-version=${apiVersion}`, create.body(fieldValues)),
      );
      setCreating(false);
      setName("");
      setValues({});
      refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track(
        { progress: `Deleting ${selected.length} ${sub.title.toLowerCase()}`, success: `Deleted ${selected.length} ${sub.title.toLowerCase()}`, failure: `Failed to delete ${sub.title.toLowerCase()}`, description: resource.name },
        () => Promise.all(selected.map((id) => armWrite("DELETE", `${id}?api-version=${apiVersion}`))),
      );
      setSelected([]);
      setDeleting(false);
      refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const rows = children.data ?? [];
  return (
    <section aria-label={sub.title} className="az-section" data-testid={testid}>
      <CommandBar
        commands={[
          ...(create ? [{ label: create.noun, icon: "add" as const, onSelect: () => setCreating(true), testid: `${testid}-create` }] : []),
          { label: "Refresh", icon: "refresh", onSelect: refresh },
          ...(sub.deletable ? [{ label: "Delete", icon: "delete" as const, disabled: selected.length === 0, onSelect: () => setDeleting(true), testid: `${testid}-delete` }] : []),
        ]}
      />
      {children.isError ? <ErrorBar title={`The ${sub.title.toLowerCase()} could not be listed.`}>{errorText(children.error)}</ErrorBar> : null}
      {error && !deleting ? <ErrorBar title="The operation failed.">{error}</ErrorBar> : null}
      <Table aria-label={sub.title} size="small" className="az-grid" data-testid={`${testid}-table`}>
        <TableHeader>
          <TableRow>
            {sub.deletable ? <TableHeaderCell className="az-grid-select"><span className="sr-only">Select</span></TableHeaderCell> : null}
            {sub.columns.map((column) => (
              <TableHeaderCell key={column.id}>{column.header}</TableHeaderCell>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((child) => (
            <TableRow key={child.id || child.name}>
              {sub.deletable ? (
                <TableCell className="az-grid-select">
                  <Checkbox
                    aria-label={`Select ${child.name}`}
                    checked={selected.includes(child.id)}
                    onChange={(_, data) => setSelected(data.checked ? [...selected, child.id] : selected.filter((id) => id !== child.id))}
                  />
                </TableCell>
              ) : null}
              {sub.columns.map((column) => (
                <TableCell key={column.id}>
                  {["status", "state", "provisioningState"].includes(column.id) && column.read(child) ? <StatusBadge status={column.read(child)} /> : column.read(child) || "—"}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {children.isLoading ? <EmptyState title={`Loading ${sub.title.toLowerCase()}…`} loading /> : null}
      {children.data && rows.length === 0 ? <EmptyState title={sub.emptyTitle} description={sub.emptyDescription} /> : null}
      {create ? (
        <OverlayDrawer open={creating} position="end" size="small" onOpenChange={(_, data) => !data.open && setCreating(false)} aria-label={`Create ${create.noun.toLowerCase()}`}>
          <DrawerHeader>
            <DrawerHeaderTitle action={<Button appearance="subtle" aria-label="Close" icon={<AzureIcon name="dismiss" />} onClick={() => setCreating(false)} />}>
              Create {create.noun.toLowerCase()}
            </DrawerHeaderTitle>
          </DrawerHeader>
          <DrawerBody className="az-drawer-form">
            <Field label="Name" required validationMessage={nameError} validationState={nameError ? "error" : "none"}>
              <Input value={name} onChange={(_, data) => setName(data.value)} />
            </Field>
            {create.fields.map((field) => (
              <Field key={field.id} label={field.label} hint={field.info}>
                {field.options ? (
                  <Dropdown
                    value={values[field.id] ?? field.defaultValue}
                    selectedOptions={[values[field.id] ?? field.defaultValue]}
                    onOptionSelect={(_, data) => setValues({ ...values, [field.id]: data.optionValue ?? "" })}
                  >
                    {field.options.map((option) => (
                      <Option key={option} value={option}>
                        {option}
                      </Option>
                    ))}
                  </Dropdown>
                ) : (
                  <Input value={values[field.id] ?? field.defaultValue} onChange={(_, data) => setValues({ ...values, [field.id]: data.value })} />
                )}
              </Field>
            ))}
            {error ? <ErrorBar title="The create failed.">{error}</ErrorBar> : null}
          </DrawerBody>
          <DrawerFooter>
            <Button appearance="primary" disabled={!name || Boolean(nameError) || busy} onClick={() => void submit()} data-testid={`${testid}-submit`}>
              {busy ? "Creating…" : "Create"}
            </Button>
            <Button onClick={() => setCreating(false)}>Cancel</Button>
          </DrawerFooter>
        </OverlayDrawer>
      ) : null}
      <ConfirmDialog
        open={deleting}
        title={`Delete ${selected.length} ${selected.length === 1 ? (create?.noun.toLowerCase() ?? "item") : sub.title.toLowerCase()}`}
        busy={busy}
        testid={`${testid}-delete-dialog`}
        error={deleting ? error : undefined}
        onConfirm={() => void remove()}
        onCancel={() => {
          setDeleting(false);
          setError(undefined);
        }}
      >
        <Text block>Deleting is permanent. This action can't be undone.</Text>
      </ConfirmDialog>
    </section>
  );
}

/** Properties: every property the resource's Get returns, by name. */
export function PropertiesSection({ resource, apiVersion }: { resource: ArmGenericResource; apiVersion: string }) {
  const flat: [string, string][] = [];
  const walk = (value: unknown, path: string) => {
    if (value !== null && typeof value === "object" && !Array.isArray(value)) {
      for (const [key, child] of Object.entries(value)) walk(child, path ? `${path}.${key}` : key);
    } else {
      flat.push([path, Array.isArray(value) ? JSON.stringify(value) : String(value)]);
    }
  };
  walk(resource.properties, "");
  return (
    <section aria-label="Properties" className="az-section">
      <InfoBar>
        Read from the resource's Get at API version {apiVersion}. <RouterLink to={resourceRoute(resource.id)}>Back to Overview</RouterLink>
      </InfoBar>
      <dl className="az-review">
        <div className="az-review-row">
          <dt>Resource ID</dt>
          <dd>
            <code>{resource.id}</code>
          </dd>
        </div>
        {flat.map(([key, value]) => (
          <div className="az-review-row" key={key}>
            <dt>{key}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
