import { useQuery } from "@tanstack/react-query";
import { listTenants } from "../azure.js";

/** The directory (tenant) display name the portal shows under browse titles. */
export function useDirectoryName(): string {
  const tenants = useQuery({ queryKey: ["tenants"], queryFn: listTenants, staleTime: Infinity });
  return tenants.data?.[0]?.displayName ?? "";
}
