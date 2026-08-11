import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import {
  createLambdaFunction,
  deleteLambdaFunction,
  fetchLambdaFunctions,
  type CreateLambdaFunctionInput,
  type LambdaFunction,
} from "../api.js";

// AWS Lambda — Functions. The list and Delete both go through the real
// Lambda REST-JSON API (GET /2015-03-31/functions for the table, DELETE
// /2015-03-31/functions/{FunctionName} for the header action) with the
// operator's federated credentials.

const columns: AwsColumn<LambdaFunction>[] = [
  {
    id: "name",
    header: "Function name",
    cell: (row) => <AwsRowLink to={`/ui/lambda/${encodeURIComponent(row.name)}`}>{row.name}</AwsRowLink>,
    value: (row) => row.name,
  },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "runtime", header: "Runtime", cell: (row) => row.runtime, value: (row) => row.runtime },
  { id: "memorySize", header: "Memory", cell: (row) => `${row.memorySize} MB`, value: (row) => String(row.memorySize) },
  { id: "timeout", header: "Timeout", cell: (row) => `${row.timeout} s`, value: (row) => String(row.timeout) },
  { id: "lastModified", header: "Last modified", cell: (row) => formatTimestamp(row.lastModified), value: (row) => row.lastModified },
];

// The function-name shape real Lambda enforces on CreateFunction: 1–64
// characters of letters, numbers, hyphens, and underscores.
const LAMBDA_NAME_PATTERN = /^[a-zA-Z0-9-_]{1,64}$/;

const PACKAGE_TYPES = [
  { label: "Container image", value: "Image" },
  { label: "Zip archive (from Amazon S3)", value: "Zip" },
] as const;

const LAMBDA_RUNTIMES = [
  "nodejs20.x",
  "nodejs22.x",
  "python3.12",
  "python3.13",
  "java21",
  "go1.x",
  "dotnet8",
  "ruby3.3",
];

function CreateFunctionModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [packageType, setPackageType] = useState<"Image" | "Zip">("Image");
  const [imageUri, setImageUri] = useState("");
  const [runtime, setRuntime] = useState(LAMBDA_RUNTIMES[0]);
  const [handler, setHandler] = useState("");
  const [s3Bucket, setS3Bucket] = useState("");
  const [s3Key, setS3Key] = useState("");
  const [role, setRole] = useState("");
  const [memory, setMemory] = useState("128");
  const [timeout, setTimeout] = useState("3");

  const memoryValue = Number(memory);
  const timeoutValue = Number(timeout);
  const nameValid = LAMBDA_NAME_PATTERN.test(name.trim());
  const memoryValid = Number.isInteger(memoryValue) && memoryValue >= 128 && memoryValue <= 10240;
  const timeoutValid = Number.isInteger(timeoutValue) && timeoutValue >= 1 && timeoutValue <= 900;
  const roleValid = role.trim().length > 0;
  const packageValid =
    packageType === "Image"
      ? imageUri.trim().length > 0
      : handler.trim().length > 0 && s3Bucket.trim().length > 0 && s3Key.trim().length > 0;
  const valid = nameValid && memoryValid && timeoutValid && roleValid && packageValid;

  const create = useMutation({
    mutationFn: () => {
      const input: CreateLambdaFunctionInput = {
        functionName: name.trim(),
        role: role.trim(),
        memorySize: memoryValue,
        timeout: timeoutValue,
        packageType,
      };
      if (packageType === "Image") {
        input.imageUri = imageUri.trim();
      } else {
        input.runtime = runtime;
        input.handler = handler.trim();
        input.s3Bucket = s3Bucket.trim();
        input.s3Key = s3Key.trim();
      }
      return createLambdaFunction(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["lambda-functions"] });
      onClose();
    },
  });

  return (
    <AwsModal
      title="Create function"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="lambda-create-function-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create function"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField
          label="Function name"
          constraintText="1–64 characters. Letters, numbers, hyphens, and underscores."
        >
          <Input
            value={name}
            onChange={(event) => setName(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "lambda-function-name-input" }}
          />
        </FormField>
        <FormField label="Package type">
          <Select
            selectedOption={PACKAGE_TYPES.find((option) => option.value === packageType) ?? null}
            options={PACKAGE_TYPES as unknown as { label: string; value: string }[]}
            onChange={(event) => setPackageType(event.detail.selectedOption.value as "Image" | "Zip")}
            ariaLabel="Package type"
            data-testid="lambda-package-type"
          />
        </FormField>
        {packageType === "Image" ? (
          <FormField label="Container image URI" description="An image in Amazon ECR the function runs from.">
            <Input
              value={imageUri}
              onChange={(event) => setImageUri(event.detail.value)}
              placeholder="123456789012.dkr.ecr.us-east-1.amazonaws.com/my-image:latest"
              nativeInputAttributes={{ "data-testid": "lambda-image-uri-input" }}
            />
          </FormField>
        ) : (
          <>
            <FormField label="Runtime">
              <Select
                selectedOption={{ label: runtime, value: runtime }}
                options={LAMBDA_RUNTIMES.map((value) => ({ label: value, value }))}
                onChange={(event) => setRuntime(event.detail.selectedOption.value ?? runtime)}
                ariaLabel="Runtime"
                data-testid="lambda-runtime"
              />
            </FormField>
            <FormField label="Handler" description="The entry point, e.g. index.handler.">
              <Input
                value={handler}
                onChange={(event) => setHandler(event.detail.value)}
                nativeInputAttributes={{ "data-testid": "lambda-handler-input" }}
              />
            </FormField>
            <FormField label="Code S3 bucket">
              <Input
                value={s3Bucket}
                onChange={(event) => setS3Bucket(event.detail.value)}
                nativeInputAttributes={{ "data-testid": "lambda-s3-bucket-input" }}
              />
            </FormField>
            <FormField label="Code S3 key">
              <Input
                value={s3Key}
                onChange={(event) => setS3Key(event.detail.value)}
                nativeInputAttributes={{ "data-testid": "lambda-s3-key-input" }}
              />
            </FormField>
          </>
        )}
        <FormField label="Execution role ARN" description="The IAM role the function assumes.">
          <Input
            value={role}
            onChange={(event) => setRole(event.detail.value)}
            placeholder="arn:aws:iam::123456789012:role/lambda-role"
            nativeInputAttributes={{ "data-testid": "lambda-role-input" }}
          />
        </FormField>
        <FormField label="Memory" constraintText="128–10240 MB.">
          <Input
            type="number"
            value={memory}
            onChange={(event) => setMemory(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "lambda-memory-input" }}
          />
        </FormField>
        <FormField label="Timeout" constraintText="1–900 seconds.">
          <Input
            type="number"
            value={timeout}
            onChange={(event) => setTimeout(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "lambda-timeout-input" }}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the function.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function DeleteFunctionsModal({
  functions,
  onClose,
  clearSelection,
}: {
  functions: LambdaFunction[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    // DeleteFunction is per-function on the wire; a failure part-way (a
    // function another resource — an event source mapping — still references)
    // surfaces as the real API error, with the already-deleted functions gone
    // from the refreshed list.
    mutationFn: async () => {
      for (const fn of functions) {
        await deleteLambdaFunction(fn.name);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["lambda-functions"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={functions.length === 1 ? `Delete ${functions[0].name}?` : `Delete ${functions.length} functions?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="lambda-delete-function-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a function is permanent. Its versions, aliases, and event source mappings are deleted with it.</p>
      <ul>
        {functions.map((fn) => (
          <li key={fn.name}>
            <code>{fn.name}</code>
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

export function LambdaFunctionsPage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ functions: LambdaFunction[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<LambdaFunction>
        title="Functions"
        description="Functions in this account and Region."
        columns={columns}
        queryKey={["lambda-functions"]}
        queryFn={fetchLambdaFunctions}
        filterPlaceholder="Find functions"
        emptyTitle="No functions"
        emptyDescription="No functions exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="lambda-functions-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="lambda-view-function"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/lambda/${encodeURIComponent(selected[0].name)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="lambda-delete-function"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ functions: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="lambda-create-function" onClick={() => setCreating(true)}>
              Create function
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateFunctionModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteFunctionsModal
          functions={deleting.functions}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
