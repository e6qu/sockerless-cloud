import { useState } from "react";
import { useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Alert from "@cloudscape-design/components/alert";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import Box from "@cloudscape-design/components/box";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsContainer,
  AwsCopyButton,
  AwsErrorAlert,
  AwsKeyValue,
  AwsModal,
  AwsPageHeader,
  AwsResourceTable,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import {
  createIAMAccessKey,
  deleteIAMAccessKey,
  fetchIAMAccessKeys,
  fetchIAMUser,
  updateIAMAccessKeyStatus,
  type IAMAccessKeyMetadata,
  type IAMCreatedAccessKey,
} from "../api.js";

// A user's Security credentials in AWS Identity and Access Management (IAM):
// the access keys the AWS CLI signs with. Creation shows the secret access key
// exactly once — the CreateAccessKey response is the only place the secret ever
// exists, and ListAccessKeys never returns it — so the dialog holds the
// one-time disclosure, the copy controls, and the exact CLI usage.

const keyColumns: AwsColumn<IAMAccessKeyMetadata>[] = [
  { id: "accessKeyId", header: "Access key ID", cell: (row) => <code>{row.accessKeyId}</code>, value: (row) => row.accessKeyId },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "createDate", header: "Created", cell: (row) => row.createDate, value: (row) => row.createDate },
];

/**
 * The one-time disclosure. `endpoint` is the console's own origin — the same
 * coordinate the console reaches the cloud APIs at is the coordinate the
 * operator's CLI reaches them at.
 */
export function AccessKeyCreatedPanel({
  accessKeyId,
  secretAccessKey,
  endpoint,
}: IAMCreatedAccessKey & { endpoint: string }) {
  const [revealed, setRevealed] = useState(false);
  const verifyCommand = `aws sts get-caller-identity --endpoint-url ${endpoint}`;
  const configureTranscript = [
    "$ aws configure",
    `AWS Access Key ID [None]: ${accessKeyId}`,
    `AWS Secret Access Key [None]: ${secretAccessKey}`,
    "Default region name [None]: us-east-1",
    "Default output format [None]: json",
  ].join("\n");
  return (
    <SpaceBetween size="l">
      <div role="alert">
        <Alert type="warning">
          <strong>This is the only time that the secret access key can be viewed or downloaded.</strong> You cannot
          recover it later. However, you can create a new access key any time.
        </Alert>
      </div>
      <AwsKeyValue
        items={[
          {
            label: "Access key",
            value: (
              <>
                <code data-testid="iam-access-key-id">{accessKeyId}</code>
                <AwsCopyButton value={accessKeyId} label="Copy access key ID" />
              </>
            ),
          },
          {
            label: "Secret access key",
            value: (
              <>
                <code data-testid="iam-secret-access-key">{revealed ? secretAccessKey : "*".repeat(40)}</code>
                <AwsButton onClick={() => setRevealed((current) => !current)}>{revealed ? "Hide" : "Show"}</AwsButton>
                <AwsCopyButton value={secretAccessKey} label="Copy secret access key" />
              </>
            ),
          },
        ]}
      />
      <Container header={<Header variant="h3">Use with the AWS Command Line Interface</Header>}>
        <div data-testid="iam-cli-usage">
          <Box variant="pre">
            {configureTranscript}
          </Box>
        </div>
      </Container>
      <Container
        header={
          <Header variant="h3" actions={<AwsCopyButton value={verifyCommand} label="Copy verification command" />}>
            Verify the credentials
          </Header>
        }
      >
        <Box variant="pre">
          {verifyCommand}
        </Box>
      </Container>
    </SpaceBetween>
  );
}

