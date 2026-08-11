import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  type AwsColumn,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createGlueGlossary,
  deleteGlueGlossary,
  fetchGlueAssetTypes,
  fetchGlueDatabases,
  fetchGlueGlossaries,
  fetchGlueJobs,
  type GlueAssetType,
  type GlueDatabase,
  type GlueGlossary,
  type GlueJob,
} from "../api.js";

// AWS Glue — Data Catalog databases and ETL jobs, the two resources the real
// Glue console leads with. GetDatabases and GetJobs on the real Glue API
// (X-Amz-Target AWSGlue.<Op>).

const databaseColumns: AwsColumn<GlueDatabase>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  { id: "locationUri", header: "Location", cell: (row) => row.locationUri || "–", value: (row) => row.locationUri },
  {
    id: "createTime",
    header: "Created",
    cell: (row) => formatEpoch(row.createTime),
    value: (row) => String(row.createTime),
  },
];

const jobColumns: AwsColumn<GlueJob>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "role", header: "IAM role", cell: (row) => row.role || "–", value: (row) => row.role },
  { id: "glueVersion", header: "Glue version", cell: (row) => row.glueVersion || "–", value: (row) => row.glueVersion },
  { id: "workerType", header: "Worker type", cell: (row) => row.workerType || "–", value: (row) => row.workerType },
  {
    id: "scriptLocation",
    header: "Script location",
    cell: (row) => row.scriptLocation || "–",
    value: (row) => row.scriptLocation,
  },
  {
    id: "createdOn",
    header: "Created",
    cell: (row) => formatEpoch(row.createdOn),
    value: (row) => String(row.createdOn),
  },
];

const glossaryColumns: AwsColumn<GlueGlossary>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "Glossary ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
];

const assetTypeColumns: AwsColumn<GlueAssetType>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "Asset type ID", cell: (row) => row.id, value: (row) => row.id },
];

function CreateGlossaryModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => createGlueGlossary(name.trim(), description.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["glue-glossaries"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create business glossary"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="glue-create-glossary-submit"
            disabled={!name.trim() || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create glossary"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>Business glossaries organize controlled terminology that can be associated with Data Catalog assets.</p>
        <FormField label="Name">
          <Input
            value={name}
            onChange={(event) => setName(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "glue-glossary-name" }}
          />
        </FormField>
        <FormField label="Description - optional">
          <Input
            value={description}
            onChange={(event) => setDescription(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "glue-glossary-description" }}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            {create.error instanceof Error ? create.error.message : "The business glossary could not be created."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function DeleteGlossaryModal({ glossary, onClose }: { glossary: GlueGlossary; onClose: () => void }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => deleteGlueGlossary(glossary.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["glue-glossaries"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title={`Delete ${glossary.name}?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="glue-delete-glossary-submit"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>A glossary can be deleted only after all of its terms have been removed.</p>
        {remove.isError && (
          <AwsErrorAlert>
            {remove.error instanceof Error ? remove.error.message : "The business glossary could not be deleted."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function GluePage() {
  const [createGlossary, setCreateGlossary] = useState(false);
  const [deleteGlossary, setDeleteGlossary] = useState<GlueGlossary | null>(null);
  return (
    <>
      <AwsResourceTable<GlueDatabase>
        title="Databases"
        description="AWS Glue Data Catalog databases in this account and Region."
        columns={databaseColumns}
        queryKey={["glue-databases"]}
        queryFn={fetchGlueDatabases}
        filterPlaceholder="Find databases"
        emptyTitle="No databases"
        emptyDescription="No AWS Glue databases exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="glue-databases-table"
        errorTestId="glue-databases-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      <AwsResourceTable<GlueJob>
        title="ETL jobs"
        headingVariant="h2"
        description="AWS Glue extract, transform, and load jobs."
        columns={jobColumns}
        queryKey={["glue-jobs"]}
        queryFn={fetchGlueJobs}
        filterPlaceholder="Find jobs"
        emptyTitle="No jobs"
        emptyDescription="No AWS Glue jobs exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="glue-jobs-table"
        errorTestId="glue-jobs-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      <AwsResourceTable<GlueGlossary>
        title="Business glossaries"
        headingVariant="h2"
        description="Controlled vocabulary associated with AWS Glue Data Catalog assets."
        columns={glossaryColumns}
        queryKey={["glue-glossaries"]}
        queryFn={fetchGlueGlossaries}
        filterPlaceholder="Find business glossaries"
        emptyTitle="No business glossaries"
        emptyDescription="No business glossaries exist in this account and Region."
        rowKey={(row) => row.id}
        tableTestId="glue-glossaries-table"
        errorTestId="glue-glossaries-error"
        actions={({ selected, refetch, isFetching }) => (
          <>
            <AwsButton data-testid="glue-create-glossary" onClick={() => setCreateGlossary(true)}>
              Create glossary
            </AwsButton>
            <AwsButton
              data-testid="glue-delete-glossary"
              disabled={selected.length !== 1}
              onClick={() => setDeleteGlossary(selected[0])}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<GlueAssetType>
        title="Asset types"
        headingVariant="h2"
        description="Business-context schemas available to Data Catalog assets."
        columns={assetTypeColumns}
        queryKey={["glue-asset-types"]}
        queryFn={fetchGlueAssetTypes}
        filterPlaceholder="Find asset types"
        emptyTitle="No asset types"
        emptyDescription="No asset types exist in this account and Region."
        rowKey={(row) => row.id}
        tableTestId="glue-asset-types-table"
        errorTestId="glue-asset-types-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {createGlossary && <CreateGlossaryModal onClose={() => setCreateGlossary(false)} />}
      {deleteGlossary && (
        <DeleteGlossaryModal glossary={deleteGlossary} onClose={() => setDeleteGlossary(null)} />
      )}
    </>
  );
}
