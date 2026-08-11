import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createSSMParameter,
  deleteSSMParameter,
  fetchSSMDocuments,
  fetchSSMParameters,
  type SSMDocument,
  type SSMParameter,
} from "../api.js";

// AWS Systems Manager — Parameter Store and Documents. DescribeParameters,
// PutParameter, DeleteParameter, and ListDocuments on the real Systems Manager
// API (X-Amz-Target AmazonSSM.<Op>).

const parameterColumns: AwsColumn<SSMParameter>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "tier", header: "Tier", cell: (row) => row.tier || "–", value: (row) => row.tier },
  { id: "version", header: "Version", cell: (row) => String(row.version), value: (row) => String(row.version) },
  { id: "dataType", header: "Data type", cell: (row) => row.dataType || "–", value: (row) => row.dataType },
  {
    id: "lastModifiedDate",
    header: "Last modified",
    cell: (row) => formatEpoch(row.lastModifiedDate),
    value: (row) => String(row.lastModifiedDate),
  },
];

const documentColumns: AwsColumn<SSMDocument>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "documentType", header: "Document type", cell: (row) => row.documentType, value: (row) => row.documentType },
  { id: "owner", header: "Owner", cell: (row) => row.owner || "–", value: (row) => row.owner },
  {
    id: "documentFormat",
    header: "Format",
    cell: (row) => row.documentFormat || "–",
    value: (row) => row.documentFormat,
  },
  {
    id: "platformTypes",
    header: "Platform types",
    cell: (row) => row.platformTypes.join(", ") || "–",
    value: (row) => row.platformTypes.join(", "),
  },
];

const PARAMETER_TYPES = [
  { label: "String", value: "String" },
  { label: "StringList", value: "StringList" },
  { label: "SecureString", value: "SecureString" },
];

function CreateParameterModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [type, setType] = useState("String");
  const create = useMutation({
    mutationFn: () => createSSMParameter(name.trim(), value, type as "String" | "StringList" | "SecureString"),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ssm-parameters"] });
      onClose();
    },
  });
  const valid = name.trim().length > 0 && value.length > 0;
  return (
    <AwsModal
      title="Create parameter"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ssm-create-parameter-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create parameter"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>A parameter stores configuration data or a secret, addressed by a hierarchical name.</p>
        <FormField label="Name" constraintText="A hierarchical name, for example /app/prod/database-url.">
          <Input
            value={name}
            onChange={(event) => setName(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "ssm-parameter-name-input" }}
          />
        </FormField>
        <FormField label="Type">
          <Select
            selectedOption={PARAMETER_TYPES.find((option) => option.value === type) ?? PARAMETER_TYPES[0]}
            options={PARAMETER_TYPES}
            ariaLabel="Parameter type"
            onChange={(event) => setType(event.detail.selectedOption.value ?? "String")}
            data-testid="ssm-parameter-type"
          />
        </FormField>
        <FormField label="Value">
          <Input
            value={value}
            onChange={(event) => setValue(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "ssm-parameter-value-input" }}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the parameter.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function DeleteParametersModal({
  parameters,
  onClose,
  clearSelection,
}: {
  parameters: SSMParameter[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const parameter of parameters) {
        await deleteSSMParameter(parameter.name);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ssm-parameters"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={parameters.length === 1 ? `Delete ${parameters[0].name}?` : `Delete ${parameters.length} parameters?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ssm-delete-parameter-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a parameter is permanent and removes every version of it.</p>
      <ul>
        {parameters.map((parameter) => (
          <li key={parameter.name}>
            <code>{parameter.name}</code>
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

export function SystemsManagerPage() {
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ parameters: SSMParameter[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<SSMParameter>
        title="Parameter Store"
        description="Systems Manager parameters in this account and Region."
        columns={parameterColumns}
        queryKey={["ssm-parameters"]}
        queryFn={fetchSSMParameters}
        filterPlaceholder="Find parameters"
        emptyTitle="No parameters"
        emptyDescription="No Systems Manager parameters exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="ssm-parameters-table"
        errorTestId="ssm-parameters-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="ssm-delete-parameter"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ parameters: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="ssm-create-parameter" onClick={() => setCreating(true)}>
              Create parameter
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<SSMDocument>
        title="Documents"
        headingVariant="h2"
        description="Systems Manager documents in this account and Region."
        columns={documentColumns}
        queryKey={["ssm-documents"]}
        queryFn={fetchSSMDocuments}
        filterPlaceholder="Find documents"
        emptyTitle="No documents"
        emptyDescription="No Systems Manager documents exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="ssm-documents-table"
        errorTestId="ssm-documents-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {creating && <CreateParameterModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteParametersModal
          parameters={deleting.parameters}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
