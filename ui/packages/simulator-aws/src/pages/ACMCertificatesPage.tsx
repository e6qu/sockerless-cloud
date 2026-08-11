import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Tabs from "@cloudscape-design/components/tabs";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createACMAcmeDomainValidation,
  createACMAcmeEndpoint,
  createACMAcmeExternalAccountBinding,
  deleteACMAcmeDomainValidation,
  deleteACMAcmeEndpoint,
  deleteACMCertificate,
  fetchACMAcmeAccounts,
  fetchACMAcmeDomainValidations,
  fetchACMAcmeEndpoints,
  fetchACMAcmeExternalAccountBindings,
  fetchACMCertificates,
  getACMAcmeExternalAccountBindingCredentials,
  revokeACMAcmeAccount,
  revokeACMAcmeExternalAccountBinding,
  type ACMAcmeEndpoint,
  type ACMCertificate,
} from "../api.js";

// AWS Certificate Manager — Certificates. ListCertificates and
// DeleteCertificate on the real ACM API (X-Amz-Target CertificateManager.<Op>).

const columns: AwsColumn<ACMCertificate>[] = [
  { id: "domainName", header: "Domain name", cell: (row) => row.domainName, value: (row) => row.domainName },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "type", header: "Type", cell: (row) => row.type || "–", value: (row) => row.type },
  {
    id: "keyAlgorithm",
    header: "Key algorithm",
    cell: (row) => row.keyAlgorithm || "–",
    value: (row) => row.keyAlgorithm,
  },
  {
    id: "inUseBy",
    header: "In use",
    cell: (row) => (row.inUseBy.length > 0 ? "Yes" : "No"),
    value: (row) => (row.inUseBy.length > 0 ? "Yes" : "No"),
  },
  {
    id: "notAfter",
    header: "Expires",
    cell: (row) => formatEpoch(row.notAfter),
    value: (row) => String(row.notAfter),
  },
];

