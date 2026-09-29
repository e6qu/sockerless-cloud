package main

// The gRPC google.longrunning.Operations service reads every operation from the
// shared store, where the REST doors write it as JSON, and protojson can turn
// that JSON back into a protobuf Any only for a message the global registry
// holds. Link in the messages of each service whose REST methods record
// operations there, so a gRPC client polling one of them reads its response and
// metadata.
import (
	_ "cloud.google.com/go/apigateway/apiv1/apigatewaypb"
	_ "cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	_ "cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	_ "cloud.google.com/go/eventarc/apiv1/eventarcpb"
	_ "cloud.google.com/go/functions/apiv2/functionspb"
	_ "cloud.google.com/go/redis/apiv1/redispb"
	_ "cloud.google.com/go/redis/cluster/apiv1/clusterpb"
	_ "cloud.google.com/go/resourcemanager/apiv2/resourcemanagerpb"
	_ "cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	_ "cloud.google.com/go/run/apiv2/runpb"
	_ "cloud.google.com/go/serviceusage/apiv1/serviceusagepb"
	_ "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	_ "cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	_ "cloud.google.com/go/vpcaccess/apiv1/vpcaccesspb"
)
