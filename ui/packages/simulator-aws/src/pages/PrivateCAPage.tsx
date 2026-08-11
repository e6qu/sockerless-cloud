import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createAndActivateRootPrivateCA,
  deletePrivateCertificateAuthority,
  fetchPrivateCertificateAuthorities,
  restorePrivateCertificateAuthority,
  setPrivateCertificateAuthorityEnabled,
  type PrivateCertificateAuthority,
} from "../api.js";

const columns: AwsColumn<PrivateCertificateAuthority>[] = [
  {
    id: "commonName",
    header: "Common name",
    cell: (row) => row.commonName || "–",
    value: (row) => row.commonName,
  },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  {
    id: "algorithm",
    header: "Key and signing algorithm",
    cell: (row) => `${row.keyAlgorithm} / ${row.signingAlgorithm}`,
    value: (row) => `${row.keyAlgorithm} ${row.signingAlgorithm}`,
  },
  {
    id: "expires",
    header: "Expires",
    cell: (row) => row.notAfter ? formatEpoch(row.notAfter) : "–",
    value: (row) => String(row.notAfter),
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatEpoch(row.createdAt),
    value: (row) => String(row.createdAt),
  },
];

function CreatePrivateCAModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [commonName, setCommonName] = useState("");
  const [organization, setOrganization] = useState("");
  const [keyAlgorithm, setKeyAlgorithm] = useState<"RSA_2048" | "RSA_3072" | "RSA_4096">("RSA_2048");
  const [validityYears, setValidityYears] = useState("10");
  const create = useMutation({
    mutationFn: () => createAndActivateRootPrivateCA({
      commonName,
      organization,
      keyAlgorithm,
      validityYears: Number(validityYears),
    }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["private-certificate-authorities"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create root certificate authority"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="private-ca-create-submit"
            disabled={!commonName || !organization || Number(validityYears) < 1 || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating and activating…" : "Create CA"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>
          AWS creates the private key, generates a certificate signing request, signs the root certificate,
          and imports it to activate the authority.
        </p>
        <FormField label="Common name">
          <Input value={commonName} onChange={(event) => setCommonName(event.detail.value)} />
        </FormField>
        <FormField label="Organization">
          <Input value={organization} onChange={(event) => setOrganization(event.detail.value)} />
        </FormField>
        <FormField label="Key algorithm">
          <Select
            selectedOption={{ label: keyAlgorithm, value: keyAlgorithm }}
            options={["RSA_2048", "RSA_3072", "RSA_4096"].map((value) => ({ label: value, value }))}
            onChange={(event) => setKeyAlgorithm(
              (event.detail.selectedOption.value ?? "RSA_2048") as typeof keyAlgorithm,
            )}
          />
        </FormField>
        <FormField label="Validity period (years)">
          <Input
            type="number"
            value={validityYears}
            onChange={(event) => setValidityYears(event.detail.value)}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the certificate authority.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function PrivateCAPage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const mutate = useMutation({
    mutationFn: async ({ authorities, action }: {
      authorities: PrivateCertificateAuthority[];
      action: "enable" | "disable" | "delete" | "restore";
    }) => {
      for (const authority of authorities) {
        if (action === "delete") await deletePrivateCertificateAuthority(authority.arn);
        else if (action === "restore") await restorePrivateCertificateAuthority(authority.arn);
        else await setPrivateCertificateAuthorityEnabled(authority.arn, action === "enable");
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["private-certificate-authorities"] }),
  });
  return (
    <>
      <AwsResourceTable<PrivateCertificateAuthority>
        title="Private certificate authorities"
        description="AWS Private Certificate Authority resources in this account and Region."
        columns={columns}
        queryKey={["private-certificate-authorities"]}
        queryFn={fetchPrivateCertificateAuthorities}
        filterPlaceholder="Find certificate authorities"
        emptyTitle="No private certificate authorities"
        emptyDescription="Create a private certificate authority to issue private certificates through AWS Certificate Manager."
        rowKey={(row) => row.arn}
        tableTestId="private-ca-table"
        errorTestId="private-ca-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate({ authorities: selected, action: "enable" })}
            >
              Enable
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate({ authorities: selected, action: "disable" })}
            >
              Disable
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate({ authorities: selected, action: "restore" })}
            >
              Restore
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate(
                { authorities: selected, action: "delete" },
                { onSuccess: clearSelection },
              )}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" onClick={() => setCreating(true)}>Create CA</AwsButton>
          </>
        )}
      />
      {creating && <CreatePrivateCAModal onClose={() => setCreating(false)} />}
    </>
  );
}
