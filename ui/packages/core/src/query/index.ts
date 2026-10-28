import { QueryClient, type InvalidateOptions, type InvalidateQueryFilters, type QueryKey } from "@tanstack/react-query";

// TanStack Query joins a refetch onto a fetch still in flight when the query has
// no data yet, so a write that lands during a list's first load would be
// answered by that load, which began before the write. Cancel the in-flight
// fetch first so the refetch starts after the write.
export class ConsoleQueryClient extends QueryClient {
  override async invalidateQueries<TTaggedQueryKey extends QueryKey = QueryKey>(
    filters?: InvalidateQueryFilters<TTaggedQueryKey>,
    options?: InvalidateOptions,
  ): Promise<void> {
    await this.cancelQueries(filters);
    return super.invalidateQueries(filters, options);
  }
}
