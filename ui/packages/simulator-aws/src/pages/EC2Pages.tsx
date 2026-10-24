import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Box from "@cloudscape-design/components/box";
import Link from "@cloudscape-design/components/link";
import Form from "@cloudscape-design/components/form";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Tiles from "@cloudscape-design/components/tiles";
import RadioGroup from "@cloudscape-design/components/radio-group";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Button from "@cloudscape-design/components/button";
import {
  AwsButton,
  AwsCopyableText,
  AwsDeleteModal,
  AwsErrorAlert,
  AwsPageHeader,
  AwsRefreshButton,
  AwsResourceTable,
  AwsStatus,
  errorText,
  useNotify,
  type AwsColumn,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import {
  createEC2KeyPair,
  deleteEC2KeyPair,
  deleteEC2SecurityGroup,
  fetchAutoScalingGroups,
  fetchEC2Instances,
  fetchEC2KeyPairs,
  fetchEC2SecurityGroups,
  fetchEC2Volumes,
  fetchLoadBalancers,
  type EC2KeyPair,
  type EC2SecurityGroup,
  type EC2Volume,
} from "../api.js";

/** The EC2 Dashboard's Resources panel: a count per resource type, each read
 * from the operation its own page reads, linking to that page. */
export function EC2DashboardPage() {
  const navigate = useNavigate();
  const resources = [
    { label: "Instances (running)", href: "/ui/ec2", queryKey: ["ec2-instances"], queryFn: fetchEC2Instances, count: (rows: unknown[]) => (rows as { state: string }[]).filter((row) => row.state === "running").length },
    { label: "Instances", href: "/ui/ec2", queryKey: ["ec2-instances"], queryFn: fetchEC2Instances },
    { label: "Volumes", href: "/ui/ec2/volumes", queryKey: ["ec2-volumes"], queryFn: fetchEC2Volumes },
    { label: "Security groups", href: "/ui/ec2/security-groups", queryKey: ["ec2-security-groups"], queryFn: () => fetchEC2SecurityGroups() },
    { label: "Key pairs", href: "/ui/ec2/key-pairs", queryKey: ["ec2-key-pairs"], queryFn: fetchEC2KeyPairs },
    { label: "Load balancers", href: "/ui/elb", queryKey: ["elb-load-balancers"], queryFn: fetchLoadBalancers },
    { label: "Auto Scaling Groups", href: "/ui/autoscaling", queryKey: ["autoscaling-groups"], queryFn: fetchAutoScalingGroups },
  ];
  const results = useQueries({
    queries: resources.map((resource) => ({ queryKey: resource.queryKey, queryFn: resource.queryFn as () => Promise<unknown[]> })),
  });
  const failure = results.find((result) => result.isError);
  return (
    <ContentLayout header={<AwsPageHeader title="Dashboard" description="View the Amazon EC2 resources in this Region." />}>
      <Container header={<Header variant="h2">Resources</Header>}>
        {failure ? (
          <AwsErrorAlert>
            <strong>Could not load your resources.</strong> {errorText(failure.error)}
          </AwsErrorAlert>
        ) : (
          <ColumnLayout columns={3} variant="text-grid">
            {resources.map((resource, index) => {
              const rows = results[index].data;
              const value = rows === undefined ? undefined : resource.count ? resource.count(rows) : rows.length;
              return (
                <div key={resource.label}>
                  <Box variant="awsui-key-label">{resource.label}</Box>
                  <Link
                    href={resource.href}
                    variant="awsui-value-large"
                    ariaLabel={`${resource.label}: ${value ?? "loading"}`}
                    onFollow={(event) => {
                      event.preventDefault();
                      navigate(resource.href);
                    }}
                  >
                    {value === undefined ? "–" : String(value)}
                  </Link>
                </div>
              );
            })}
          </ColumnLayout>
        )}
      </Container>
    </ContentLayout>
  );
}

const volumeColumns: AwsColumn<EC2Volume>[] = [
  { id: "volumeId", header: "Volume ID", cell: (row) => row.volumeId, value: (row) => row.volumeId },
  { id: "volumeType", header: "Type", cell: (row) => row.volumeType, value: (row) => row.volumeType },
  { id: "size", header: "Size", cell: (row) => `${row.size} GiB`, value: (row) => String(row.size) },
  { id: "state", header: "Volume state", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "availabilityZone", header: "Availability Zone", cell: (row) => row.availabilityZone, value: (row) => row.availabilityZone },
  { id: "encrypted", header: "Encryption", cell: (row) => (row.encrypted ? "Encrypted" : "Not encrypted"), value: (row) => (row.encrypted ? "Encrypted" : "Not encrypted") },
  { id: "createTime", header: "Created", cell: (row) => formatTimestamp(row.createTime), value: (row) => row.createTime },
];

export function EC2VolumesPage() {
  return (
    <AwsResourceTable<EC2Volume>
      title="Volumes"
      columns={volumeColumns}
      queryKey={["ec2-volumes"]}
      queryFn={fetchEC2Volumes}
      filterPlaceholder="Search"
      emptyTitle="No volumes"
      emptyDescription="You do not have any volumes in this Region."
      rowKey={(row) => row.volumeId}
      tableTestId="ec2-volumes-table"
      errorTestId="ec2-volumes-error"
    />
  );
}

const securityGroupColumns: AwsColumn<EC2SecurityGroup>[] = [
  { id: "groupId", header: "Security group ID", cell: (row) => row.groupId, value: (row) => row.groupId },
  { id: "groupName", header: "Security group name", cell: (row) => row.groupName, value: (row) => row.groupName },
  { id: "vpcId", header: "VPC ID", cell: (row) => row.vpcId || "–", value: (row) => row.vpcId },
  { id: "description", header: "Description", cell: (row) => row.description, value: (row) => row.description },
];

export function EC2SecurityGroupsPage() {
  const queryClient = useQueryClient();
  const notify = useNotify();
  const [deleting, setDeleting] = useState<{ groups: EC2SecurityGroup[]; clearSelection: () => void } | null>(null);
  const remove = useMutation({
    mutationFn: async (groups: EC2SecurityGroup[]) => {
      for (const group of groups) await deleteEC2SecurityGroup(group.groupId);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ec2-security-groups"] }),
    onSuccess: (_data, groups) => {
      notify("success", `Successfully deleted ${groups.map((group) => group.groupId).join(", ")}`);
      deleting?.clearSelection();
      setDeleting(null);
    },
  });
  return (
    <>
      <AwsResourceTable<EC2SecurityGroup>
        title="Security Groups"
        columns={securityGroupColumns}
        queryKey={["ec2-security-groups"]}
        queryFn={() => fetchEC2SecurityGroups()}
        filterPlaceholder="Find resources by attribute or tag"
        emptyTitle="No security groups"
        emptyDescription="You do not have any security groups in this Region."
        rowKey={(row) => row.groupId}
        tableTestId="ec2-security-groups-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <AwsButton
              data-testid="ec2-delete-security-group"
              disabled={selected.length === 0}
              onClick={() => {
                remove.reset();
                setDeleting({ groups: selected, clearSelection });
              }}
            >
              Delete security groups
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <AwsDeleteModal
          title="Delete security groups"
          confirmationText="delete"
          onDismiss={() => setDeleting(null)}
          onDelete={() => remove.mutate(deleting.groups)}
          pending={remove.isPending}
          testIdPrefix="ec2-delete-security-group"
          error={remove.isError ? <AwsErrorAlert>{errorText(remove.error)}</AwsErrorAlert> : undefined}
        >
          <Box>Delete the following security groups?</Box>
          <ul>
            {deleting.groups.map((group) => (
              <li key={group.groupId}>
                {group.groupId} – {group.groupName}
              </li>
            ))}
          </ul>
        </AwsDeleteModal>
      )}
    </>
  );
}

