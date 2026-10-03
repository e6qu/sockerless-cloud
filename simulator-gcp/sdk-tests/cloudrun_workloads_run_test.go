package gcp_sdk_test

import (
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// cloudRunWorkloadScript runs as a Cloud Run workload's container: it records
// its start in the Cloud Storage volume at /mnt/bucket under a name its
// hostname makes unique, logs line and its hostname, and on the stop signal
// records its stop the same way before it exits. listen, when set, keeps a TCP
// listener on $PORT for the instance's default startup probe.
func cloudRunWorkloadScript(line string, listen bool) string {
	listener := ""
	if listen {
		listener = `nc -lk -p "$PORT" -e true &` + "\n"
	}
	return `trap 'echo stopped > /mnt/bucket/stopped-$HOSTNAME; exit 0' TERM
echo started > /mnt/bucket/started-$HOSTNAME
` + listener + `echo "` + line + ` $HOSTNAME"
sleep 2147483647 &
wait $!`
}

func cloudRunWorkloadContainer(script string) *runpb.Container {
	return &runpb.Container{
		Image:        "alpine:latest",
		Command:      []string{"sh", "-c", script},
		VolumeMounts: []*runpb.VolumeMount{{Name: "bucket", MountPath: "/mnt/bucket"}},
	}
}

// distinctLogHosts returns the hostnames the log lines that start with prefix
// name, each once.
func distinctLogHosts(messages []string, prefix string) []string {
	var hosts []string
	for _, m := range messages {
		if host, ok := strings.CutPrefix(strings.TrimSpace(m), prefix+" "); ok && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func objectsWithPrefix(t *testing.T, bucket *storage.BucketHandle, prefix string) []string {
	t.Helper()
	var names []string
	for _, name := range volumeObjectNames(t, bucket) {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names
}

func manualScaling(count int32) *runpb.WorkerPoolScaling {
	return &runpb.WorkerPoolScaling{ManualInstanceCount: &count}
}

// A worker pool runs as many instances as its manual instance count, each a
// container group with the pool's Cloud Storage volume mounted and its output
// in the pool's Cloud Logging entries. Raising the count starts more
// instances; lowering it stops the surplus with the stop signal before the
// update completes, and deleting the pool stops the rest.
func TestSDK_CloudRun_WorkerPoolRunsItsInstances(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucketName := uniqueName("sdk-run-wp-volume")
	bucket := client.Bucket(bucketName)
	require.NoError(t, bucket.Create(ctx, "test-project", nil))

	pools := newWorkerPoolsClient(t)
	parent := "projects/test-project/locations/us-central1"
	id := uniqueName("sdk-wp-run")
	name := parent + "/workerPools/" + id
	createOp, err := pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: id,
		WorkerPool: &runpb.WorkerPool{
			Scaling: manualScaling(2),
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{cloudRunWorkloadContainer(cloudRunWorkloadScript("worker started", false))},
				Volumes:    []*runpb.Volume{cloudStorageVolume("bucket", bucketName, false)},
			},
		},
	})
	require.NoError(t, err)
	pool, err := createOp.Wait(ctx)
	require.NoError(t, err)
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		if op, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: name}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	filter := `resource.type="cloud_run_worker_pool" AND resource.labels.worker_pool_name="` + id + `"`
	started := func(want int) []string {
		messages := followLogMessages(t, filter, func(messages []string) bool {
			return len(distinctLogHosts(messages, "worker started")) >= want
		})
		return distinctLogHosts(messages, "worker started")
	}
	require.Len(t, started(2), 2, "the pool runs its two instances")
	assert.Len(t, objectsWithPrefix(t, bucket, "started-"), 2, "each instance wrote through the pool's volume")

	pool.Scaling = manualScaling(3)
	updateOp, err := pools.UpdateWorkerPool(ctx, &runpb.UpdateWorkerPoolRequest{
		WorkerPool: pool,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"scaling"}},
	})
	require.NoError(t, err)
	pool, err = updateOp.Wait(ctx)
	require.NoError(t, err)
	require.Len(t, started(3), 3, "raising the instance count starts another instance")
	assert.Empty(t, objectsWithPrefix(t, bucket, "stopped-"), "no instance has stopped")

	pool.Scaling = manualScaling(1)
	updateOp, err = pools.UpdateWorkerPool(ctx, &runpb.UpdateWorkerPoolRequest{
		WorkerPool: pool,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"scaling"}},
	})
	require.NoError(t, err)
	_, err = updateOp.Wait(ctx)
	require.NoError(t, err)
	assert.Len(t, objectsWithPrefix(t, bucket, "stopped-"), 2, "lowering the instance count stopped two instances")

	deleteOp, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: name})
	require.NoError(t, err)
	_, err = deleteOp.Wait(ctx)
	require.NoError(t, err)
	deleted = true
	assert.Len(t, objectsWithPrefix(t, bucket, "stopped-"), 3, "deleting the pool stopped its last instance")
}

// A Cloud Run instance runs its containers from creation, with its Cloud
// Storage volume mounted and its output in the instance's Cloud Logging
// entries. StopInstance stops the containers with the stop signal,
// StartInstance runs them again, and deleting the instance stops them.
func TestSDK_CloudRun_InstanceRunsItsContainers(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucketName := uniqueName("sdk-run-inst-volume")
	bucket := client.Bucket(bucketName)
	require.NoError(t, bucket.Create(ctx, "test-project", nil))

	instances := newInstancesClient(t)
	parent := "projects/test-project/locations/us-central1"
	id := uniqueName("sdk-inst-run")
	name := parent + "/instances/" + id
	createOp, err := instances.CreateInstance(ctx, &runpb.CreateInstanceRequest{
		Parent:     parent,
		InstanceId: id,
		Instance: &runpb.Instance{
			Containers: []*runpb.Container{cloudRunWorkloadContainer(cloudRunWorkloadScript("instance started", true))},
			Volumes:    []*runpb.Volume{cloudStorageVolume("bucket", bucketName, false)},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		if op, err := instances.DeleteInstance(ctx, &runpb.DeleteInstanceRequest{Name: name}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	filter := `resource.type="cloud_run_instance" AND resource.labels.instance_name="` + id + `"`
	started := func(want int) {
		followLogMessages(t, filter, func(messages []string) bool {
			return len(distinctLogHosts(messages, "instance started")) >= want
		})
	}
	started(1)
	assert.Len(t, objectsWithPrefix(t, bucket, "started-"), 1, "the instance wrote through its volume")

	stopOp, err := instances.StopInstance(ctx, &runpb.StopInstanceRequest{Name: name})
	require.NoError(t, err)
	_, err = stopOp.Wait(ctx)
	require.NoError(t, err)
	assert.Len(t, objectsWithPrefix(t, bucket, "stopped-"), 1, "StopInstance stopped the container with the stop signal")

	startOp, err := instances.StartInstance(ctx, &runpb.StartInstanceRequest{Name: name})
	require.NoError(t, err)
	_, err = startOp.Wait(ctx)
	require.NoError(t, err)
	started(2)

	deleteOp, err := instances.DeleteInstance(ctx, &runpb.DeleteInstanceRequest{Name: name})
	require.NoError(t, err)
	_, err = deleteOp.Wait(ctx)
	require.NoError(t, err)
	deleted = true
	assert.Len(t, objectsWithPrefix(t, bucket, "stopped-"), 2, "deleting the instance stopped its container")
}
