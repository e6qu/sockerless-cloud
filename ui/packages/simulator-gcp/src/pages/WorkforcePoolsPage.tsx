import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { LoadError, Loading } from "../console/GcpResourceDetail.js";
import { ResourceTableView } from "../console/GcpTable.js";
import { shortName } from "../console/format.js";
import { useProject } from "../console/project.js";
import {
  fetchProject,
  fetchWorkforcePoolProviders,
  fetchWorkforcePools,
  type WorkforcePool,
  type WorkforcePoolProvider,
} from "../api.js";

const stateLabel = (state: string | undefined, disabled: boolean | undefined) =>
  disabled ? "Disabled" : state ? state.charAt(0) + state.slice(1).toLowerCase() : "Unknown";

// Workforce Identity Federation: the organization's workforce pools and the
// identity providers in the selected pool, the federation this console signs
// its operators in through.
export function WorkforcePoolsPage() {
  const { project } = useProject();
  const [selected, setSelected] = useState<string | null>(null);
  const crm = useQuery({ queryKey: ["crm-project", project], queryFn: () => fetchProject(project) });
  const organization = crm.data?.parent?.startsWith("organizations/") ? crm.data.parent : undefined;
  const pools = useQuery({
    queryKey: ["workforce-pools", organization],
    queryFn: () => fetchWorkforcePools(organization!),
    enabled: Boolean(organization),
  });
  const pool = selected ?? pools.data?.[0]?.name ?? null;
  const providers = useQuery({
    queryKey: ["workforce-pool-providers", pool],
    queryFn: () => fetchWorkforcePoolProviders(pool!),
    enabled: Boolean(pool),
  });

  return (
    <>
      <GcpPageHeader
        title="Workforce Identity Federation"
        description={organization ? `Workforce pools of ${organization}` : undefined}
        onRefresh={() => {
          void pools.refetch();
          void providers.refetch();
        }}
        refreshing={pools.isFetching || providers.isFetching}
      />
      {crm.isError ? (
        <LoadError error={crm.error} noun="the project" />
      ) : crm.isLoading ? (
        <Loading noun="project" />
      ) : !organization ? (
        <p className="gc-tab-intro">This project has no organization. Workforce pools belong to an organization.</p>
      ) : (
        <>
          <ResourceTableView<WorkforcePool>
            rows={pools.data}
            isLoading={pools.isLoading}
            error={pools.error}
            tableId="workforce-pools-table"
            resourceNoun="workforce pools"
            rowKey={(row) => row.name}
            columns={[
              {
                id: "displayName",
                header: "Display name",
                cell: (row) => (
                  <button
                    type="button"
                    className="gc-link-button"
                    aria-pressed={row.name === pool}
                    onClick={() => setSelected(row.name)}
                  >
                    {row.displayName || shortName(row.name)}
                  </button>
                ),
                value: (row) => row.displayName ?? "",
              },
              { id: "id", header: "Pool ID", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
              {
                id: "state",
                header: "Status",
                cell: (row) => <GcpStatus status={stateLabel(row.state, row.disabled)} />,
                value: (row) => stateLabel(row.state, row.disabled),
              },
              { id: "session", header: "Session duration", cell: (row) => row.sessionDuration ?? "—", value: (row) => row.sessionDuration ?? "" },
              { id: "description", header: "Description", cell: (row) => row.description || "—", value: (row) => row.description ?? "" },
            ]}
            empty={{ headline: "No workforce pools", description: "A workforce pool federates an external identity provider's users." }}
          />
          {pool ? (
            <>
              <h2 className="gc-section-title">Providers in {shortName(pool)}</h2>
              <ResourceTableView<WorkforcePoolProvider>
                rows={providers.data}
                isLoading={providers.isLoading}
                error={providers.error}
                tableId="workforce-providers-table"
                resourceNoun="providers"
                rowKey={(row) => row.name}
                columns={[
                  { id: "displayName", header: "Display name", cell: (row) => row.displayName || shortName(row.name), value: (row) => row.displayName ?? "" },
                  { id: "id", header: "Provider ID", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
                  { id: "protocol", header: "Protocol", cell: (row) => (row.oidc ? "OIDC" : row.saml ? "SAML" : "—"), value: (row) => (row.oidc ? "OIDC" : "SAML") },
                  { id: "issuer", header: "Issuer (URL)", cell: (row) => row.oidc?.issuerUri ?? "—", value: (row) => row.oidc?.issuerUri ?? "" },
                  { id: "client", header: "Client ID", cell: (row) => row.oidc?.clientId ?? "—", value: (row) => row.oidc?.clientId ?? "" },
                  {
                    id: "state",
                    header: "Status",
                    cell: (row) => <GcpStatus status={stateLabel(row.state, row.disabled)} />,
                    value: (row) => stateLabel(row.state, row.disabled),
                  },
                ]}
                empty={{ headline: "No providers", description: "Add an identity provider to let its users sign in." }}
              />
            </>
          ) : null}
        </>
      )}
    </>
  );
}
