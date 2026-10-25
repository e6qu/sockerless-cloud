import { type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, type GcpPageAction } from "./GcpConsole.js";
import { GcpTabs, type GcpTab } from "./GcpTabs.js";
import { Icon } from "./icons.js";

export interface GcpDetailProperty {
  label: string;
  value: ReactNode;
}

/** The key/value list a Details tab shows. */
export function DetailList({ properties }: { properties: GcpDetailProperty[] }) {
  return (
    <dl className="gc-detail-grid">
      {properties.map((property) => (
        <div className="gc-detail-pair" key={property.label}>
          <dt>{property.label}</dt>
          <dd>{property.value}</dd>
        </div>
      ))}
    </dl>
  );
}

export function LoadError({ error, noun = "this resource" }: { error: unknown; noun?: string }) {
  return (
    <div className="gc-message gc-message-error" role="alert">
      <Icon name="error_fill" />
      <span>
        <strong>Couldn't load {noun}.</strong> {error instanceof Error ? error.message : "The API did not respond."}
      </span>
    </div>
  );
}

export function Loading({ noun }: { noun?: string }) {
  return (
    <div className="gc-loading" role="status">
      <span className="gc-spinner" aria-hidden />
      Loading{noun ? ` ${noun}` : ""}…
    </div>
  );
}

// GcpResourceDetail is a resource's detail page as the real console lays it
// out: a back arrow to the list, the resource name with its actions, then the
// resource's tabs, the first of them its Details. It reads the cloud's real
// Get method and surfaces the API's error rather than masking it.
export function GcpResourceDetail<T>({
  title,
  description,
  backTo,
  queryKey,
  queryFn,
  properties,
  extra,
  tabs,
  actions,
}: {
  title: string;
  description?: string;
  backTo: string;
  /** Kept for callers that still pass it; the back arrow names no label. */
  backLabel?: string;
  queryKey: unknown[];
  queryFn: () => Promise<T>;
  properties: (resource: T) => GcpDetailProperty[];
  extra?: (resource: T) => ReactNode;
  /** Tabs after Details. */
  tabs?: (resource: T) => GcpTab[];
  actions?: GcpPageAction[] | ((resource: T | undefined) => GcpPageAction[]);
}) {
  const query = useQuery({ queryKey, queryFn });
  const resolvedActions = typeof actions === "function" ? actions(query.data) : actions;

  return (
    <>
      <GcpPageHeader
        title={title}
        description={description}
        actions={resolvedActions}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        backTo={backTo}
      />
      {query.isError ? (
        <LoadError error={query.error} />
      ) : query.isLoading || !query.data ? (
        <Loading />
      ) : (
        <GcpTabs
          label={title}
          tabs={[
            {
              id: "details",
              label: "Details",
              content: (
                <>
                  <DetailList properties={properties(query.data)} />
                  {extra?.(query.data)}
                </>
              ),
            },
            ...(tabs?.(query.data) ?? []),
          ]}
        />
      )}
    </>
  );
}
