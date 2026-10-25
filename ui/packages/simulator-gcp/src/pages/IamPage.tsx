import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpResourceTable, ResourceTableView } from "../console/GcpTable.js";
import { useOperations } from "../console/notifications.js";
import {
  fetchIamRoles,
  fetchProjectIamPolicy,
  setProjectIamPolicy,
  type IamBinding,
  type IamPolicy,
  type IamRole,
} from "../api.js";
import { useProject } from "../console/project.js";

// A grant is one (principal, role) pair. The API's allow policy groups
// principals under each role, so the console flattens it into the per-principal
// rows the real IAM page shows.
export interface IamGrant {
  member: string;
  role: string;
}

export function flattenBindings(policy: IamPolicy | undefined): IamGrant[] {
  const grants: IamGrant[] = [];
  for (const binding of policy?.bindings ?? []) {
    for (const member of binding.members ?? []) {
      grants.push({ member, role: binding.role });
    }
  }
  return grants.sort((left, right) => left.member.localeCompare(right.member));
}

// addBinding folds a new (principal, role) grant into the policy the console
// read. setIamPolicy replaces the whole policy, so the edit is applied to the
// policy as read — etag included — rather than sent as a partial update.
export function addBinding(policy: IamPolicy, member: string, role: string): IamPolicy {
  const bindings: IamBinding[] = (policy.bindings ?? []).map((binding) => ({ ...binding, members: [...(binding.members ?? [])] }));
  const existing = bindings.find((binding) => binding.role === role);
  if (existing) {
    if (!existing.members!.includes(member)) existing.members!.push(member);
  } else {
    bindings.push({ role, members: [member] });
  }
  return { ...policy, bindings };
}

// removeBinding drops a (principal, role) grant, and drops the binding
// entirely once its last principal is gone — the shape the real API stores.
export function removeBinding(policy: IamPolicy, member: string, role: string): IamPolicy {
  const bindings = (policy.bindings ?? [])
    .map((binding) =>
      binding.role === role
        ? { ...binding, members: (binding.members ?? []).filter((candidate) => candidate !== member) }
        : binding,
    )
    .filter((binding) => (binding.members ?? []).length > 0);
  return { ...policy, bindings };
}

