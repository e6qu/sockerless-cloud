import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Navigate, Route } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { GcpApp } from "./console/index.js";
import { AllProductsPage, DashboardPage, WelcomePage } from "./pages/HomePages.js";
import { CreateProjectPage } from "./pages/CreateProjectPage.js";
import { CloudRunJobsPage } from "./pages/CloudRunJobsPage.js";
import { CloudRunJobDetailPage } from "./pages/CloudRunJobDetailPage.js";
import { CloudRunServiceCreatePage, CloudRunServiceDetailPage, CloudRunServicesPage } from "./pages/CloudRunServicesPage.js";
import { CloudFunctionsPage } from "./pages/CloudFunctionsPage.js";
import { CloudFunctionDetailPage } from "./pages/CloudFunctionDetailPage.js";
import { ArtifactRegistryPage, CreateRepositoryPage } from "./pages/ArtifactRegistryPage.js";
import { ARRepoDetailPage } from "./pages/ARRepoDetailPage.js";
import { GCSBucketsPage } from "./pages/GCSBucketsPage.js";
import { GCSCreateBucketPage } from "./pages/GCSCreateBucketPage.js";
import { GCSBucketDetailPage } from "./pages/GCSBucketDetailPage.js";
import { ServiceAccountsPage } from "./pages/ServiceAccountsPage.js";
import { ServiceAccountDetailPage } from "./pages/ServiceAccountDetailPage.js";
import { ManageProjectsPage } from "./pages/ManageProjectsPage.js";
import { LoggingPage } from "./pages/LoggingPage.js";
import { ComputeDisksPage, ComputeEnginePage } from "./pages/ComputeEnginePage.js";
import { ComputeCreateInstancePage } from "./pages/ComputeCreateInstancePage.js";
import { ComputeInstanceDetailPage } from "./pages/ComputeInstanceDetailPage.js";
import { VpcNetworkDetailPage, VpcNetworkPage } from "./pages/VpcNetworkPage.js";
import { VpcCreateNetworkPage } from "./pages/VpcCreateNetworkPage.js";
import { LoadBalancingPage } from "./pages/LoadBalancingPage.js";
import { CloudDnsPage, CloudDnsZoneDetailPage } from "./pages/CloudDnsPage.js";
import { VpcAccessPage } from "./pages/VpcAccessPage.js";
import { CloudSqlInstanceDetailPage, CloudSqlPage } from "./pages/CloudSqlPage.js";
import { FirestorePage } from "./pages/FirestorePage.js";
import { SpannerInstanceDetailPage, SpannerPage } from "./pages/SpannerPage.js";
import { BigtableInstanceDetailPage, BigtablePage } from "./pages/BigtablePage.js";
import { MemorystoreInstanceDetailPage, MemorystorePage } from "./pages/MemorystorePage.js";
import { BigQueryDatasetDetailPage, BigQueryPage } from "./pages/BigQueryPage.js";
import { PubSubPage, PubSubTopicDetailPage } from "./pages/PubSubPage.js";
import {
  PubSubSubscriptionCreatePage,
  PubSubSubscriptionDetailPage,
  PubSubSubscriptionsPage,
} from "./pages/PubSubSubscriptionsPage.js";
import { DataflowJobDetailPage, DataflowPage } from "./pages/DataflowPage.js";
import { CloudBuildDetailPage, CloudBuildPage } from "./pages/CloudBuildPage.js";
import { EventarcPage, EventarcTriggerDetailPage } from "./pages/EventarcPage.js";
import { ApiGatewayPage } from "./pages/ApiGatewayPage.js";
import { EnabledApisPage } from "./pages/EnabledApisPage.js";
import { CloudKmsCryptoKeyDetailPage, CloudKmsKeyRingDetailPage, CloudKmsPage } from "./pages/CloudKmsPage.js";
import { SecretCreatePage, SecretDetailPage, SecretManagerPage } from "./pages/SecretManagerPage.js";
import { IamPage, IamRolesPage } from "./pages/IamPage.js";
import { WorkforcePoolsPage } from "./pages/WorkforcePoolsPage.js";
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
      <GcpApp>
        <Route path="/ui/" element={<Navigate to="/ui/welcome" replace />} />
        <Route path="/ui/welcome" element={<WelcomePage />} />
        <Route path="/ui/home/dashboard" element={<DashboardPage tab="dashboard" />} />
        <Route path="/ui/home/activity" element={<DashboardPage tab="activity" />} />
        <Route path="/ui/products" element={<AllProductsPage />} />
        <Route path="/ui/projectcreate" element={<CreateProjectPage />} />
        <Route path="/ui/cloud-resource-manager" element={<ManageProjectsPage />} />

        <Route path="/ui/compute/instances" element={<ComputeEnginePage />} />
        <Route path="/ui/compute/instancesAdd" element={<ComputeCreateInstancePage />} />
        <Route path="/ui/compute/instancesDetail/zones/:zone/instances/:name" element={<ComputeInstanceDetailPage />} />
        <Route path="/ui/compute/disks" element={<ComputeDisksPage />} />

        <Route path="/ui/run" element={<CloudRunServicesPage />} />
        <Route path="/ui/run/create" element={<CloudRunServiceCreatePage />} />
        <Route path="/ui/run/detail/:region/:name" element={<CloudRunServiceDetailPage />} />
        <Route path="/ui/run/jobs" element={<CloudRunJobsPage />} />
        <Route path="/ui/run/jobs/details/:region/:name/executions" element={<CloudRunJobDetailPage />} />
        <Route path="/ui/functions/list" element={<CloudFunctionsPage />} />
        <Route path="/ui/functions/details/:region/:name" element={<CloudFunctionDetailPage />} />

        <Route path="/ui/storage/browser" element={<GCSBucketsPage />} />
        <Route path="/ui/storage/create-bucket" element={<GCSCreateBucketPage />} />
        <Route path="/ui/storage/browser/:name" element={<GCSBucketDetailPage />} />

        <Route path="/ui/sql/instances" element={<CloudSqlPage />} />
        <Route path="/ui/sql/instances/:name/overview" element={<CloudSqlInstanceDetailPage />} />
        <Route path="/ui/firestore/databases" element={<FirestorePage />} />
        <Route path="/ui/spanner/instances" element={<SpannerPage />} />
        <Route path="/ui/spanner/instances/:name/details/databases" element={<SpannerInstanceDetailPage />} />
        <Route path="/ui/bigtable/instances" element={<BigtablePage />} />
        <Route path="/ui/bigtable/instances/:name/overview" element={<BigtableInstanceDetailPage />} />
        <Route path="/ui/memorystore/redis/instances" element={<MemorystorePage />} />
        <Route path="/ui/memorystore/redis/instances/:name/details" element={<MemorystoreInstanceDetailPage />} />

        <Route path="/ui/networking/networks/list" element={<VpcNetworkPage />} />
        <Route path="/ui/networking/networks/add" element={<VpcCreateNetworkPage />} />
        <Route path="/ui/networking/networks/details/:name" element={<VpcNetworkDetailPage />} />
        <Route path="/ui/networking/connectors/list" element={<VpcAccessPage />} />
        <Route path="/ui/net-services/loadbalancing/list/loadBalancers" element={<LoadBalancingPage />} />
        <Route path="/ui/net-services/dns/zones" element={<CloudDnsPage />} />
        <Route path="/ui/net-services/dns/zones/:name/details" element={<CloudDnsZoneDetailPage />} />

        <Route path="/ui/logs/query" element={<LoggingPage />} />
        <Route path="/ui/bigquery" element={<BigQueryPage />} />
        <Route path="/ui/bigquery/datasets/:name" element={<BigQueryDatasetDetailPage />} />
        <Route path="/ui/cloudpubsub/topic/list" element={<PubSubPage />} />
        <Route path="/ui/cloudpubsub/topic/detail/:name" element={<PubSubTopicDetailPage />} />
        <Route path="/ui/cloudpubsub/subscription/list" element={<PubSubSubscriptionsPage />} />
        <Route path="/ui/cloudpubsub/subscription/create" element={<PubSubSubscriptionCreatePage />} />
        <Route path="/ui/cloudpubsub/subscription/detail/:name" element={<PubSubSubscriptionDetailPage />} />
        <Route path="/ui/dataflow/jobs" element={<DataflowPage />} />
        <Route path="/ui/dataflow/jobs/:id" element={<DataflowJobDetailPage />} />

        <Route path="/ui/artifacts" element={<ArtifactRegistryPage />} />
        <Route path="/ui/artifacts/create-repo" element={<CreateRepositoryPage />} />
        <Route path="/ui/artifacts/:format/:projectId/:location/:name" element={<ARRepoDetailPage />} />
        <Route path="/ui/cloud-build/builds" element={<CloudBuildPage />} />
        <Route path="/ui/cloud-build/builds/:id" element={<CloudBuildDetailPage />} />
        <Route path="/ui/eventarc/triggers" element={<EventarcPage />} />
        <Route path="/ui/eventarc/triggers/:name" element={<EventarcTriggerDetailPage />} />
        <Route path="/ui/api-gateway" element={<ApiGatewayPage />} />

        <Route path="/ui/security/secret-manager" element={<SecretManagerPage />} />
        <Route path="/ui/security/secret-manager/create" element={<SecretCreatePage />} />
        <Route path="/ui/security/secret-manager/secret/:name/versions" element={<SecretDetailPage />} />
        <Route path="/ui/security/kms/keyrings" element={<CloudKmsPage />} />
        <Route path="/ui/security/kms/keyring/manage/:location/:name/key" element={<CloudKmsKeyRingDetailPage />} />
        <Route path="/ui/security/kms/key/manage/:location/:name/:key" element={<CloudKmsCryptoKeyDetailPage />} />

        <Route path="/ui/iam-admin/iam" element={<IamPage />} />
        <Route path="/ui/iam-admin/roles" element={<IamRolesPage />} />
        <Route path="/ui/iam-admin/serviceaccounts" element={<ServiceAccountsPage />} />
        <Route path="/ui/iam-admin/serviceaccounts/details/:email" element={<ServiceAccountDetailPage />} />
        <Route path="/ui/iam-admin/workforce-identity-pools" element={<WorkforcePoolsPage />} />
        <Route path="/ui/apis/dashboard" element={<EnabledApisPage />} />
        {/* A mistyped or stale deep link lands on Welcome rather than an empty shell. */}
        <Route path="*" element={<Navigate to="/ui/welcome" replace />} />
      </GcpApp>
    </QueryClientProvider>
  </StrictMode>,
);
