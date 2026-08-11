import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Navigate, Route } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { GcpApp } from "./console/index.js";
import { OverviewPage } from "./pages/OverviewPage.js";
import { CloudRunJobsPage } from "./pages/CloudRunJobsPage.js";
import { CloudRunJobDetailPage } from "./pages/CloudRunJobDetailPage.js";
import { CloudFunctionsPage } from "./pages/CloudFunctionsPage.js";
import { CloudFunctionDetailPage } from "./pages/CloudFunctionDetailPage.js";
import { ArtifactRegistryPage } from "./pages/ArtifactRegistryPage.js";
import { ARRepoDetailPage } from "./pages/ARRepoDetailPage.js";
import { GCSBucketsPage } from "./pages/GCSBucketsPage.js";
import { GCSBucketDetailPage } from "./pages/GCSBucketDetailPage.js";
import { ServiceAccountsPage } from "./pages/ServiceAccountsPage.js";
import { ServiceAccountDetailPage } from "./pages/ServiceAccountDetailPage.js";
import { ManageProjectsPage } from "./pages/ManageProjectsPage.js";
import { LoggingPage } from "./pages/LoggingPage.js";
import { ComputeEnginePage } from "./pages/ComputeEnginePage.js";
import { ComputeInstanceDetailPage } from "./pages/ComputeInstanceDetailPage.js";
import { VpcNetworkDetailPage, VpcNetworkPage } from "./pages/VpcNetworkPage.js";
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
import { DataflowJobDetailPage, DataflowPage } from "./pages/DataflowPage.js";
import { CloudBuildDetailPage, CloudBuildPage } from "./pages/CloudBuildPage.js";
import { EventarcPage, EventarcTriggerDetailPage } from "./pages/EventarcPage.js";
import { ApiGatewayPage } from "./pages/ApiGatewayPage.js";
import { EnabledApisPage } from "./pages/EnabledApisPage.js";
import { CloudKmsCryptoKeyDetailPage, CloudKmsKeyRingDetailPage, CloudKmsPage } from "./pages/CloudKmsPage.js";
import { SecretDetailPage, SecretManagerPage } from "./pages/SecretManagerPage.js";
import { IamPage } from "./pages/IamPage.js";
import { NotSupportedPage } from "./pages/NotSupportedPage.js";
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
        <Route path="/ui/" element={<OverviewPage />} />
        <Route path="/ui/cloudrun" element={<CloudRunJobsPage />} />
        <Route path="/ui/cloudrun/:name" element={<CloudRunJobDetailPage />} />
        <Route path="/ui/functions" element={<CloudFunctionsPage />} />
        <Route path="/ui/functions/:name" element={<CloudFunctionDetailPage />} />
        <Route path="/ui/ar" element={<ArtifactRegistryPage />} />
        <Route path="/ui/ar/:name" element={<ARRepoDetailPage />} />
        <Route path="/ui/gcs" element={<GCSBucketsPage />} />
        <Route path="/ui/gcs/:name" element={<GCSBucketDetailPage />} />
        <Route path="/ui/serviceaccounts" element={<ServiceAccountsPage />} />
        <Route path="/ui/serviceaccounts/:email" element={<ServiceAccountDetailPage />} />
        <Route path="/ui/projects" element={<ManageProjectsPage />} />
        <Route path="/ui/logging" element={<LoggingPage />} />
        <Route path="/ui/compute" element={<ComputeEnginePage />} />
        <Route path="/ui/compute/:zone/:name" element={<ComputeInstanceDetailPage />} />
        <Route path="/ui/vpc" element={<VpcNetworkPage />} />
        <Route path="/ui/vpc/:name" element={<VpcNetworkDetailPage />} />
        <Route path="/ui/loadbalancing" element={<LoadBalancingPage />} />
        <Route path="/ui/dns" element={<CloudDnsPage />} />
        <Route path="/ui/dns/:name" element={<CloudDnsZoneDetailPage />} />
        <Route path="/ui/vpcaccess" element={<VpcAccessPage />} />
        <Route path="/ui/sql" element={<CloudSqlPage />} />
        <Route path="/ui/sql/:name" element={<CloudSqlInstanceDetailPage />} />
        <Route path="/ui/firestore" element={<FirestorePage />} />
        <Route path="/ui/spanner" element={<SpannerPage />} />
        <Route path="/ui/spanner/:name" element={<SpannerInstanceDetailPage />} />
        <Route path="/ui/bigtable" element={<BigtablePage />} />
        <Route path="/ui/bigtable/:name" element={<BigtableInstanceDetailPage />} />
        <Route path="/ui/memorystore" element={<MemorystorePage />} />
        <Route path="/ui/memorystore/:name" element={<MemorystoreInstanceDetailPage />} />
        <Route path="/ui/bigquery" element={<BigQueryPage />} />
        <Route path="/ui/bigquery/:name" element={<BigQueryDatasetDetailPage />} />
        <Route path="/ui/pubsub" element={<PubSubPage />} />
        <Route path="/ui/pubsub/:name" element={<PubSubTopicDetailPage />} />
        <Route path="/ui/dataflow" element={<DataflowPage />} />
        <Route path="/ui/dataflow/:id" element={<DataflowJobDetailPage />} />
        <Route path="/ui/cloudbuild" element={<CloudBuildPage />} />
        <Route path="/ui/cloudbuild/:id" element={<CloudBuildDetailPage />} />
        <Route path="/ui/eventarc" element={<EventarcPage />} />
        <Route path="/ui/eventarc/:name" element={<EventarcTriggerDetailPage />} />
        <Route path="/ui/apigateway" element={<ApiGatewayPage />} />
        <Route path="/ui/apis" element={<EnabledApisPage />} />
        <Route path="/ui/kms" element={<CloudKmsPage />} />
        <Route path="/ui/kms/:name" element={<CloudKmsKeyRingDetailPage />} />
        <Route path="/ui/kms/:name/:key" element={<CloudKmsCryptoKeyDetailPage />} />
        <Route path="/ui/secrets" element={<SecretManagerPage />} />
        <Route path="/ui/secrets/:name" element={<SecretDetailPage />} />
        <Route path="/ui/iam" element={<IamPage />} />
        <Route path="/ui/not-supported/:service" element={<NotSupportedPage />} />
        {/* Any other path lands on the overview rather than an empty shell:
            a mistyped or stale deep link must never render a blank console. */}
        <Route path="*" element={<Navigate to="/ui/" replace />} />
      </GcpApp>
    </QueryClientProvider>
  </StrictMode>,
);