const keyPairColumns: AwsColumn<EC2KeyPair>[] = [
  { id: "keyName", header: "Name", cell: (row) => row.keyName, value: (row) => row.keyName },
  { id: "keyType", header: "Type", cell: (row) => row.keyType, value: (row) => row.keyType },
  { id: "createTime", header: "Created", cell: (row) => formatTimestamp(row.createTime), value: (row) => row.createTime },
  {
    id: "keyFingerprint",
    header: "Fingerprint",
    cell: (row) => <AwsCopyableText value={row.keyFingerprint} label="Copy fingerprint" />,
    value: (row) => row.keyFingerprint,
  },
  { id: "keyPairId", header: "ID", cell: (row) => row.keyPairId, value: (row) => row.keyPairId },
];

export function EC2KeyPairsPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const notify = useNotify();
  const [deleting, setDeleting] = useState<{ pairs: EC2KeyPair[]; clearSelection: () => void } | null>(null);
  const remove = useMutation({
    mutationFn: async (pairs: EC2KeyPair[]) => {
      for (const pair of pairs) await deleteEC2KeyPair(pair.keyPairId);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ec2-key-pairs"] }),
    onSuccess: (_data, pairs) => {
      notify("success", `Successfully deleted ${pairs.length === 1 ? `key pair ${pairs[0].keyName}` : `${pairs.length} key pairs`}`);
      deleting?.clearSelection();
      setDeleting(null);
    },
  });
  return (
    <>
      <AwsResourceTable<EC2KeyPair>
        title="Key pairs"
        columns={keyPairColumns}
        queryKey={["ec2-key-pairs"]}
        queryFn={fetchEC2KeyPairs}
        filterPlaceholder="Filter key pairs"
        emptyTitle="No key pairs"
        emptyDescription="You do not have any key pairs in this Region."
        emptyAction={<Button onClick={() => navigate("/ui/ec2/key-pairs/create")}>Create key pair</Button>}
        rowKey={(row) => row.keyPairId}
        tableTestId="ec2-key-pairs-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <AwsButton
              data-testid="ec2-delete-key-pair"
              disabled={selected.length === 0}
              onClick={() => {
                remove.reset();
                setDeleting({ pairs: selected, clearSelection });
              }}
            >
              Delete
            </AwsButton>
            <AwsButton variant="primary" data-testid="ec2-create-key-pair" onClick={() => navigate("/ui/ec2/key-pairs/create")}>
              Create key pair
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <AwsDeleteModal
          title={deleting.pairs.length === 1 ? `Delete ${deleting.pairs[0].keyName}?` : `Delete ${deleting.pairs.length} key pairs?`}
          confirmationText="Delete"
          onDismiss={() => setDeleting(null)}
          onDelete={() => remove.mutate(deleting.pairs)}
          pending={remove.isPending}
          testIdPrefix="ec2-delete-key-pair"
          error={remove.isError ? <AwsErrorAlert>{errorText(remove.error)}</AwsErrorAlert> : undefined}
        >
          <Box>Instances launched with a deleted key pair keep the public key, but you can no longer launch instances with it.</Box>
        </AwsDeleteModal>
      )}
    </>
  );
}