function DeleteCertificatesModal({
  certificates,
  onClose,
  clearSelection,
}: {
  certificates: ACMCertificate[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const certificate of certificates) {
        await deleteACMCertificate(certificate.certificateArn);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["acm-certificates"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={
        certificates.length === 1 ? `Delete ${certificates[0].domainName}?` : `Delete ${certificates.length} certificates?`
      }
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="acm-delete-certificate-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>A certificate still associated with another AWS resource cannot be deleted until the association is removed.</p>
      <ul>
        {certificates.map((certificate) => (
          <li key={certificate.certificateArn}>
            <code>{certificate.domainName}</code>
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

function CertificatesPane() {
  const [deleting, setDeleting] = useState<{ certificates: ACMCertificate[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<ACMCertificate>
        title="Certificates"
        description="AWS Certificate Manager certificates in this account and Region."
        columns={columns}
        queryKey={["acm-certificates"]}
        queryFn={fetchACMCertificates}
        filterPlaceholder="Find certificates"
        emptyTitle="No certificates"
        emptyDescription="No AWS Certificate Manager certificates exist in this account and Region."
        rowKey={(row) => row.certificateArn}
        tableTestId="acm-table"
        errorTestId="acm-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="acm-delete-certificate"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ certificates: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <DeleteCertificatesModal
          certificates={deleting.certificates}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

const endpointColumns: AwsColumn<ACMAcmeEndpoint>[] = [
  {
    id: "arn",
    header: "Endpoint ARN",
    cell: (row) => row.acmeEndpointArn,
    value: (row) => row.acmeEndpointArn,
  },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "contact", header: "Contact", cell: (row) => row.contact || "NOT_REQUIRED", value: (row) => row.contact },
  {
    id: "createdAt",
    header: "Created",
    cell: (row) => formatEpoch(row.createdAt),
    value: (row) => String(row.createdAt),
  },
];

function CreateEndpointModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [contact, setContact] = useState<"REQUIRED" | "NOT_REQUIRED">("REQUIRED");
  const create = useMutation({
    mutationFn: () => createACMAcmeEndpoint(contact),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["acm-acme-endpoints"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create ACME endpoint"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="acm-create-acme-endpoint-submit"
            disabled={create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create endpoint"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>
          The endpoint runs the RFC 8555 ACME protocol. Domains must be pre-approved and clients register with
          external account binding credentials.
        </p>
        <FormField label="Account contact requirement">
          <Select
            selectedOption={{ label: contact === "REQUIRED" ? "Required" : "Not required", value: contact }}
            options={[
              { label: "Required", value: "REQUIRED" },
              { label: "Not required", value: "NOT_REQUIRED" },
            ]}
            onChange={(event) => setContact((event.detail.selectedOption.value ?? "REQUIRED") as typeof contact)}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the endpoint.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function EndpointDetailModal({ endpoint, onClose }: { endpoint: ACMAcmeEndpoint; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [domainName, setDomainName] = useState("");
  const [hostedZoneId, setHostedZoneId] = useState("");
  const [roleArn, setRoleArn] = useState("");
  const [credentials, setCredentials] = useState<{ keyId: string; macKey: string } | null>(null);
  const validations = useQuery({
    queryKey: ["acm-acme-domain-validations", endpoint.acmeEndpointArn],
    queryFn: () => fetchACMAcmeDomainValidations(endpoint.acmeEndpointArn),
  });
  const bindings = useQuery({
    queryKey: ["acm-acme-bindings", endpoint.acmeEndpointArn],
    queryFn: () => fetchACMAcmeExternalAccountBindings(endpoint.acmeEndpointArn),
  });
  const accounts = useQuery({
    queryKey: ["acm-acme-accounts", endpoint.acmeEndpointArn],
    queryFn: () => fetchACMAcmeAccounts(endpoint.acmeEndpointArn),
  });
  const createDomain = useMutation({
    mutationFn: () => createACMAcmeDomainValidation(endpoint.acmeEndpointArn, domainName.trim(), hostedZoneId.trim()),
    onSuccess: async () => {
      setDomainName("");
      setHostedZoneId("");
      await queryClient.invalidateQueries({ queryKey: ["acm-acme-domain-validations", endpoint.acmeEndpointArn] });
    },
  });
  const removeDomain = useMutation({
    mutationFn: deleteACMAcmeDomainValidation,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["acm-acme-domain-validations", endpoint.acmeEndpointArn] }),
  });
  const createBinding = useMutation({
    mutationFn: async () => {
      const binding = await createACMAcmeExternalAccountBinding(endpoint.acmeEndpointArn, roleArn.trim());
      return getACMAcmeExternalAccountBindingCredentials(binding.acmeExternalAccountBindingArn);
    },
    onSuccess: async (createdCredentials) => {
      setCredentials(createdCredentials);
      setRoleArn("");
      await queryClient.invalidateQueries({ queryKey: ["acm-acme-bindings", endpoint.acmeEndpointArn] });
    },
  });
  const revokeBinding = useMutation({
    mutationFn: revokeACMAcmeExternalAccountBinding,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["acm-acme-bindings", endpoint.acmeEndpointArn] }),
  });
  const revokeAccount = useMutation({
    mutationFn: (accountUrl: string) => revokeACMAcmeAccount(endpoint.acmeEndpointArn, accountUrl),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["acm-acme-accounts", endpoint.acmeEndpointArn] }),
  });
  const error =
    validations.error ||
    bindings.error ||
    accounts.error ||
    createDomain.error ||
    removeDomain.error ||
    createBinding.error ||
    revokeBinding.error ||
    revokeAccount.error;
  return (
    <AwsModal title="ACME endpoint" onDismiss={onClose} footer={<AwsButton onClick={onClose}>Close</AwsButton>}>
      <SpaceBetween size="l">
        <div>
          <h3>Endpoint URL</h3>
          <code data-testid="acm-acme-directory-url">{endpoint.endpointUrl}</code>
        </div>
        <div>
          <h3>Pre-approved domains</h3>
          <SpaceBetween size="s">
            <FormField label="Domain name">
              <Input value={domainName} onChange={(event) => setDomainName(event.detail.value)} />
            </FormField>
            <FormField label="Route 53 hosted zone ID" description="Optional. ACM creates the validation CNAME when supplied.">
              <Input value={hostedZoneId} onChange={(event) => setHostedZoneId(event.detail.value)} />
            </FormField>
            <AwsButton
              variant="primary"
              disabled={!domainName.trim() || createDomain.isPending}
              onClick={() => createDomain.mutate()}
            >
              Add domain
            </AwsButton>
            {(validations.data ?? []).map((validation) => (
              <div key={validation.acmeDomainValidationArn}>
                <AwsStatus status={validation.status} /> <strong>{validation.domainName}</strong>
                <div>
                  <code>{validation.recordName}</code> → <code>{validation.recordValue}</code>
                </div>
                <AwsButton onClick={() => removeDomain.mutate(validation.acmeDomainValidationArn)}>Delete</AwsButton>
              </div>
            ))}
          </SpaceBetween>
        </div>
        <div>
          <h3>External account bindings</h3>
          <SpaceBetween size="s">
            <FormField label="IAM role ARN">
              <Input value={roleArn} onChange={(event) => setRoleArn(event.detail.value)} />
            </FormField>
            <AwsButton
              variant="primary"
              disabled={!roleArn.trim() || createBinding.isPending}
              onClick={() => createBinding.mutate()}
            >
              Create binding
            </AwsButton>
            {credentials && (
              <div data-testid="acm-acme-credentials">
                <p>Copy these credentials now and configure them in the ACME client.</p>
                <div>Key ID: <code>{credentials.keyId}</code></div>
                <div>MAC key: <code>{credentials.macKey}</code></div>
              </div>
            )}
            {(bindings.data ?? []).map((binding) => (
              <div key={binding.acmeExternalAccountBindingArn}>
                <code>{binding.roleArn}</code>{" "}
                <AwsStatus status={binding.revokedAt ? "REVOKED" : "ACTIVE"} />
                {!binding.revokedAt && (
                  <AwsButton onClick={() => revokeBinding.mutate(binding.acmeExternalAccountBindingArn)}>Revoke</AwsButton>
                )}
              </div>
            ))}
          </SpaceBetween>
        </div>
        <div>
          <h3>Registered accounts</h3>
          {(accounts.data ?? []).length === 0 ? (
            <p>No ACME clients have registered with this endpoint.</p>
          ) : (
            (accounts.data ?? []).map((account) => (
              <div key={account.accountUrl}>
                <AwsStatus status={account.status} /> <code>{account.accountUrl}</code>
                <div>{account.contacts.join(", ") || "No contact"}</div>
                {account.status === "VALID" && (
                  <AwsButton onClick={() => revokeAccount.mutate(account.accountUrl)}>Revoke account</AwsButton>
                )}
              </div>
            ))
          )}
        </div>
        {error && (
          <AwsErrorAlert>
            <strong>The ACME request failed.</strong> {error instanceof Error ? error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function ACMEEndpointsPane() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [managing, setManaging] = useState<ACMAcmeEndpoint | null>(null);
  const remove = useMutation({
    mutationFn: async (endpoints: ACMAcmeEndpoint[]) => {
      for (const endpoint of endpoints) {
        await deleteACMAcmeEndpoint(endpoint.acmeEndpointArn);
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["acm-acme-endpoints"] }),
  });
  return (
    <>
      <AwsResourceTable<ACMAcmeEndpoint>
        title="ACME endpoints"
        description="Managed RFC 8555 endpoints for certificates used on customer-managed infrastructure."
        columns={endpointColumns}
        queryKey={["acm-acme-endpoints"]}
        queryFn={fetchACMAcmeEndpoints}
        filterPlaceholder="Find ACME endpoints"
        emptyTitle="No ACME endpoints"
        emptyDescription="Create an endpoint, pre-approve domains, and issue external account binding credentials."
        rowKey={(row) => row.acmeEndpointArn}
        tableTestId="acm-acme-endpoints-table"
        errorTestId="acm-acme-endpoints-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton variant="primary" data-testid="acm-create-acme-endpoint" onClick={() => setCreating(true)}>
              Create endpoint
            </AwsButton>
            <AwsButton disabled={selected.length !== 1} onClick={() => setManaging(selected[0] ?? null)}>
              View details
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || remove.isPending}
              onClick={() => {
                remove.mutate(selected);
                clearSelection();
              }}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateEndpointModal onClose={() => setCreating(false)} />}
      {managing && <EndpointDetailModal endpoint={managing} onClose={() => setManaging(null)} />}
    </>
  );
}

export function ACMCertificatesPage() {
  const [activeTabId, setActiveTabId] = useState("certificates");
  return (
    <Tabs
      ariaLabel="AWS Certificate Manager resources"
      activeTabId={activeTabId}
      onChange={(event) => setActiveTabId(event.detail.activeTabId)}
      tabs={[
        { id: "certificates", label: "Certificates", content: <CertificatesPane /> },
        { id: "acme", label: "ACME endpoints", content: <ACMEEndpointsPane /> },
      ]}
    />
  );
}
