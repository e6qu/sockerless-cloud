import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Navigate, Route } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AzureApp } from "./portal/AzureApp.js";
import { HomePage } from "./pages/HomePage.js";
import { AllServicesPage } from "./pages/AllServicesPage.js";
import { AllResourcesPage, ResourceGroupsPage, ServiceBrowsePage, SubscriptionsPage } from "./pages/BrowsePages.js";
import { ResourcePage } from "./pages/resource/ResourcePage.js";
import { CreateWizardPage } from "./pages/create/CreateWizard.js";
import { CreateResourcePage, DeploymentPage } from "./pages/create/CreatePages.js";
import { AppRegistrationPage, AppRegistrationsPage, EntraGroupsPage, EntraOverviewPage, EntraUsersPage } from "./pages/EntraPages.js";
import { MonitorActivityLogPage, MonitorLogsPage, MonitorOverviewPage } from "./pages/MonitorPages.js";
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
        <Route path="/ui/" element={<Navigate to="/ui/home" replace />} />
        <Route path="/ui/home" element={<HomePage />} />
        <Route path="/ui/allservices" element={<AllServicesPage />} />
        <Route path="/ui/browse/all" element={<AllResourcesPage />} />
        <Route path="/ui/browse/resourcegroups" element={<ResourceGroupsPage />} />
        <Route path="/ui/browse/subscriptions" element={<SubscriptionsPage />} />
        <Route path="/ui/browse/:namespace/:type" element={<ServiceBrowsePage />} />
        <Route path="/ui/browse/:namespace/:type/kind/:kind" element={<ServiceBrowsePage />} />
        <Route path="/ui/resource/*" element={<ResourcePage />} />
        <Route path="/ui/create" element={<CreateResourcePage />} />
        <Route path="/ui/create/:namespace/:type" element={<CreateWizardPage />} />
        <Route path="/ui/create/:namespace/:type/kind/:kind" element={<CreateWizardPage />} />
        <Route path="/ui/deployment/:name" element={<DeploymentPage />} />
        <Route path="/ui/entra" element={<EntraOverviewPage />} />
        <Route path="/ui/entra/users" element={<EntraUsersPage />} />
        <Route path="/ui/entra/groups" element={<EntraGroupsPage />} />
        <Route path="/ui/entra/app-registrations" element={<AppRegistrationsPage />} />
        <Route path="/ui/entra/app-registrations/:objectId" element={<AppRegistrationPage />} />
        <Route path="/ui/entra/app-registrations/:objectId/:section" element={<AppRegistrationPage />} />
        <Route path="/ui/monitor" element={<MonitorOverviewPage />} />
        <Route path="/ui/monitor/activitylog" element={<MonitorActivityLogPage />} />
        <Route path="/ui/monitor/logs" element={<MonitorLogsPage />} />
        <Route path="*" element={<Navigate to="/ui/home" replace />} />
      </AzureApp>
    </QueryClientProvider>
  </StrictMode>,
);
