import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Navigate, Route } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AzureApp } from "./portal/index.js";
import { OverviewPage } from "./pages/OverviewPage.js";
import { SubscriptionsPage } from "./pages/SubscriptionsPage.js";
import { SubscriptionDetailPage } from "./pages/SubscriptionDetailPage.js";
import { ContainerAppsPage } from "./pages/ContainerAppsPage.js";
import { ContainerAppDetailPage } from "./pages/ContainerAppDetailPage.js";
import { AzureFunctionsPage } from "./pages/AzureFunctionsPage.js";
import { FunctionAppDetailPage } from "./pages/FunctionAppDetailPage.js";
import { ACRRegistriesPage } from "./pages/ACRRegistriesPage.js";
import { ACRRegistryDetailPage } from "./pages/ACRRegistryDetailPage.js";
import { StorageAccountsPage } from "./pages/StorageAccountsPage.js";
import { StorageAccountDetailPage } from "./pages/StorageAccountDetailPage.js";
import { MonitorPage } from "./pages/MonitorPage.js";
import { AppRegistrationsPage } from "./pages/AppRegistrationsPage.js";
import { AppRegistrationDetailPage } from "./pages/AppRegistrationDetailPage.js";
import { NotSupportedPage } from "./pages/NotSupportedPage.js";
import { ServiceListPage } from "./pages/ServiceListPage.js";
import { ServiceDetailPage } from "./pages/ServiceDetailPage.js";
import { SERVICE_BLADES } from "./services.js";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AzureApp>
        <Route path="/ui/" element={<OverviewPage />} />
        <Route path="/ui/subscriptions" element={<SubscriptionsPage />} />
        <Route path="/ui/subscriptions/:subscriptionId" element={<SubscriptionDetailPage />} />
        <Route path="/ui/container-apps" element={<ContainerAppsPage />} />
        <Route path="/ui/container-apps/:name" element={<ContainerAppDetailPage />} />
        <Route path="/ui/functions" element={<AzureFunctionsPage />} />
        <Route path="/ui/functions/:name" element={<FunctionAppDetailPage />} />
        <Route path="/ui/acr" element={<ACRRegistriesPage />} />
        <Route path="/ui/acr/:name" element={<ACRRegistryDetailPage />} />
        <Route path="/ui/storage" element={<StorageAccountsPage />} />
        <Route path="/ui/storage/:name" element={<StorageAccountDetailPage />} />
        <Route path="/ui/monitor" element={<MonitorPage />} />
        <Route path="/ui/entra/app-registrations" element={<AppRegistrationsPage />} />
        <Route path="/ui/entra/app-registrations/:objectId" element={<AppRegistrationDetailPage />} />
        {/* Every descriptor-driven service blade: a list on the provider's
            real subscription-wide ARM List, and a resource pane on its real
            Get plus the sub-resource Lists and action POSTs ARM nests under
            it. The route slug and everything each blade reads come from the
            service's own descriptor (services.ts). */}
        {SERVICE_BLADES.map((blade) => (
          <Route key={blade.slug} path={`/ui/${blade.slug}`} element={<ServiceListPage blade={blade} />} />
        ))}
        {SERVICE_BLADES.map((blade) => (
          <Route
            key={`${blade.slug}-detail`}
            path={`/ui/${blade.slug}/:name`}
            element={<ServiceDetailPage blade={blade} />}
          />
        ))}
        <Route path="/ui/not-supported/:slug" element={<NotSupportedPage />} />
        {/* Any other path lands on the overview rather than an empty shell:
            a mistyped or stale deep link must never render a blank console. */}
        <Route path="*" element={<Navigate to="/ui/" replace />} />
      </AzureApp>
    </QueryClientProvider>
  </StrictMode>,
);
