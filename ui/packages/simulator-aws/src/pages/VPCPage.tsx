import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Checkbox from "@cloudscape-design/components/checkbox";
import FormField from "@cloudscape-design/components/form-field";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { useNavigate } from "react-router";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  type AwsColumn,
  type AwsStatusKind,
} from "../console/index.js";
import {
  fetchEC2AccountVpcEncryptionControl,
  fetchEC2Vpcs,
  modifyEC2AccountVpcEncryptionControl,
  type EC2AccountVpcEncryptionExclusion,
  type EC2AccountVpcEncryptionMode,
  type EC2Vpc,
} from "../api.js";

// Amazon Virtual Private Cloud (VPC) — Your VPCs. DescribeVpcs on the real
// Amazon EC2 Query API, which is the API the VPC console reads: VPC has no API
// of its own, its resources live in the EC2 API surface.

const columns: AwsColumn<EC2Vpc>[] = [
  { id: "name", header: "Name", cell: (row) => row.name || "–", value: (row) => row.name },
  {
    id: "vpcId",
    header: "VPC ID",
    cell: (row) => <AwsRowLink to={`/ui/vpc/${encodeURIComponent(row.vpcId)}`}>{row.vpcId}</AwsRowLink>,
    value: (row) => row.vpcId,
  },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "cidrBlock", header: "IPv4 CIDR", cell: (row) => row.cidrBlock, value: (row) => row.cidrBlock },
  {
    id: "isDefault",
    header: "Default VPC",
    cell: (row) => (row.isDefault ? "Yes" : "No"),
    value: (row) => (row.isDefault ? "Yes" : "No"),
  },
  { id: "tenancy", header: "Tenancy", cell: (row) => row.instanceTenancy || "–", value: (row) => row.instanceTenancy },
];

const exclusionLabels: Record<EC2AccountVpcEncryptionExclusion, string> = {
  InternetGateway: "Internet gateways",
  EgressOnlyInternetGateway: "Egress-only internet gateways",
  NatGateway: "NAT gateways",
  VirtualPrivateGateway: "Virtual private gateways",
  VpcPeering: "VPC peering connections",
  Lambda: "AWS Lambda functions",
  VpcLattice: "Amazon VPC Lattice",
  ElasticFileSystem: "Amazon Elastic File System (EFS)",
};

const emptyExclusions = (): Record<EC2AccountVpcEncryptionExclusion, boolean> =>
  Object.fromEntries(Object.keys(exclusionLabels).map((key) => [key, false])) as Record<
    EC2AccountVpcEncryptionExclusion,
    boolean
  >;

function accountEncryptionStatusKind(state: string): AwsStatusKind {
  if (state === "transitions-successful") return "success";
  if (state === "transitions-in-progress") return "warning";
  if (state === "transitions-failed" || state === "transitions-partially-successful") return "error";
  return "inactive";
}

function AccountEncryptionControlsModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const control = useQuery({
    queryKey: ["ec2-account-vpc-encryption-control"],
    queryFn: fetchEC2AccountVpcEncryptionControl,
  });
  const [mode, setMode] = useState<EC2AccountVpcEncryptionMode>("unmanaged");
  const [exclusions, setExclusions] =
    useState<Record<EC2AccountVpcEncryptionExclusion, boolean>>(emptyExclusions);

  useEffect(() => {
    if (!control.data) return;
    setMode(control.data.mode);
    setExclusions(control.data.exclusions);
  }, [control.data]);

  const save = useMutation({
    mutationFn: () => modifyEC2AccountVpcEncryptionControl(mode, exclusions),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["ec2-account-vpc-encryption-control"] }),
        queryClient.invalidateQueries({ queryKey: ["ec2-vpcs"] }),
      ]);
      onClose();
    },
  });

  return (
    <AwsModal
      title="Account-level VPC encryption controls"
      size="large"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="vpc-account-encryption-save"
            disabled={control.isLoading || control.isError || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Saving…" : "Save changes"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        {control.data && (
          <div>
            <strong>State</strong>{" "}
            <AwsStatus status={control.data.state} kind={accountEncryptionStatusKind(control.data.state)} />
            <p>
              Managed by {control.data.managedBy}
              {control.data.lastUpdateTimestamp
                ? ` · Last updated ${new Date(control.data.lastUpdateTimestamp).toLocaleString()}`
                : ""}
            </p>
          </div>
        )}
        <FormField
          label="Encryption mode"
          description="Apply one regional account policy to existing VPCs and VPCs created later."
        >
          <Select
            data-testid="vpc-account-encryption-mode"
            selectedOption={{
              label:
                mode === "attempt-enforce" ? "Attempt enforce" : mode === "attempt-monitor" ? "Attempt monitor" : "Unmanaged",
              value: mode,
            }}
            options={[
              { label: "Unmanaged", value: "unmanaged" },
              { label: "Attempt monitor", value: "attempt-monitor" },
              { label: "Attempt enforce", value: "attempt-enforce" },
            ]}
            onChange={(event) => setMode(event.detail.selectedOption.value as EC2AccountVpcEncryptionMode)}
          />
        </FormField>
        <FormField
          label="Resource exclusions"
          description="Excluded resource traffic remains the account owner's encryption responsibility."
        >
          <SpaceBetween size="xs">
            {(Object.keys(exclusionLabels) as EC2AccountVpcEncryptionExclusion[]).map((field) => (
              <Checkbox
                key={field}
                checked={exclusions[field]}
                onChange={(event) => setExclusions((current) => ({ ...current, [field]: event.detail.checked }))}
              >
                {exclusionLabels[field]}
              </Checkbox>
            ))}
          </SpaceBetween>
        </FormField>
        {(control.isError || save.isError) && (
          <AwsErrorAlert>
            {control.error instanceof Error
              ? control.error.message
              : save.error instanceof Error
                ? save.error.message
                : "The account-level VPC encryption controls could not be updated."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function VPCPage() {
  const navigate = useNavigate();
  const [showAccountEncryption, setShowAccountEncryption] = useState(false);
  return (
    <>
      <AwsResourceTable<EC2Vpc>
        title="Your VPCs"
        description="Virtual private clouds in this account and Region."
        columns={columns}
        queryKey={["ec2-vpcs"]}
        queryFn={fetchEC2Vpcs}
        filterPlaceholder="Find VPCs"
        emptyTitle="No VPCs"
        emptyDescription="No virtual private clouds exist in this account and Region."
        rowKey={(row) => row.vpcId}
        tableTestId="vpc-table"
        errorTestId="vpc-error"
        actions={({ selected, refetch, isFetching }) => (
          <>
            <AwsButton data-testid="vpc-account-encryption-open" onClick={() => setShowAccountEncryption(true)}>
              Account encryption controls
            </AwsButton>
            <AwsButton
              data-testid="vpc-view-vpc"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/vpc/${encodeURIComponent(selected[0].vpcId)}`)}
            >
              View details
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      {showAccountEncryption && <AccountEncryptionControlsModal onClose={() => setShowAccountEncryption(false)} />}
    </>
  );
}