function CreateAccessKeyModal({ userName, onClose }: { userName: string; onClose: () => void }) {
  const queryClient = useQueryClient();
  const create = useMutation({
    mutationFn: () => createIAMAccessKey(userName),
    // The new key is listed (as metadata) the moment it exists, matching what a
    // parallel ListAccessKeys would return.
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["iam-access-keys", userName] }),
  });
  if (create.isSuccess) {
    // Once this dialog closes the secret is gone for good. Cloudscape's own
    // Modal always offers its close control, an overlay click, and Escape —
    // the same as the real console's own "Access key created" dialog, which
    // an operator can just as easily dismiss early and lose the one-time
    // secret.
    return (
      <AwsModal
        title="Access key created"
        onDismiss={onClose}
        footer={
          <AwsButton variant="primary" data-testid="iam-access-key-done" onClick={onClose}>
            Done
          </AwsButton>
        }
      >
        <AccessKeyCreatedPanel
          accessKeyId={create.data.accessKeyId}
          secretAccessKey={create.data.secretAccessKey}
          endpoint={window.location.origin}
        />
      </AwsModal>
    );
  }
  return (
    <AwsModal
      title="Create access key"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="iam-create-access-key-submit"
            disabled={create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create access key"}
          </AwsButton>
        </>
      }
    >
      <p>
        An access key is a long-term credential for <code>{userName}</code>. The secret access key is shown once, at
        creation — store it securely.
      </p>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the access key.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function DeleteAccessKeyModal({
  userName,
  accessKey,
  onClose,
  clearSelection,
}: {
  userName: string;
  accessKey: IAMAccessKeyMetadata;
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => deleteIAMAccessKey(userName, accessKey.accessKeyId),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["iam-access-keys", userName] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={`Delete ${accessKey.accessKeyId}?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="iam-delete-access-key-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>
        Deleting an access key is permanent. Anything still signing requests with it loses access immediately.
        Deactivate the key first to verify nothing depends on it.
      </p>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete the access key.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function UserSummary({ userName }: { userName: string }) {
  const { data, isError, error } = useQuery({ queryKey: ["iam-user", userName], queryFn: () => fetchIAMUser(userName) });
  if (isError) {
    return (
      <AwsContainer>
        <AwsErrorAlert>
          <strong>Could not load the user.</strong> {error instanceof Error ? error.message : "The request failed."}
        </AwsErrorAlert>
      </AwsContainer>
    );
  }
  return (
    <AwsContainer>
      <ColumnLayout columns={3} variant="text-grid">
        <div>
          <Box variant="awsui-key-label">ARN</Box>
          <div>{data?.arn ?? "—"}</div>
        </div>
        <div>
          <Box variant="awsui-key-label">Path</Box>
          <div>{data?.path ?? "—"}</div>
        </div>
        <div>
          <Box variant="awsui-key-label">Created</Box>
          <div>{data?.createDate ?? "—"}</div>
        </div>
      </ColumnLayout>
    </AwsContainer>
  );
}

export function IAMUserSecurityCredentialsPage() {
  const { userName = "" } = useParams();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ key: IAMAccessKeyMetadata; clearSelection: () => void } | null>(null);
  const toggleStatus = useMutation({
    mutationFn: (key: IAMAccessKeyMetadata) =>
      updateIAMAccessKeyStatus(userName, key.accessKeyId, key.status === "Active" ? "Inactive" : "Active"),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["iam-access-keys", userName] }),
  });
  return (
    <>
      <AwsPageHeader title={userName} description="Security credentials for this IAM user." />
      <UserSummary userName={userName} />
      {toggleStatus.isError && (
        <AwsErrorAlert>
          <strong>Could not update the access key.</strong>{" "}
          {toggleStatus.error instanceof Error ? toggleStatus.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
      <AwsResourceTable<IAMAccessKeyMetadata>
        title="Access keys"
        headingVariant="h2"
        description="Use access keys to sign programmatic requests with the AWS CLI or SDKs. The secret is shown only at creation."
        columns={keyColumns}
        queryKey={["iam-access-keys", userName]}
        queryFn={() => fetchIAMAccessKeys(userName)}
        filterPlaceholder="Find access keys"
        emptyTitle="No access keys"
        emptyDescription="This user has no access keys. Create one to use the AWS CLI as this user."
        rowKey={(row) => row.accessKeyId}
        actions={({ selected, clearSelection }) => (
          <>
            <AwsButton
              disabled={selected.length !== 1 || toggleStatus.isPending}
              onClick={() => toggleStatus.mutate(selected[0])}
            >
              {selected.length === 1 && selected[0].status === "Inactive" ? "Activate" : "Deactivate"}
            </AwsButton>
            <AwsButton
              data-testid="iam-delete-access-key"
              disabled={selected.length !== 1}
              onClick={() => setDeleting({ key: selected[0], clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton variant="primary" data-testid="iam-create-access-key" onClick={() => setCreating(true)}>
              Create access key
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateAccessKeyModal userName={userName} onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteAccessKeyModal
          userName={userName}
          accessKey={deleting.key}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