// GrantAccessDialog and RemoveAccessDialog both write through the real
// projects.setIamPolicy method, sending the whole policy the page read with
// the edit applied.
export function GrantAccessDialog({
  policy,
  roles,
  onClose,
  onSaved,
}: {
  policy: IamPolicy;
  roles: IamRole[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { project } = useProject();
  const [member, setMember] = useState("");
  const [role, setRole] = useState(roles[0]?.name ?? "roles/viewer");
  const { track } = useOperations();
  const save = useMutation({
    mutationFn: () =>
      track("Update the project allow policy", () => setProjectIamPolicy(project, addBinding(policy, member, role)), "Updated the project allow policy"),
    onSuccess: onSaved,
  });
  // A principal is written as "<type>:<identifier>" — user:, serviceAccount:,
  // group:, domain: or the allUsers / allAuthenticatedUsers specials.
  const valid =
    /^(user|serviceAccount|group|domain):\S+$/.test(member) ||
    /^principal(Set)?:\/\/iam\.googleapis\.com\/\S+$/.test(member) ||
    /^all(AuthenticatedUsers|Users)$/.test(member);
  return (
    <GcpDialog title="Grant access" testId="iam-grant-dialog" onClose={onClose}>
      <label className="gc-field">
        New principals
        <input
          type="text"
          value={member}
          data-testid="iam-grant-member"
          onChange={(event) => setMember(event.target.value)}
        />
        <p className="gc-field-hint">
          A principal identifier, e.g. user:jane@example.com or serviceAccount:svc@project.iam.gserviceaccount.com.
        </p>
      </label>
      <label className="gc-field">
        Role
        <select value={role} data-testid="iam-grant-role" onChange={(event) => setRole(event.target.value)}>
          {roles.map((candidate) => (
            <option key={candidate.name} value={candidate.name}>
              {candidate.title ? `${candidate.title} (${candidate.name})` : candidate.name}
            </option>
          ))}
        </select>
      </label>
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the policy.</strong>{" "}
          {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="iam-grant-submit"
          disabled={!valid || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function RemoveAccessDialog({
  policy,
  grants,
  onClose,
  onSaved,
}: {
  policy: IamPolicy;
  grants: IamGrant[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { project } = useProject();
  const { track } = useOperations();
  const save = useMutation({
    mutationFn: () =>
      track(
        "Update the project allow policy",
        () => setProjectIamPolicy(project, grants.reduce((next, grant) => removeBinding(next, grant.member, grant.role), policy)),
        "Updated the project allow policy",
      ),
    onSuccess: onSaved,
  });
  return (
    <GcpDialog title="Remove access?" testId="iam-remove-dialog" onClose={onClose}>
      <p>These roles are removed, revoking the access they grant on this project:</p>
      <ul className="gc-dialog-list">
        {grants.map((grant) => (
          <li key={`${grant.member} ${grant.role}`}>
            <strong>{grant.role}</strong> from {grant.member}
          </li>
        ))}
      </ul>
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the policy.</strong>{" "}
          {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="iam-remove-confirm"
          disabled={save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Removing…" : "Remove"}
        </button>
      </div>
    </GcpDialog>
  );
}


// The principal type the IAM page's Type column names, from the member prefix.
export function principalType(member: string): string {
  const prefix = member.split(":")[0];
  switch (prefix) {
    case "user":
      return "User";
    case "serviceAccount":
      return "Service account";
    case "group":
      return "Group";
    case "domain":
      return "Domain";
    case "principal":
    case "principalSet":
      return "Workforce identity";
    default:
      return member.startsWith("all") ? "Public" : prefix;
  }
}

interface PrincipalRow {
  member: string;
  roles: string[];
}

interface RoleRow {
  role: string;
  members: string[];
}

export function IamPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [view, setView] = useState<"principals" | "roles">("principals");
  const [granting, setGranting] = useState(false);
  const [removing, setRemoving] = useState<IamGrant[] | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const policy = useQuery({ queryKey: ["iam-policy", project], queryFn: () => fetchProjectIamPolicy(project) });
  const roles = useQuery({ queryKey: ["iam-roles"], queryFn: fetchIamRoles });
  const grants = flattenBindings(policy.data);
  const roleTitle = (role: string) => roles.data?.find((candidate) => candidate.name === role)?.title ?? role;
  const principals: PrincipalRow[] = Array.from(new Set(grants.map((grant) => grant.member))).map((member) => ({
    member,
    roles: grants.filter((grant) => grant.member === member).map((grant) => grant.role),
  }));
  const byRole: RoleRow[] = (policy.data?.bindings ?? []).map((binding) => ({ role: binding.role, members: binding.members ?? [] }));
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["iam-policy", project] });
  const selectedGrants = grants.filter((grant) => selected.has(grant.member));

  return (
    <>
      <GcpPageHeader
        title="IAM"
        actions={[
          { label: "Grant access", icon: "person", testId: "iam-grant-access", disabled: !policy.data, onSelect: () => setGranting(true) },
          {
            label: "Remove access",
            icon: "delete",
            testId: "iam-remove-access",
            disabled: selectedGrants.length === 0,
            onSelect: () => setRemoving(selectedGrants),
          },
        ]}
        onRefresh={() => {
          void policy.refetch();
          void roles.refetch();
        }}
        refreshing={policy.isFetching || roles.isFetching}
      />
      <h2 className="gc-section-title">Permissions for project “{project}”</h2>
      <p className="gc-tab-intro">These permissions affect this project and all of its resources.</p>
      <div role="tablist" aria-label="View" className="gc-tablist">
        {(["principals", "roles"] as const).map((id) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={view === id}
            className={view === id ? "gc-tab gc-tab-active" : "gc-tab"}
            onClick={() => setView(id)}
          >
            {id === "principals" ? "View by principals" : "View by roles"}
          </button>
        ))}
      </div>
      {view === "principals" ? (
        <ResourceTableView<PrincipalRow>
          rows={policy.data ? principals : undefined}
          isLoading={policy.isLoading}
          error={policy.error}
          tableId="iam-grants-table"
          resourceNoun="principals"
          rowKey={(row) => row.member}
          selectable
          selected={selected}
          onSelectedChange={setSelected}
          columns={[
            { id: "type", header: "Type", cell: (row) => principalType(row.member), value: (row) => principalType(row.member) },
            { id: "principal", header: "Principal", cell: (row) => row.member, value: (row) => row.member },
            {
              id: "role",
              header: "Role",
              cell: (row) => (
                <ul className="gc-cell-list">
                  {row.roles.map((role) => (
                    <li key={role}>{roleTitle(role)}</li>
                  ))}
                </ul>
              ),
              value: (row) => row.roles.map(roleTitle).join(" "),
            },
          ]}
          rowActions={(row) =>
            row.roles.map((role) => ({
              label: `Remove ${roleTitle(role)}`,
              onSelect: () => setRemoving([{ member: row.member, role }]),
            }))
          }
          empty={{ headline: "No principals", description: "Grant a principal a role to give it access to this project." }}
        />
      ) : (
        <ResourceTableView<RoleRow>
          rows={policy.data ? byRole : undefined}
          isLoading={policy.isLoading}
          error={policy.error}
          tableId="iam-roles-view-table"
          resourceNoun="roles"
          rowKey={(row) => row.role}
          columns={[
            { id: "role", header: "Role", cell: (row) => roleTitle(row.role), value: (row) => roleTitle(row.role) },
            { id: "name", header: "Role name", cell: (row) => row.role, value: (row) => row.role },
            {
              id: "principals",
              header: "Principals",
              cell: (row) => (
                <ul className="gc-cell-list">
                  {row.members.map((member) => (
                    <li key={member}>{member}</li>
                  ))}
                </ul>
              ),
              value: (row) => row.members.join(" "),
            },
          ]}
          empty={{ headline: "No roles granted", description: "Roles granted on this project appear here." }}
        />
      )}
      {granting && policy.data ? (
        <GrantAccessDialog
          policy={policy.data}
          roles={roles.data ?? []}
          onClose={() => setGranting(false)}
          onSaved={() => {
            setGranting(false);
            refresh();
          }}
        />
      ) : null}
      {removing && policy.data ? (
        <RemoveAccessDialog
          policy={policy.data}
          grants={removing}
          onClose={() => setRemoving(null)}
          onSaved={() => {
            setRemoving(null);
            setSelected(new Set());
            refresh();
          }}
        />
      ) : null}
    </>
  );
}

export function IamRolesPage() {
  return (
    <GcpResourceTable<IamRole>
      title="Roles"
      columns={[
        { id: "title", header: "Title", cell: (row) => row.title ?? row.name, value: (row) => row.title ?? row.name },
        { id: "id", header: "ID", cell: (row) => row.name, value: (row) => row.name },
        {
          id: "stage",
          header: "Status",
          cell: (row) => (row.stage ? row.stage.charAt(0) + row.stage.slice(1).toLowerCase() : "—"),
          value: (row) => row.stage ?? "",
        },
        { id: "description", header: "Description", cell: (row) => row.description ?? "—", value: (row) => row.description ?? "" },
        {
          id: "permissions",
          header: "Permissions",
          cell: (row) => (row.includedPermissions ? String(row.includedPermissions.length) : "—"),
          value: (row) => String(row.includedPermissions?.length ?? ""),
          defaultHidden: true,
        },
      ]}
      queryKey={["iam-roles"]}
      queryFn={fetchIamRoles}
      resourceNoun="roles"
      testId="iam-roles-table"
      empty={{ headline: "No roles", description: "Predefined roles published by Google Cloud appear here." }}
      rowKey={(row) => row.name}
    />
  );
}
