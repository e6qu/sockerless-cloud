import { useQuery } from "@tanstack/react-query";
import { useApiClient } from "./api-context.js";

export function useHealth() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["health"],
    queryFn: () => client.health(),
    refetchInterval: 10_000,
  });
}

export function useStatus() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["status"],
    queryFn: () => client.status(),
    refetchInterval: 5_000,
  });
}

export function useContainers() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["containers"],
    queryFn: () => client.containers(),
    refetchInterval: 5_000,
  });
}

export function useMetrics() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["metrics"],
    queryFn: () => client.metrics(),
    refetchInterval: 5_000,
  });
}

export function useResources(active?: boolean) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["resources", { active }],
    queryFn: () => client.resources(active),
    refetchInterval: 5_000,
  });
}

export function useCheck() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["check"],
    queryFn: () => client.check(),
    refetchInterval: 10_000,
  });
}

export function useInfo() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["info"],
    queryFn: () => client.info(),
    staleTime: 60_000,
  });
}
