import { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Wizard from "@cloudscape-design/components/wizard";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Textarea from "@cloudscape-design/components/textarea";
import Tiles from "@cloudscape-design/components/tiles";
import Select, { type SelectProps } from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Box from "@cloudscape-design/components/box";
import Table from "@cloudscape-design/components/table";
import TextFilter from "@cloudscape-design/components/text-filter";
import KeyValuePairs from "@cloudscape-design/components/key-value-pairs";
import {
  AwsButton,
  AwsCopyableText,
  AwsDeleteModal,
  AwsErrorAlert,
  AwsPageHeader,
  AwsRefreshButton,
  AwsResourceTable,
  AwsRowLink,
  AwsTabs,
  errorText,
  useNotify,
  type AwsColumn,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import { usePageTitle } from "../console/AwsConsole.js";
import {
  createIAMRole,
  deleteIAMRole,
  fetchCallerIdentity,
  fetchIAMAttachedRolePolicies,
  fetchIAMOpenIDConnectProviders,
  fetchIAMPolicies,
  fetchIAMRole,
  fetchIAMRoleInlinePolicyNames,
  fetchIAMRoles,
  isAWSManagedPolicy,
  type IAMPolicy,
  type IAMRole,
} from "../api.js";

/** "AWS service: lambda" for a role a service assumes, the way the real
 * console's Trusted entities column summarizes a trust policy. */
export function trustedEntities(document: string): string {
  try {
    const parsed = JSON.parse(document) as { Statement?: { Principal?: Record<string, string | string[]> }[] | { Principal?: Record<string, string | string[]> } };
    const statements = Array.isArray(parsed.Statement) ? parsed.Statement : parsed.Statement ? [parsed.Statement] : [];
    const entities: string[] = [];
    for (const statement of statements) {
      for (const [kind, value] of Object.entries(statement.Principal ?? {})) {
        for (const principal of Array.isArray(value) ? value : [value]) {
          if (kind === "Service") entities.push(`AWS Service: ${principal.replace(/\.amazonaws\.com$/, "")}`);
          else if (kind === "Federated") entities.push(`Identity Provider: ${principal.split(":").slice(5).join(":").replace(/^oidc-provider\//, "")}`);
          else if (kind === "AWS") entities.push(`Account: ${principal.split(":")[4] ?? principal}`);
          else entities.push(`${kind}: ${principal}`);
        }
      }
    }
    return entities.join(", ") || "–";
  } catch {
    return "–";
  }
}

const roleColumns: AwsColumn<IAMRole>[] = [
  {
    id: "roleName",
    header: "Role name",
    cell: (row) => <AwsRowLink to={`/ui/iam/roles/${encodeURIComponent(row.roleName)}`}>{row.roleName}</AwsRowLink>,
    value: (row) => row.roleName,
  },
  {
    id: "trustedEntities",
    header: "Trusted entities",
    cell: (row) => trustedEntities(row.assumeRolePolicyDocument),
    value: (row) => trustedEntities(row.assumeRolePolicyDocument),
  },
  { id: "lastUsed", header: "Last activity", cell: (row) => (row.lastUsedDate ? formatTimestamp(row.lastUsedDate) : "–"), value: (row) => row.lastUsedDate },
  { id: "createDate", header: "Creation time", cell: (row) => formatTimestamp(row.createDate), value: (row) => row.createDate },
];

function DeleteRoleModal({ roleName, onClose, onDeleted }: { roleName: string; onClose: () => void; onDeleted: () => void }) {
  const queryClient = useQueryClient();
  const notify = useNotify();
  const remove = useMutation({
    mutationFn: () => deleteIAMRole(roleName),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["iam-roles"] }),
    onSuccess: () => {
      notify("success", `Role ${roleName} deleted.`);
      onDeleted();
    },
  });
  return (
    <AwsDeleteModal
      title={`Delete ${roleName}?`}
      confirmationText={roleName}
      onDismiss={onClose}
      onDelete={() => remove.mutate()}
      pending={remove.isPending}
      testIdPrefix="iam-delete-role"
      error={remove.isError ? <AwsErrorAlert>{errorText(remove.error)}</AwsErrorAlert> : undefined}
    >
      <Box>Deleting the role detaches its permissions policies and removes its inline policies.</Box>
    </AwsDeleteModal>
  );
}

