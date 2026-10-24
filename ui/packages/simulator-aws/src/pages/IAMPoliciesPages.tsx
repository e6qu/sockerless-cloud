import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import Form from "@cloudscape-design/components/form";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Textarea from "@cloudscape-design/components/textarea";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Button from "@cloudscape-design/components/button";
import Box from "@cloudscape-design/components/box";
import {
  AwsButton,
  AwsDeleteModal,
  AwsErrorAlert,
  AwsPageHeader,
  AwsRefreshButton,
  AwsResourceTable,
  errorText,
  useNotify,
  type AwsColumn,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import {
  createIAMPolicy,
  deleteIAMPolicy,
  fetchIAMOpenIDConnectProviders,
  fetchIAMPolicies,
  isAWSManagedPolicy,
  type IAMOpenIDConnectProvider,
  type IAMPolicy,
} from "../api.js";

const policyType = (policy: IAMPolicy) => (isAWSManagedPolicy(policy.arn) ? "AWS managed" : "Customer managed");
const usedAs = (policy: IAMPolicy) => (policy.attachmentCount > 0 ? `Permissions policy (${policy.attachmentCount})` : "None");

const policyColumns: AwsColumn<IAMPolicy>[] = [
  { id: "policyName", header: "Policy name", cell: (row) => row.policyName, value: (row) => row.policyName },
  { id: "type", header: "Type", cell: policyType, value: policyType },
  { id: "usedAs", header: "Used as", cell: usedAs, value: usedAs },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  { id: "createDate", header: "Creation time", cell: (row) => formatTimestamp(row.createDate), value: (row) => row.createDate },
];

export function IAMPoliciesPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const notify = useNotify();
  const [deleting, setDeleting] = useState<{ policy: IAMPolicy; clearSelection: () => void } | null>(null);
  const remove = useMutation({
    mutationFn: (policy: IAMPolicy) => deleteIAMPolicy(policy.arn),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["iam-policies"] }),
    onSuccess: (_data, policy) => {
      notify("success", `Policy ${policy.policyName} deleted.`);
      deleting?.clearSelection();
      setDeleting(null);
    },
  });
  return (
    <>
      <AwsResourceTable<IAMPolicy>
        title="Policies"
        description="A policy is an object in AWS that defines permissions."
        columns={policyColumns}
        queryKey={["iam-policies"]}
        queryFn={fetchIAMPolicies}
        filterPlaceholder="Filter by Type, Policy name, or Description"
        emptyTitle="No policies"
        emptyDescription="No policies to display."
        selectionType="single"
        rowKey={(row) => row.arn}
        tableTestId="iam-policies-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <AwsButton
              data-testid="iam-delete-policy"
              disabled={selected.length !== 1 || isAWSManagedPolicy(selected[0].arn)}
              onClick={() => {
                remove.reset();
                setDeleting({ policy: selected[0], clearSelection });
              }}
            >
              Delete
            </AwsButton>
            <AwsButton variant="primary" data-testid="iam-create-policy" onClick={() => navigate("/ui/iam/policies/create")}>
              Create policy
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <AwsDeleteModal
          title={`Delete ${deleting.policy.policyName}?`}
          confirmationText={deleting.policy.policyName}
          onDismiss={() => setDeleting(null)}
          onDelete={() => remove.mutate(deleting.policy)}
          pending={remove.isPending}
          testIdPrefix="iam-delete-policy"
          error={remove.isError ? <AwsErrorAlert>{errorText(remove.error)}</AwsErrorAlert> : undefined}
        >
          <Box>IAM refuses to delete a policy that is still attached to a user, group, or role.</Box>
        </AwsDeleteModal>
      )}
    </>
  );
}

const STARTER_POLICY = JSON.stringify(
  { Version: "2012-10-17", Statement: [{ Sid: "Statement1", Effect: "Allow", Action: [], Resource: [] }] },
  null,
  4,
);

const POLICY_NAME_PATTERN = /^[\w+=,.@-]{1,128}$/;

export function IAMCreatePolicyPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const notify = useNotify();
  const [document, setDocument] = useState(STARTER_POLICY);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [touched, setTouched] = useState(false);
  let documentError = "";
  try {
    JSON.parse(document);
  } catch (error) {
    documentError = error instanceof Error ? error.message : "The policy is not valid JSON.";
  }
  const nameValid = POLICY_NAME_PATTERN.test(name);
  const create = useMutation({
    mutationFn: () => createIAMPolicy({ policyName: name, description, document }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["iam-policies"] });
      notify("success", `Policy ${name} created.`);
      navigate("/ui/iam/policies");
    },
  });
  return (
    <ContentLayout header={<AwsPageHeader title="Create policy" description="Add permissions by writing a policy document in JSON." />}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setTouched(true);
          if (nameValid && !documentError) create.mutate();
        }}
      >
        <Form
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button formAction="none" variant="link" onClick={() => navigate("/ui/iam/policies")}>
                Cancel
              </Button>
              <Button variant="primary" loading={create.isPending} nativeButtonAttributes={{ "data-testid": "iam-create-policy-submit" }}>
                Create policy
              </Button>
            </SpaceBetween>
          }
          errorText={create.isError ? errorText(create.error) : undefined}
        >
          <SpaceBetween size="l">
            <Container header={<Header variant="h2">Policy editor</Header>}>
              <FormField label="Policy document" errorText={touched && documentError ? documentError : undefined} stretch>
                <Textarea
                  value={document}
                  rows={14}
                  onChange={(event) => setDocument(event.detail.value)}
                  spellcheck={false}
                />
              </FormField>
            </Container>
            <Container header={<Header variant="h2">Policy details</Header>}>
              <SpaceBetween size="m">
                <FormField
                  label="Policy name"
                  constraintText="Maximum 128 characters. Use alphanumeric and '+=,.@-_' characters."
                  errorText={touched && !nameValid ? "Enter a valid policy name." : undefined}
                >
                  <Input value={name} onChange={(event) => setName(event.detail.value)} nativeInputAttributes={{ "data-testid": "iam-policy-name-input" }} />
                </FormField>
                <FormField label="Description - optional" constraintText="Maximum 1,000 characters.">
                  <Textarea value={description} rows={2} onChange={(event) => setDescription(event.detail.value)} />
                </FormField>
              </SpaceBetween>
            </Container>
          </SpaceBetween>
        </Form>
      </form>
    </ContentLayout>
  );
}

const providerColumns: AwsColumn<IAMOpenIDConnectProvider>[] = [
  { id: "url", header: "Provider", cell: (row) => row.url.replace(/^https?:\/\//, ""), value: (row) => row.url },
  { id: "type", header: "Type", cell: () => "OpenID Connect", value: () => "OpenID Connect" },
  { id: "audiences", header: "Audiences", cell: (row) => row.clientIds.join(", ") || "–", value: (row) => row.clientIds.join(", ") },
  { id: "createDate", header: "Creation time", cell: (row) => formatTimestamp(row.createDate), value: (row) => row.createDate },
];

export function IAMIdentityProvidersPage() {
  return (
    <AwsResourceTable<IAMOpenIDConnectProvider>
      title="Identity providers"
      description="Use an identity provider (IdP) to manage your user identities outside of AWS, and give these external user identities permissions to use AWS resources in your account."
      columns={providerColumns}
      queryKey={["iam-oidc-providers"]}
      queryFn={fetchIAMOpenIDConnectProviders}
      filterPlaceholder="Filter identity providers by property or value"
      emptyTitle="No identity providers"
      emptyDescription="No identity providers to display."
      rowKey={(row) => row.arn}
      tableTestId="iam-identity-providers-table"
    />
  );
}
