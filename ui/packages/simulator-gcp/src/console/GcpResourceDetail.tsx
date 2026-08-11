import { type ReactNode } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, type GcpPageAction } from "./GcpConsole.js";

export interface GcpDetailProperty {
  label: string;
  value: ReactNode;
}

// GcpResourceDetail renders a resource's real properties as the console's
// key/value detail, reading the cloud's real Get API and surfacing its error
// rather than masking it.
export function GcpResourceDetail<T>({
  title,
  description,
  backTo,
  backLabel,
  queryKey,
  queryFn,
  properties,
  extra,
  actions,
}: {
  title: string;
  description: string;
  backTo: string;
  backLabel: string;
  queryKey: unknown[];
  queryFn: () => Promise<T>;
  properties: (resource: T) => GcpDetailProperty[];
  extra?: (resource: T) => ReactNode;
  /** Header actions (e.g. Delete) that don't depend on the resource having
   *  loaded — the same way the list page's "Create …" header action renders
   *  regardless of whether the list read has succeeded. */
  actions?: GcpPageAction[];
}) {
  const query = useQuery({ queryKey, queryFn });

  return (
    <>
      <div className="gc-detail-back">
        <Link to={backTo}>‹ {backLabel}</Link>
      </div>
      <GcpPageHeader
        title={title}
        description={description}
        actions={actions}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
      />

      {query.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this resource.</strong>{" "}
          {query.error instanceof Error ? query.error.message : "The simulator did not respond."}
        </div>
      ) : query.isLoading || !query.data ? (
        <div className="gc-loading" role="status">Loading…</div>
      ) : (
        <>
          <dl className="gc-detail-grid">
            {properties(query.data).map((property) => (
              <div className="gc-detail-pair" key={property.label}>
                <dt>{property.label}</dt>
                <dd>{property.value}</dd>
              </div>
            ))}
          </dl>
          {extra?.(query.data)}
        </>
      )}
    </>
  );
}
