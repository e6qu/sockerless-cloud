package gcp_sdk_test

import (
	"io"
	"sort"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/run/apiv2/runpb"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/durationpb"
)

// runJobWithGCSVolumes creates a Cloud Run job whose single container runs
// script with volumes mounted, runs it, and returns the settled execution once
// the RunJob operation completes.
func runJobWithGCSVolumes(t *testing.T, jobID string, volumes []*runpb.Volume, mounts []*runpb.VolumeMount, script string) *runpb.Execution {
	t.Helper()
	jobs := newJobsClient(t)
	createOp, err := jobs.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{{
						Image:        "public.ecr.aws/docker/library/alpine:latest",
						Command:      []string{"sh", "-c", script},
						VolumeMounts: mounts,
					}},
					Volumes: volumes,
					Retries: &runpb.TaskTemplate_MaxRetries{MaxRetries: 0},
					Timeout: durationpb.New(60 * time.Second),
				},
			},
		},
	})
	require.NoError(t, err)
	job, err := createOp.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, jobs, job.Name)

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: job.Name})
	require.NoError(t, err)
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err, "the job's execution failed")
	return exec
}

func cloudStorageVolume(name, bucket string, readOnly bool, options ...string) *runpb.Volume {
	return &runpb.Volume{Name: name, VolumeType: &runpb.Volume_Gcs{Gcs: &runpb.GCSVolumeSource{
		Bucket: bucket, ReadOnly: readOnly, MountOptions: options,
	}}}
}

func readVolumeObject(t *testing.T, bucket *storage.BucketHandle, name string) string {
	t.Helper()
	reader, err := bucket.Object(name).NewReader(ctx)
	require.NoError(t, err, "read %s", name)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(data)
}

func volumeObjectNames(t *testing.T, bucket *storage.BucketHandle) []string {
	t.Helper()
	var names []string
	it := bucket.Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, attrs.Name)
	}
	sort.Strings(names)
	return names
}

func putVolumeObject(t *testing.T, bucket *storage.BucketHandle, name, contentType, data string) *storage.ObjectAttrs {
	t.Helper()
	w := bucket.Object(name).NewWriter(ctx)
	w.ContentType = contentType
	_, err := w.Write([]byte(data))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return w.Attrs()
}

// A Cloud Run job writes, overwrites, removes and renames files through its
// Cloud Storage volumes, and each change is an object change once the
// execution completes, as Cloud Storage FUSE makes it: a closed file is a new
// generation, a directory gets a placeholder object, a removed file's object
// is deleted and a renamed file's object moves to the new name. The volume
// with only-dir mounts one directory of the bucket.
func TestSDK_CloudRun_JobWritesThroughCloudStorageVolumes(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucketName := uniqueName("sdk-run-gcs-volume")
	bucket := client.Bucket(bucketName)
	require.NoError(t, bucket.Create(ctx, "test-project", nil))

	seed := putVolumeObject(t, bucket, "seed.txt", "text/plain", "seed\n")
	putVolumeObject(t, bucket, "rename-me.txt", "text/csv", "a,b\n")
	putVolumeObject(t, bucket, "delete-me.txt", "text/plain", "doomed\n")

	exec := runJobWithGCSVolumes(t, uniqueName("sdk-gcs-volume-job"),
		[]*runpb.Volume{
			cloudStorageVolume("bucket", bucketName, false),
			cloudStorageVolume("results", bucketName, false, "only-dir=results"),
		},
		[]*runpb.VolumeMount{
			{Name: "bucket", MountPath: "/mnt/bucket"},
			{Name: "results", MountPath: "/mnt/results"},
		},
		`set -e
cat /mnt/bucket/seed.txt > /mnt/results/copy.txt
echo appended >> /mnt/bucket/seed.txt
mkdir /mnt/bucket/made
echo inside > /mnt/bucket/made/inside.txt
mv /mnt/bucket/rename-me.txt /mnt/bucket/renamed.csv
rm /mnt/bucket/delete-me.txt`)
	require.Equal(t, int32(1), exec.GetSucceededCount())

	assert.Equal(t, []string{"made/", "made/inside.txt", "renamed.csv", "results/copy.txt", "seed.txt"}, volumeObjectNames(t, bucket))
	assert.Equal(t, "seed\nappended\n", readVolumeObject(t, bucket, "seed.txt"))
	assert.Equal(t, "seed\n", readVolumeObject(t, bucket, "results/copy.txt"))
	assert.Equal(t, "inside\n", readVolumeObject(t, bucket, "made/inside.txt"))
	assert.Equal(t, "a,b\n", readVolumeObject(t, bucket, "renamed.csv"))

	rewritten, err := bucket.Object("seed.txt").Attrs(ctx)
	require.NoError(t, err)
	assert.Greater(t, rewritten.Generation, seed.Generation, "the overwrite is a new generation")
	assert.Equal(t, "text/plain", rewritten.ContentType, "the overwrite keeps the object's metadata")
	_, err = time.Parse(time.RFC3339Nano, rewritten.Metadata["gcsfuse_mtime"])
	assert.NoError(t, err, "the generation records the file's modification time")

	renamed, err := bucket.Object("renamed.csv").Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, "text/csv", renamed.ContentType, "a rename copies the object with its metadata")

	placeholder, err := bucket.Object("made/").Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), placeholder.Size)

	// A generation written through the API reaches the mount the next
	// execution reads, and that read does not write it back.
	updated := putVolumeObject(t, bucket, "seed.txt", "text/plain", "from the API\n")
	exec = runJobWithGCSVolumes(t, uniqueName("sdk-gcs-volume-read"),
		[]*runpb.Volume{cloudStorageVolume("bucket", bucketName, false)},
		[]*runpb.VolumeMount{{Name: "bucket", MountPath: "/mnt/bucket"}},
		`set -e
test "$(cat /mnt/bucket/seed.txt)" = "from the API"
test "$(cat /mnt/bucket/results/copy.txt)" = "seed"`)
	require.Equal(t, int32(1), exec.GetSucceededCount())
	current, err := bucket.Object("seed.txt").Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, updated.Generation, current.Generation, "reading the mounted file wrote no generation")
}

// A read-only Cloud Storage volume refuses the workload's writes, and nothing
// it tried reaches the bucket.
func TestSDK_CloudRun_JobReadOnlyCloudStorageVolume(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucketName := uniqueName("sdk-run-gcs-readonly")
	bucket := client.Bucket(bucketName)
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	seed := putVolumeObject(t, bucket, "seed.txt", "text/plain", "seed\n")

	exec := runJobWithGCSVolumes(t, uniqueName("sdk-gcs-readonly-job"),
		[]*runpb.Volume{cloudStorageVolume("bucket", bucketName, true)},
		[]*runpb.VolumeMount{{Name: "bucket", MountPath: "/mnt/bucket"}},
		`test "$(cat /mnt/bucket/seed.txt)" = "seed" || exit 2
if echo blocked > /mnt/bucket/blocked.txt; then exit 3; fi
if rm /mnt/bucket/seed.txt; then exit 4; fi
exit 0`)
	require.Equal(t, int32(1), exec.GetSucceededCount(), "the volume refused every write")

	assert.Equal(t, []string{"seed.txt"}, volumeObjectNames(t, bucket))
	current, err := bucket.Object("seed.txt").Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, strconv.FormatInt(seed.Generation, 10), strconv.FormatInt(current.Generation, 10))
}
