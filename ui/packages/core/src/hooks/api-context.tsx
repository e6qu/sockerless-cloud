import { createContext, useContext, type ReactNode } from "react";
import { ApiClient } from "../api/client.js";

const ApiClientContext = createContext<ApiClient | null>(null);

export interface ApiClientProviderProps {
  client: ApiClient;
  children: ReactNode;
}

export function ApiClientProvider({ client, children }: ApiClientProviderProps) {
  return <ApiClientContext.Provider value={client}>{children}</ApiClientContext.Provider>;
}

export function useApiClient(): ApiClient {
  const client = useContext(ApiClientContext);
  if (!client) {
    throw new Error("useApiClient must be used within an ApiClientProvider");
  }
  return client;
}