const KEY_NAME_PATTERN = /^[\x20-\x7E]{1,255}$/;

/** Saves the private key the way the real console does after CreateKeyPair:
 * as a file download the browser keeps, since AWS never returns it again. */
function downloadPrivateKey(name: string, format: "pem" | "ppk", material: string) {
  const url = URL.createObjectURL(new Blob([material], { type: "application/x-pem-file" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `${name}.${format}`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

export function EC2CreateKeyPairPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const notify = useNotify();
  const [name, setName] = useState("");
  const [keyType, setKeyType] = useState<"rsa" | "ed25519">("rsa");
  const [keyFormat, setKeyFormat] = useState<"pem" | "ppk">("pem");
  const [touched, setTouched] = useState(false);
  const valid = KEY_NAME_PATTERN.test(name);
  const create = useMutation({
    mutationFn: () => createEC2KeyPair({ keyName: name, keyType, keyFormat }),
    onSuccess: async (created) => {
      downloadPrivateKey(created.keyName, keyFormat, created.keyMaterial);
      await queryClient.invalidateQueries({ queryKey: ["ec2-key-pairs"] });
      notify("success", `Successfully created key pair ${created.keyName}`);
      navigate("/ui/ec2/key-pairs");
    },
  });
  return (
    <ContentLayout
      header={
        <AwsPageHeader
          title="Create key pair"
          description="A key pair, consisting of a private key and a public key, is a set of security credentials that you use to prove your identity when connecting to an instance."
        />
      }
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setTouched(true);
          if (valid) create.mutate();
        }}
      >
        <Form
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button formAction="none" variant="link" onClick={() => navigate("/ui/ec2/key-pairs")}>
                Cancel
              </Button>
              <Button variant="primary" loading={create.isPending} nativeButtonAttributes={{ "data-testid": "ec2-create-key-pair-submit" }}>
                Create key pair
              </Button>
            </SpaceBetween>
          }
          errorText={create.isError ? errorText(create.error) : undefined}
        >
          <Container header={<Header variant="h2">Key pair</Header>}>
            <SpaceBetween size="l">
              <FormField
                label="Name"
                constraintText="The name can include up to 255 ASCII characters. It can't include leading or trailing spaces."
                errorText={touched && !valid ? "Enter a key pair name." : undefined}
              >
                <Input
                  value={name}
                  placeholder="Enter key pair name"
                  onChange={(event) => setName(event.detail.value)}
                  nativeInputAttributes={{ "data-testid": "ec2-key-pair-name-input" }}
                />
              </FormField>
              <FormField label="Key pair type">
                <Tiles
                  value={keyType}
                  onChange={(event) => setKeyType(event.detail.value as "rsa" | "ed25519")}
                  items={[
                    { value: "rsa", label: "RSA", description: "RSA encrypted private and public key pair" },
                    { value: "ed25519", label: "ED25519", description: "ED25519 encrypted private and public key pair" },
                  ]}
                />
              </FormField>
              <FormField label="Private key file format">
                <RadioGroup
                  value={keyFormat}
                  onChange={(event) => setKeyFormat(event.detail.value as "pem" | "ppk")}
                  items={[
                    { value: "pem", label: ".pem", description: "For use with OpenSSH" },
                    { value: "ppk", label: ".ppk", description: "For use with PuTTY" },
                  ]}
                />
              </FormField>
            </SpaceBetween>
          </Container>
        </Form>
      </form>
    </ContentLayout>
  );
}
