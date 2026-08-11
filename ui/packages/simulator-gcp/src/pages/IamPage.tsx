import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { SubResourceTable } from "./SubResourceTable.js";
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
  const save = useMutation({
    mutationFn: () => setProjectIamPolicy(project, addBinding(policy, member, role)),
    onSuccess: onSaved,
  });
  // A principal is written as "<type>:<identifier>" — user:, serviceAccount:,
  // group:, domain: or the allUsers / allAuthenticatedUsers specials.
  const valid = /^(user|serviceAccount|group|domain):\S+$/.test(member) || /^all(AuthenticatedUsers|Users)$/.test(member);
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
  grant,
  onClose,
  onSaved,
}: {
  policy: IamPolicy;
  grant: IamGrant;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { project } = useProject();
  const save = useMutation({
    mutationFn: () => setProjectIamPolicy(project, removeBinding(policy, grant.member, grant.role)),
    onSuccess: onSaved,
  });
  return (
    <GcpDialog title="Remove access?" testId="iam-remove-dialog" onClose={onClose}>
      <p>
        Removing <strong>{grant.role}</strong> from <strong>{grant.member}</strong> revokes the access that
        role grants on this project.
      </p>
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

export function IamPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [granting, setGranting] = useState(false);
  const [removing, setRemoving] = useState<IamGrant | null>(null);

  const policy = useQuery({ queryKey: ["iam-policy", project], queryFn: () => fetchProjectIamPolicy(project) });
  const roles = useQuery({ queryKey: ["iam-roles"], queryFn: fetchIamRoles });
  const grants = useQuery({
    queryKey: ["iam-policy", project],
    queryFn: () => fetchProjectIamPolicy(project),
    select: flattenBindings,
  });

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["iam-policy", project] });

  return (
    <>
      <GcpPageHeader
        title="IAM"
        description="Identity and Access Management controls who has what access on this project. A principal is granted a role, and the role carries the permissions."
        actions={[
          {
            label: "Grant access",
            icon: "add",
            primary: true,
            testId: "iam-grant-access",
            disabled: !policy.data,
            onSelect: () => setGranting(true),
          },
        ]}
        onRefresh={() => {
          void policy.refetch();
          void roles.refetch();
        }}
        refreshing={policy.isFetching || roles.isFetching}
      />
      <GcpTabs
        label="IAM"
        tabs={[
          {
            id: "permissions",
            label: "Permissions",
            content: (
              <SubResourceTable<IamGrant>
                query={grants}
                testId="iam-grants-table"
                noun="the allow policy"
                emptyHeadline="This project's allow policy has no grants"
                emptyDescription="Grant a principal a role to give it access to this project."
                rowKey={(row) => `${row.member}|${row.role}`}
                columns={[
                  { header: "Principal", cell: (row) => row.member },
                  { header: "Role", cell: (row) => row.role },
                  {
                    header: "Actions",
                    cell: (row) => (
                      <button
                        type="button"
                        className="gc-button-text"
                        data-testid={`iam-remove-${row.member}-${row.role}`}
                        aria-label={`Remove ${row.role} from ${row.member}`}
                        onClick={() => setRemoving(row)}
                      >
                        Remove
                      </button>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            id: "roles",
            label: "Roles",
            content: (
              <SubResourceTable<IamRole>
                query={roles}
                testId="iam-roles-table"
                noun="roles"
                emptyHeadline="No roles are available"
                emptyDescription="Predefined roles published by Google Cloud appear here."
                rowKey={(row) => row.name}
                columns={[
                  { header: "Role", cell: (row) => row.title ?? row.name },
                  { header: "Role name", cell: (row) => row.name },
                  { header: "Description", cell: (row) => row.description ?? "—" },
                  { header: "Launch stage", cell: (row) => row.stage ?? "—" },
                ]}
              />
            ),
          },
        ]}
      />
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
          grant={removing}
          onClose={() => setRemoving(null)}
          onSaved={() => {
            setRemoving(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}
