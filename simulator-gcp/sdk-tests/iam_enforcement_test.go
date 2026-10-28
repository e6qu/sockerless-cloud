package gcp_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/iam"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	iamadmin "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
	pubsubrest "google.golang.org/api/pubsub/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IAM decides every call: a service account performs a method only when a
// policy on the resource or one of its ancestors grants the permission the
// method requires, the same code a client runs against Google Cloud.

// enforcementPrincipal creates a service account with a key and returns its
// email and the token source its credential file yields.
func enforcementPrincipal(t *testing.T, accountID string) (string, oauth2.TokenSource) {
	t.Helper()
	_, _, keyFile := mintServiceAccountKeyFile(t, accountID)
	email, _ := keyFile["client_email"].(string)
	return email, tokenSourceFromKeyFile(t, keyFile).TokenSource
}

func storageClientAs(t *testing.T, ts oauth2.TokenSource) *storage.Client {
	t.Helper()
	client, err := storage.NewClient(ctx, option.WithHTTPClient(&http.Client{Transport: &simAuthTransport{
		base: http.DefaultTransport,
		host: strings.TrimPrefix(baseURL, "http://"),
		ts:   ts,
	}}))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// grantOnBucket adds a binding to the bucket's policy, read-modify-write.
func grantOnBucket(t *testing.T, bucket *storage.BucketHandle, role iam.RoleName, member string) {
	t.Helper()
	policy, err := bucket.IAM().Policy(ctx)
	require.NoError(t, err)
	policy.Add(member, role)
	require.NoError(t, bucket.IAM().SetPolicy(ctx, policy))
}

func requireStorageForbidden(t *testing.T, err error, message string) {
	t.Helper()
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "want a googleapi error, got %v", err)
	require.Equal(t, http.StatusForbidden, apiErr.Code, "%v", err)
	require.Equal(t, message, apiErr.Message)
}

func TestIAMEnforcement_ObjectViewerReadsButCannotDelete(t *testing.T) {
	t.Parallel()
	email, ts := enforcementPrincipal(t, "enf-object-viewer")
	owner := storageClient(t)
	name := uniquename.New("iam-enforce")
	bucket := owner.Bucket(name)
	require.NoError(t, bucket.Create(ctx, "test-project", &storage.BucketAttrs{
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
	}))
	_, err := writeObject(t, bucket.Object("report.txt"), "quarterly")
	require.NoError(t, err)

	asViewer := storageClientAs(t, ts).Bucket(name).Object("report.txt")
	_, err = asViewer.NewReader(ctx)
	require.Error(t, err, "a principal bound to nothing reads the object")

	grantOnBucket(t, bucket, "roles/storage.objectViewer", "serviceAccount:"+email)
	reader, err := asViewer.NewReader(ctx)
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	reader.Close()
	require.Equal(t, "quarterly", string(body))

	requireStorageForbidden(t, asViewer.Delete(ctx), email+
		" does not have storage.objects.delete access to the Google Cloud Storage object. "+
		"Permission 'storage.objects.delete' denied on resource (or it may not exist).")

	grantOnBucket(t, bucket, "roles/storage.objectAdmin", "serviceAccount:"+email)
	require.NoError(t, asViewer.Delete(ctx))
}

func TestIAMEnforcement_ProjectGrantReachesTheBucket(t *testing.T) {
	t.Parallel()
	const project = "iam-enforce-inherit"
	ensureProject(t, project)
	email, ts := enforcementPrincipal(t, "enf-project-viewer")
	owner := storageClient(t)
	name := uniquename.New("iam-inherit")
	bucket := owner.Bucket(name)
	require.NoError(t, bucket.Create(ctx, project, &storage.BucketAttrs{
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
	}))
	_, err := writeObject(t, bucket.Object("shared.txt"), "inherited")
	require.NoError(t, err)

	crm, err := cloudresourcemanager.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	policy, err := crm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{
		Role: "roles/storage.objectViewer", Members: []string{"serviceAccount:" + email},
	})
	_, err = crm.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Do()
	require.NoError(t, err)

	reader, err := storageClientAs(t, ts).Bucket(name).Object("shared.txt").NewReader(ctx)
	require.NoError(t, err, "a project-level grant reaches the project's buckets")
	reader.Close()
}

func TestIAMEnforcement_CustomRoleGrantsItsPermissions(t *testing.T) {
	t.Parallel()
	email, ts := enforcementPrincipal(t, "enf-custom-role")
	admin, err := iamadmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	role, err := admin.Projects.Roles.Create("projects/test-project", &iamadmin.CreateRoleRequest{
		RoleId: "enforcementObjectReader",
		Role:   &iamadmin.Role{Title: "Object reader", IncludedPermissions: []string{"storage.objects.get"}, Stage: "GA"},
	}).Do()
	require.NoError(t, err)

	owner := storageClient(t)
	name := uniquename.New("iam-custom")
	bucket := owner.Bucket(name)
	require.NoError(t, bucket.Create(ctx, "test-project", &storage.BucketAttrs{
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
	}))
	_, err = writeObject(t, bucket.Object("custom.txt"), "custom")
	require.NoError(t, err)
	grantOnBucket(t, bucket, iam.RoleName(role.Name), "serviceAccount:"+email)

	asHolder := storageClientAs(t, ts).Bucket(name)
	reader, err := asHolder.Object("custom.txt").NewReader(ctx)
	require.NoError(t, err)
	reader.Close()
	_, err = writeObject(t, asHolder.Object("other.txt"), "denied")
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "the role holds no storage.objects.create: %v", err)
	require.Equal(t, http.StatusForbidden, apiErr.Code)
}

func TestIAMEnforcement_PubSubPublishOverGRPC(t *testing.T) {
	t.Parallel()
	ensureProject(t, "iam-enforce-pubsub")
	const project = "iam-enforce-pubsub"
	email, ts := enforcementPrincipal(t, "enf-publisher")
	owner := newPSGRPCClient(t, project)
	topic, err := owner.CreateTopic(ctx, "enforced-topic")
	require.NoError(t, err)

	publisher, err := pubsub.NewClient(ctx, project, append(simGRPCClientOptionsAt(grpcAddr, ts), option.WithTelemetryDisabled())...)
	require.NoError(t, err)
	t.Cleanup(func() { publisher.Close() })
	_, err = publisher.Topic("enforced-topic").Publish(ctx, &pubsub.Message{Data: []byte("denied")}).Get(ctx)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Equal(t, "User not authorized to perform this action.", status.Convert(err).Message())

	rest, err := pubsubrest.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	_, err = rest.Projects.Topics.SetIamPolicy(topic.String(), &pubsubrest.SetIamPolicyRequest{Policy: &pubsubrest.Policy{
		Bindings: []*pubsubrest.Binding{{Role: "roles/pubsub.publisher", Members: []string{"serviceAccount:" + email}}},
	}}).Do()
	require.NoError(t, err)

	allowed := publisher.Topic("enforced-topic")
	t.Cleanup(allowed.Stop)
	id, err := allowed.Publish(ctx, &pubsub.Message{Data: []byte("allowed")}).Get(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, id)
}
