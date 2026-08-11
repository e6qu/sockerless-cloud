import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsRowLink, type AwsColumn } from "../console/index.js";
import { createIAMUser, deleteIAMUser, fetchIAMUsers, type IAMUserSummary } from "../api.js";

// AWS Identity and Access Management (IAM) users. The list, creation, and
// deletion all go through the real IAM Query API with the operator's federated
// credentials — the console is just another signed IAM client.

const columns: AwsColumn<IAMUserSummary>[] = [
  {
    id: "userName",
    header: "User name",
    cell: (row) => <AwsRowLink to={`/ui/iam/users/${encodeURIComponent(row.userName)}`}>{row.userName}</AwsRowLink>,
    value: (row) => row.userName,
  },
  { id: "path", header: "Path", cell: (row) => row.path, value: (row) => row.path },
  { id: "arn", header: "ARN", cell: (row) => row.arn, value: (row) => row.arn },
  { id: "createDate", header: "Created", cell: (row) => row.createDate, value: (row) => row.createDate },
];

// The constraint real IAM enforces on user names; validating it inline is what
// the real console does before it lets the request go out.
const USER_NAME_PATTERN = /^[\w+=,.@-]{1,64}$/;

function CreateUserModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: createIAMUser,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["iam-users"] });
      onClose();
    },
  });
  const trimmed = name.trim();
  const valid = USER_NAME_PATTERN.test(trimmed);
  return (
    <AwsModal
      title="Create user"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="iam-create-user-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate(trimmed)}
          >
            {create.isPending ? "Creating…" : "Create user"}
          </AwsButton>
        </>
      }
    >
      <p>
        A user is an identity in AWS Identity and Access Management (IAM) with long-term credentials. After creating
        the user, open its Security credentials to create an access key.
      </p>
      <FormField label="User name" constraintText="Up to 64 characters. Letters, digits, and + = , . @ _ - are allowed.">
        <Input
          value={name}
          onChange={(event) => setName(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "iam-user-name-input" }}
        />
      </FormField>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the user.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function DeleteUsersModal({
  users,
  onClose,
  clearSelection,
}: {
  users: IAMUserSummary[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    // Deletion is per-user on the wire; a failure part-way (IAM's
    // DeleteConflict when access keys or policies still exist) surfaces as the
    // real API error, with the already-deleted users gone from the refreshed
    // list.
    mutationFn: async () => {
      for (const user of users) {
        await deleteIAMUser(user.userName);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["iam-users"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={users.length === 1 ? `Delete ${users[0].userName}?` : `Delete ${users.length} users?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="iam-delete-user-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>
        Deleting a user is permanent. IAM refuses to delete a user that still has access keys, inline or attached
        policies, group memberships, or a login profile — remove those first.
      </p>
      <ul>
        {users.map((user) => (
          <li key={user.userName}>
            <code>{user.userName}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function IAMUsersPage() {
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ users: IAMUserSummary[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<IAMUserSummary>
        title="Users"
        description="Users in AWS Identity and Access Management (IAM). Open a user's Security credentials to create an access key for the AWS CLI."
        columns={columns}
        queryKey={["iam-users"]}
        queryFn={fetchIAMUsers}
        filterPlaceholder="Find users"
        emptyTitle="No users"
        emptyDescription="No IAM users exist in this account. Create a user to mint CLI credentials."
        rowKey={(row) => row.userName}
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="iam-delete-user"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ users: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="iam-create-user" onClick={() => setCreating(true)}>
              Create user
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateUserModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteUsersModal
          users={deleting.users}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
