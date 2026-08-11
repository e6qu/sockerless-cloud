import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AzureResourceTable, AzureErrorMessage, type AzureColumn } from "../portal/index.js";
import { resourceGroupOf, locationLabel } from "../portal/format.js";
import {
  createContainerAppJob,
  deleteContainerAppJob,
  fetchContainerAppJobs,
  fetchSubscriptions,
  type ContainerAppJob,
} from "../api.js";
import { ContainerAppJobCreateForm } from "./ContainerAppJobForms.js";

const columns: AzureColumn<ContainerAppJob>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link to={`/ui/container-apps/${encodeURIComponent(row.name)}`}>{row.name}</Link>,
    value: (row) => row.name,
  },
  { id: "resourceGroup", header: "Resource group", cell: (row) => resourceGroupOf(row.id), value: (row) => resourceGroupOf(row.id) },
  { id: "location", header: "Location", cell: (row) => locationLabel(row.location), value: (row) => row.location },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
];

// The Container App jobs blade: the real Microsoft.App/jobs list across every
// subscription this directory can reach, and a Create flow that provisions a
// new job (its resource group and managed environment first, the same order
// `az containerapp job create` uses) through the real jobs PUT.
export function ContainerAppsPage() {
  const queryClient = useQueryClient();
  const { data: subscriptions } = useQuery({ queryKey: ["subscriptions"], queryFn: fetchSubscriptions });
  const [creating, setCreating] = useState(false);

  const create = useMutation({
    mutationFn: createContainerAppJob,
    onSuccess: () => {
      setCreating(false);
      void queryClient.invalidateQueries({ queryKey: ["ca-jobs"] });
    },
  });

  return (
    <AzureResourceTable<ContainerAppJob>
      columns={columns}
      queryKey={["ca-jobs"]}
      queryFn={fetchContainerAppJobs}
      filterPlaceholder="Filter by name"
      resourceNoun="Container App jobs"
      emptyTitle="No Container App jobs to display"
      emptyDescription="Jobs created in this subscription appear here."
      rowKey={(row) => row.id}
      essentials={(rows) => [
        { label: "Subscription", value: "Simulator" },
        { label: "Jobs", value: String(rows.length) },
        { label: "Locations", value: new Set(rows.map((row) => row.location)).size || "—" },
      ]}
      extraCommands={[{ label: "Create", icon: "add", testid: "ca-create", onSelect: () => setCreating(true) }]}
      onDelete={async (jobs) => {
        for (const job of jobs) {
          await deleteContainerAppJob(job.id);
        }
      }}
      deleteWarning="Deleting a Container App job is permanent and removes its execution history. This action can't be undone."
      deleteTestId="ca-delete"
      banner={
        <>
          {create.isError ? (
            <AzureErrorMessage testid="ca-create-error">
              <strong>The Container App job could not be created.</strong>{" "}
              {create.error instanceof Error ? create.error.message : "Azure Resource Manager did not respond."}
            </AzureErrorMessage>
          ) : null}
          {creating ? (
            <ContainerAppJobCreateForm
              subscriptions={subscriptions ?? []}
              busy={create.isPending}
              onCreate={(input) => create.mutate(input)}
              onDismiss={() => setCreating(false)}
            />
          ) : null}
        </>
      }
    />
  );
}