export function IAMRolesPage() {
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState<{ roleName: string; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<IAMRole>
        title="Roles"
        description="An IAM role is an identity you can create that has specific permissions with credentials that are valid for short durations. Roles can be assumed by entities that you trust."
        columns={roleColumns}
        queryKey={["iam-roles"]}
        queryFn={fetchIAMRoles}
        filterPlaceholder="Search"
        emptyTitle="No roles"
        emptyDescription="No roles to display."
        selectionType="single"
        rowKey={(row) => row.roleName}
        tableTestId="iam-roles-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <AwsButton
              data-testid="iam-delete-role"
              disabled={selected.length !== 1}
              onClick={() => setDeleting({ roleName: selected[0].roleName, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton variant="primary" data-testid="iam-create-role" onClick={() => navigate("/ui/iam/roles/create")}>
              Create role
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <DeleteRoleModal
          roleName={deleting.roleName}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            deleting.clearSelection();
            setDeleting(null);
          }}
        />
      )}
    </>
  );
}

function prettyJson(document: string): string {
  try {
    return JSON.stringify(JSON.parse(document), null, 2);
  } catch {
    return document;
  }
}

export function IAMRoleDetailPage() {
  const { roleName = "" } = useParams();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const role = useQuery({ queryKey: ["iam-role", roleName], queryFn: () => fetchIAMRole(roleName) });
  const attached = useQuery({ queryKey: ["iam-role-attached", roleName], queryFn: () => fetchIAMAttachedRolePolicies(roleName) });
  const inline = useQuery({ queryKey: ["iam-role-inline", roleName], queryFn: () => fetchIAMRoleInlinePolicyNames(roleName) });
  const permissions = [
    ...(attached.data ?? []).map((policy) => ({
      name: policy.policyName,
      type: isAWSManagedPolicy(policy.policyArn) ? "AWS managed" : "Customer managed",
    })),
    ...(inline.data ?? []).map((name) => ({ name, type: "Customer inline" })),
  ];
  return (
    <ContentLayout
      header={
        <AwsPageHeader
          title={roleName}
          actions={
            <AwsButton data-testid="iam-role-delete" disabled={!role.data} onClick={() => setDeleting(true)}>
              Delete
            </AwsButton>
          }
        />
      }
    >
      <SpaceBetween size="l">
        {role.isError ? (
          <AwsErrorAlert testId="iam-role-error">
            <strong>Could not load the role.</strong> {errorText(role.error)}
          </AwsErrorAlert>
        ) : (
          <Container header={<Header variant="h2">Summary</Header>}>
            <KeyValuePairs
              columns={3}
              items={[
                { label: "Creation date", value: role.data ? formatTimestamp(role.data.createDate) : "–" },
                { label: "ARN", value: <AwsCopyableText value={role.data?.arn ?? ""} label="Copy ARN" /> },
                { label: "Last activity", value: role.data?.lastUsedDate ? formatTimestamp(role.data.lastUsedDate) : "–" },
                { label: "Description", value: role.data?.description || "–" },
                { label: "Maximum session duration", value: role.data ? `${role.data.maxSessionDuration / 3600} hour` : "–" },
                { label: "Role ID", value: role.data?.roleId ?? "–" },
              ]}
            />
          </Container>
        )}
        <AwsTabs
          ariaLabel="Role details"
          tabs={[
            {
              id: "permissions",
              label: "Permissions",
              content: (
                <Table
                  variant="container"
                  header={<Header variant="h2" counter={`(${permissions.length})`}>Permissions policies</Header>}
                  loading={attached.isLoading || inline.isLoading}
                  loadingText="Loading permissions policies"
                  items={permissions}
                  columnDefinitions={[
                    { id: "name", header: "Policy name", cell: (row) => row.name, isRowHeader: true },
                    { id: "type", header: "Type", cell: (row) => row.type },
                  ]}
                  empty={<Box textAlign="center">No permissions policies attached.</Box>}
                />
              ),
            },
            {
              id: "trust",
              label: "Trust relationships",
              content: (
                <Container header={<Header variant="h2">Trusted entities</Header>}>
                  <pre className="aws-code" data-testid="iam-role-trust-policy">
                    {role.data ? prettyJson(role.data.assumeRolePolicyDocument) : ""}
                  </pre>
                </Container>
              ),
            },
          ]}
        />
      </SpaceBetween>
      {deleting && <DeleteRoleModal roleName={roleName} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/iam/roles")} />}
    </ContentLayout>
  );
}

type TrustedEntityType = "service" | "account" | "web-identity" | "custom";

const SERVICE_USE_CASES: SelectProps.Option[] = [
  { value: "ec2.amazonaws.com", label: "EC2" },
  { value: "lambda.amazonaws.com", label: "Lambda" },
  { value: "ecs-tasks.amazonaws.com", label: "Elastic Container Service Task" },
  { value: "states.amazonaws.com", label: "Step Functions" },
  { value: "codebuild.amazonaws.com", label: "CodeBuild" },
  { value: "events.amazonaws.com", label: "EventBridge" },
  { value: "scheduler.amazonaws.com", label: "EventBridge Scheduler" },
  { value: "batch.amazonaws.com", label: "Batch" },
];

/** The trust policy the Create role wizard's first step describes. */
export function buildTrustPolicy(
  type: TrustedEntityType,
  options: { service?: string; account?: string; provider?: { arn: string; url: string }; audience?: string; custom?: string },
): string {
  const statement = (principal: Record<string, string>, action: string, condition?: Record<string, unknown>) =>
    JSON.stringify(
      { Version: "2012-10-17", Statement: [{ Effect: "Allow", Principal: principal, Action: action, ...(condition ? { Condition: condition } : {}) }] },
      null,
      2,
    );
  switch (type) {
    case "service":
      return statement({ Service: options.service ?? "" }, "sts:AssumeRole");
    case "account":
      return statement({ AWS: `arn:aws:iam::${options.account ?? ""}:root` }, "sts:AssumeRole");
    case "web-identity": {
      const name = (options.provider?.url ?? "").replace(/^https?:\/\//, "");
      return statement({ Federated: options.provider?.arn ?? "" }, "sts:AssumeRoleWithWebIdentity", {
        StringEquals: { [`${name}:aud`]: options.audience ?? "" },
      });
    }
    default:
      return options.custom ?? "";
  }
}

function isJson(text: string): boolean {
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

const ROLE_NAME_PATTERN = /^[\w+=,.@-]{1,64}$/;

export function IAMCreateRolePage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const notify = useNotify();
  const [step, setStep] = useState(0);
  const [type, setType] = useState<TrustedEntityType>("service");
  const [service, setService] = useState<SelectProps.Option | null>(SERVICE_USE_CASES[0]);
  const [account, setAccount] = useState("");
  const [provider, setProvider] = useState<SelectProps.Option | null>(null);
  const [audience, setAudience] = useState<SelectProps.Option | null>(null);
  const [custom, setCustom] = useState(
    JSON.stringify({ Version: "2012-10-17", Statement: [{ Effect: "Allow", Principal: {}, Action: "sts:AssumeRole" }] }, null, 2),
  );
  const [selectedPolicies, setSelectedPolicies] = useState<IAMPolicy[]>([]);
  const [policyFilter, setPolicyFilter] = useState("");
  const [roleName, setRoleName] = useState("");
  const [description, setDescription] = useState("");
  const [showErrors, setShowErrors] = useState(false);
  usePageTitle("Create role");

  const identity = useQuery({ queryKey: ["sts-caller-identity"], queryFn: fetchCallerIdentity, retry: false });
  const providers = useQuery({ queryKey: ["iam-oidc-providers"], queryFn: fetchIAMOpenIDConnectProviders });
  const policies = useQuery({ queryKey: ["iam-policies"], queryFn: fetchIAMPolicies });
  const chosenProvider = providers.data?.find((entry) => entry.arn === provider?.value);
  const accountId = account || identity.data?.account || "";

  const trustPolicy = buildTrustPolicy(type, {
    service: service?.value,
    account: accountId,
    provider: chosenProvider,
    audience: audience?.value,
    custom,
  });
  const trustValid =
    type === "service"
      ? Boolean(service)
      : type === "account"
        ? /^\d{12}$/.test(accountId)
        : type === "web-identity"
          ? Boolean(chosenProvider && audience)
          : isJson(custom);
  const nameValid = ROLE_NAME_PATTERN.test(roleName);

  const visiblePolicies = useMemo(() => {
    const needle = policyFilter.trim().toLowerCase();
    return (policies.data ?? []).filter((policy) => !needle || policy.policyName.toLowerCase().includes(needle));
  }, [policies.data, policyFilter]);

  const create = useMutation({
    mutationFn: () =>
      createIAMRole({
        roleName,
        description,
        assumeRolePolicyDocument: trustPolicy,
        policyArns: selectedPolicies.map((policy) => policy.arn),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["iam-roles"] });
      notify("success", `Role ${roleName} created.`);
      navigate("/ui/iam/roles");
    },
  });

  return (
    <Wizard
      activeStepIndex={step}
      onNavigate={(event) => {
        if (event.detail.requestedStepIndex > step && step === 0 && !trustValid) {
          setShowErrors(true);
          return;
        }
        setShowErrors(false);
        setStep(event.detail.requestedStepIndex);
      }}
      onCancel={() => navigate("/ui/iam/roles")}
      onSubmit={() => {
        if (!nameValid) {
          setShowErrors(true);
          return;
        }
        create.mutate();
      }}
      isLoadingNextStep={create.isPending}
      submitButtonText="Create role"
      i18nStrings={{
        stepNumberLabel: (stepNumber) => `Step ${stepNumber}`,
        collapsedStepsLabel: (stepNumber, stepsCount) => `Step ${stepNumber} of ${stepsCount}`,
        navigationAriaLabel: "Steps",
        cancelButton: "Cancel",
        previousButton: "Previous",
        nextButton: "Next",
        optional: "optional",
      }}
      steps={[
        {
          title: "Select trusted entity",
          content: (
            <SpaceBetween size="l">
              <Container header={<Header variant="h2">Trusted entity type</Header>}>
                <Tiles
                  value={type}
                  onChange={(event) => setType(event.detail.value as TrustedEntityType)}
                  columns={2}
                  items={[
                    { value: "service", label: "AWS service", description: "Allow AWS services like EC2, Lambda, or others to perform actions in this account." },
                    { value: "account", label: "AWS account", description: "Allow entities in other AWS accounts belonging to you or a 3rd party to perform actions in this account." },
                    { value: "web-identity", label: "Web identity", description: "Allows users federated by the specified external web identity provider to assume this role to perform actions in this account." },
                    { value: "custom", label: "Custom trust policy", description: "Create a custom trust policy to enable others to perform actions in this account." },
                  ]}
                />
              </Container>
              {type === "service" && (
                <Container header={<Header variant="h2">Use case</Header>}>
                  <FormField label="Service or use case">
                    <Select selectedOption={service} options={SERVICE_USE_CASES} onChange={(event) => setService(event.detail.selectedOption)} />
                  </FormField>
                </Container>
              )}
              {type === "account" && (
                <Container header={<Header variant="h2">An AWS account</Header>}>
                  <FormField label="Account ID" errorText={showErrors && !/^\d{12}$/.test(accountId) ? "Enter a 12-digit account ID." : undefined}>
                    <Input value={accountId} onChange={(event) => setAccount(event.detail.value)} />
                  </FormField>
                </Container>
              )}
              {type === "web-identity" && (
                <Container header={<Header variant="h2">Web identity</Header>}>
                  <SpaceBetween size="m">
                    <FormField label="Identity provider" errorText={showErrors && !chosenProvider ? "Choose an identity provider." : undefined}>
                      <Select
                        selectedOption={provider}
                        placeholder="Choose an identity provider"
                        statusType={providers.isLoading ? "loading" : "finished"}
                        options={(providers.data ?? []).map((entry) => ({ value: entry.arn, label: entry.url.replace(/^https?:\/\//, "") }))}
                        onChange={(event) => {
                          setProvider(event.detail.selectedOption);
                          setAudience(null);
                        }}
                      />
                    </FormField>
                    <FormField label="Audience" errorText={showErrors && !audience ? "Choose an audience." : undefined}>
                      <Select
                        selectedOption={audience}
                        placeholder="Choose an audience"
                        disabled={!chosenProvider}
                        options={(chosenProvider?.clientIds ?? []).map((clientId) => ({ value: clientId, label: clientId }))}
                        onChange={(event) => setAudience(event.detail.selectedOption)}
                      />
                    </FormField>
                  </SpaceBetween>
                </Container>
              )}
              {type === "custom" && (
                <Container header={<Header variant="h2">Custom trust policy</Header>}>
                  <FormField label="Trust policy" errorText={showErrors && !isJson(custom) ? "The trust policy is not valid JSON." : undefined}>
                    <Textarea value={custom} rows={12} onChange={(event) => setCustom(event.detail.value)} />
                  </FormField>
                </Container>
              )}
            </SpaceBetween>
          ),
        },
        {
          title: "Add permissions",
          content: (
            <Table<IAMPolicy>
              variant="container"
              header={
                <Header
                  variant="h2"
                  counter={`(${selectedPolicies.length}/${policies.data?.length ?? 0})`}
                  actions={<AwsRefreshButton onClick={() => void policies.refetch()} loading={policies.isFetching} />}
                >
                  Permissions policies
                </Header>
              }
              filter={
                <TextFilter
                  filteringText={policyFilter}
                  filteringPlaceholder="Filter by policy name"
                  filteringAriaLabel="Filter by policy name"
                  onChange={(event) => setPolicyFilter(event.detail.filteringText)}
                />
              }
              selectionType="multi"
              selectedItems={selectedPolicies}
              onSelectionChange={(event) => setSelectedPolicies([...event.detail.selectedItems])}
              trackBy="arn"
              loading={policies.isLoading}
              loadingText="Loading policies"
              items={visiblePolicies}
              ariaLabels={{
                selectionGroupLabel: "Policies selection",
                allItemsSelectionLabel: () => "Select all policies",
                itemSelectionLabel: (_state, policy) => `Select ${policy.policyName}`,
              }}
              columnDefinitions={[
                { id: "name", header: "Policy name", cell: (policy) => policy.policyName, isRowHeader: true },
                { id: "type", header: "Type", cell: (policy) => (isAWSManagedPolicy(policy.arn) ? "AWS managed" : "Customer managed") },
                { id: "description", header: "Description", cell: (policy) => policy.description || "–" },
              ]}
              empty={<Box textAlign="center">No policies to display.</Box>}
            />
          ),
        },
        {
          title: "Name, review, and create",
          content: (
            <SpaceBetween size="l">
              <Container header={<Header variant="h2">Role details</Header>}>
                <SpaceBetween size="m">
                  <FormField
                    label="Role name"
                    description="Enter a meaningful name to identify this role."
                    constraintText="Maximum 64 characters. Use alphanumeric and '+=,.@-_' characters."
                    errorText={showErrors && !nameValid ? "Enter a valid role name." : undefined}
                  >
                    <Input value={roleName} onChange={(event) => setRoleName(event.detail.value)} nativeInputAttributes={{ "data-testid": "iam-role-name-input" }} />
                  </FormField>
                  <FormField label="Description" constraintText="Maximum 1000 characters.">
                    <Textarea value={description} rows={2} onChange={(event) => setDescription(event.detail.value)} />
                  </FormField>
                </SpaceBetween>
              </Container>
              <Container header={<Header variant="h2">Step 1: Select trusted entities</Header>}>
                <pre className="aws-code" data-testid="iam-create-role-trust-policy">
                  {trustPolicy}
                </pre>
              </Container>
              <Container header={<Header variant="h2">Step 2: Add permissions</Header>}>
                {selectedPolicies.length === 0 ? (
                  <Box color="text-body-secondary">No permissions policies selected.</Box>
                ) : (
                  <ul>
                    {selectedPolicies.map((policy) => (
                      <li key={policy.arn}>{policy.policyName}</li>
                    ))}
                  </ul>
                )}
              </Container>
              {create.isError && (
                <AwsErrorAlert>
                  <strong>Failed to create role.</strong> {errorText(create.error)}
                </AwsErrorAlert>
              )}
            </SpaceBetween>
          ),
        },
      ]}
    />
  );
}
