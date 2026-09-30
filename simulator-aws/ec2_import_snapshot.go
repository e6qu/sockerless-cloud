package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// EC2ImportSnapshotTask is a VM Import/Export task that reads a disk image
// from Amazon S3 into an EBS snapshot.
type EC2ImportSnapshotTask struct {
	ImportTaskId  string
	Description   string
	SnapshotId    string
	Status        string
	StatusMessage string
	Progress      string
	DiskImageSize float64
	Format        string
	S3Bucket      string
	S3Key         string
	Url           string
	Encrypted     bool
	KmsKeyId      string
	Tags          []EC2Tag
}

var ec2ImportSnapshotTasks sim.Store[EC2ImportSnapshotTask]

const ec2GiB = int64(1) << 30

// ec2ImportedSnapshotVolumeID is the volume ID EC2 reports for a snapshot
// that VM Import/Export created rather than one taken from a volume.
const ec2ImportedSnapshotVolumeID = "vol-ffffffff"

// ec2ImportSource resolves the disk container's UserBucket or its s3:// or
// Amazon S3 https URL to a bucket and key.
func ec2ImportSource(r *http.Request, prefix string) (bucket, key, rawURL string, err error) {
	bucket = r.FormValue(prefix + "UserBucket.S3Bucket")
	key = r.FormValue(prefix + "UserBucket.S3Key")
	rawURL = r.FormValue(prefix + "Url")
	if bucket != "" || key != "" {
		return bucket, key, rawURL, nil
	}
	if rawURL == "" {
		return "", "", "", errors.New("the disk container must specify a UserBucket or a Url")
	}
	u, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return "", "", "", fmt.Errorf("invalid disk container Url %q", rawURL)
	}
	switch {
	case u.Scheme == "s3":
		return u.Host, strings.TrimPrefix(u.Path, "/"), rawURL, nil
	case u.Scheme == "https" && strings.HasPrefix(u.Host, "s3.") && strings.HasSuffix(u.Host, ".amazonaws.com"):
		bucket, key, _ = strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		return bucket, key, rawURL, nil
	case u.Scheme == "https" && strings.Contains(u.Host, ".s3.") && strings.HasSuffix(u.Host, ".amazonaws.com"):
		return u.Host[:strings.Index(u.Host, ".s3.")], strings.TrimPrefix(u.Path, "/"), rawURL, nil
	}
	return "", "", "", fmt.Errorf("the disk container Url %q is not an Amazon S3 URL", rawURL)
}

func handleImportSnapshot(w http.ResponseWriter, r *http.Request) {
	format := strings.ToUpper(r.FormValue("DiskContainer.Format"))
	switch format {
	case "VHD", "VMDK", "RAW":
	default:
		ec2ErrorXML(w, "InvalidParameter", fmt.Sprintf("Unsupported disk container format %q. Valid values are VHD, VMDK and RAW.", r.FormValue("DiskContainer.Format")), http.StatusBadRequest)
		return
	}
	bucket, key, rawURL, err := ec2ImportSource(r, "DiskContainer.")
	if err != nil {
		ec2ErrorXML(w, "InvalidParameter", err.Error(), http.StatusBadRequest)
		return
	}
	if !ec2VMImportRoleCanRead(w, r, bucket, key) {
		return
	}
	task := EC2ImportSnapshotTask{
		ImportTaskId:  ec2ID("import-snap"),
		Description:   r.FormValue("Description"),
		Status:        "active",
		StatusMessage: "pending",
		Progress:      "0",
		Format:        format,
		S3Bucket:      bucket,
		S3Key:         key,
		Url:           rawURL,
		Encrypted:     r.FormValue("Encrypted") == "true",
		KmsKeyId:      r.FormValue("KmsKeyId"),
		Tags:          parseTags(r),
	}
	ec2ImportSnapshotTasks.Put(task.ImportTaskId, task)
	bg.Go(func() { ec2RunImportSnapshot(task) })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportSnapshotResponse %s><requestId>%s</requestId>%s</ImportSnapshotResponse>`,
		ec2Xmlns(), sim.NewUUID(), ec2ImportSnapshotTaskFieldsXML(task))
}

// ec2VMImportRoleCanRead checks that the VM Import/Export service role, the
// request's RoleName or else vmimport, can read the disk image, and writes the
// error EC2 returns when it cannot.
func ec2VMImportRoleCanRead(w http.ResponseWriter, r *http.Request, bucket, key string) bool {
	roleName := ec2Default(r.FormValue("RoleName"), "vmimport")
	roleARN := fmt.Sprintf("arn:aws:iam::%s:role/%s", awsAccountID(), roleName)
	if iamValidateServiceRole(roleARN, "vmie.amazonaws.com", map[string]string{
		"s3:GetObject": fmt.Sprintf("arn:aws:s3:::%s/%s", bucket, key),
	}) != nil {
		ec2ErrorXML(w, "InvalidParameter", fmt.Sprintf("The service role %s provided does not exist or does not have sufficient permissions", roleName), http.StatusBadRequest)
		return false
	}
	return true
}

// ec2RunImportSnapshot reads the disk image from Amazon S3, converts it into
// the snapshot's raw block image, and completes the task, or fails it with
// the message VM Import/Export reports.
func ec2RunImportSnapshot(task EC2ImportSnapshotTask) {
	snapshot, imageSize, failure := ec2ImportDiskImage(task.ImportTaskId, task.Format, task.S3Bucket, task.S3Key, task.Encrypted, task.KmsKeyId)
	ec2ImportSnapshotTasks.Update(task.ImportTaskId, func(t *EC2ImportSnapshotTask) {
		t.DiskImageSize = float64(imageSize)
		t.Progress = ""
		if failure != "" {
			t.Status = "error"
			t.StatusMessage = failure
			return
		}
		t.Status = "completed"
		t.StatusMessage = ""
		t.SnapshotId = snapshot.SnapshotId
	})
}

// ec2ImportDiskImage converts an import task's disk image into a completed
// snapshot. It returns the image's size in bytes and, when the import fails,
// the task's status message.
func ec2ImportDiskImage(taskID, format, bucket, key string, encrypted bool, kmsKeyID string) (EC2Snapshot, int64, string) {
	source := fmt.Sprintf("s3://%s/%s", bucket, key)
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		return EC2Snapshot{}, 0, "ClientError: Unable to read the disk image " + source + ": the object does not exist"
	}
	current, reader, err := s3OpenObject(obj)
	if err != nil {
		return EC2Snapshot{}, 0, fmt.Sprintf("ClientError: Unable to read the disk image %s: %v", source, err)
	}
	defer func() { _ = reader.Close() }()

	snapshot := EC2Snapshot{
		SnapshotId:  ec2ID("snap"),
		VolumeId:    ec2ImportedSnapshotVolumeID,
		State:       "completed",
		StartTime:   ec2NowRFC3339Milli(),
		Progress:    "100%",
		Description: "Created by AWS-VMImport service for " + taskID,
		OwnerId:     ec2Owner(),
		Encrypted:   encrypted,
		KmsKeyId:    kmsKeyID,
	}
	snapshot.HostPath = ebsSnapshotHostDirPath(snapshot.SnapshotId)
	diskSize, err := ec2WriteSnapshotImage(format, reader, current.Size, snapshot.HostPath)
	if err != nil {
		_ = os.RemoveAll(snapshot.HostPath)
		var diskErr *ec2DiskImageError
		if errors.As(err, &diskErr) {
			return EC2Snapshot{}, current.Size, diskErr.statusMessage()
		}
		return EC2Snapshot{}, current.Size, "ServerError: " + err.Error()
	}
	snapshot.VolumeSize = int(max(1, (diskSize+ec2GiB-1)/ec2GiB))
	ec2Snapshots.Put(snapshot.SnapshotId, snapshot)
	return snapshot, current.Size, ""
}

// ec2WriteSnapshotImage converts a disk image into the raw block image a
// snapshot directory holds, the file a volume restored from it starts from.
func ec2WriteSnapshotImage(format string, src io.ReaderAt, size int64, dir string) (int64, error) {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return 0, err
	}
	image, err := os.Create(filepath.Join(dir, "ebs.raw"))
	if err != nil {
		return 0, err
	}
	diskSize, err := ec2ConvertDiskImage(format, src, size, image)
	if err == nil {
		err = image.Truncate(diskSize)
	}
	if closeErr := image.Close(); err == nil {
		err = closeErr
	}
	return diskSize, err
}

func ec2ImportSnapshotTaskFieldsXML(t EC2ImportSnapshotTask) string {
	var detail strings.Builder
	if t.Description != "" {
		fmt.Fprintf(&detail, "<description>%s</description>", xmlEscape(t.Description))
	}
	if t.DiskImageSize > 0 {
		fmt.Fprintf(&detail, "<diskImageSize>%g</diskImageSize>", t.DiskImageSize)
	}
	fmt.Fprintf(&detail, "<encrypted>%t</encrypted><format>%s</format>", t.Encrypted, t.Format)
	if t.KmsKeyId != "" {
		fmt.Fprintf(&detail, "<kmsKeyId>%s</kmsKeyId>", xmlEscape(t.KmsKeyId))
	}
	if t.Progress != "" {
		fmt.Fprintf(&detail, "<progress>%s</progress>", t.Progress)
	}
	if t.SnapshotId != "" {
		fmt.Fprintf(&detail, "<snapshotId>%s</snapshotId>", t.SnapshotId)
	}
	fmt.Fprintf(&detail, "<status>%s</status>", t.Status)
	if t.StatusMessage != "" {
		fmt.Fprintf(&detail, "<statusMessage>%s</statusMessage>", xmlEscape(t.StatusMessage))
	}
	if t.Url != "" {
		fmt.Fprintf(&detail, "<url>%s</url>", xmlEscape(t.Url))
	}
	if t.S3Bucket != "" {
		fmt.Fprintf(&detail, "<userBucket><s3Bucket>%s</s3Bucket><s3Key>%s</s3Key></userBucket>", xmlEscape(t.S3Bucket), xmlEscape(t.S3Key))
	}
	var b strings.Builder
	if t.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(t.Description))
	}
	fmt.Fprintf(&b, "<importTaskId>%s</importTaskId><snapshotTaskDetail>%s</snapshotTaskDetail>%s",
		t.ImportTaskId, detail.String(), writeTagSetXML(t.Tags))
	return b.String()
}

func handleDescribeImportSnapshotTasks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ImportTaskId")
	results := make([]EC2ImportSnapshotTask, 0)
	for _, t := range ec2ImportSnapshotTasks.List() {
		if len(ids) > 0 && !ec2StrInValues(t.ImportTaskId, ids) {
			continue
		}
		results = append(results, t)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ImportTaskId < results[j].ImportTaskId })
	results, nextToken, pageOK := awsPage(w, ec2BadToken, results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
	if !pageOK {
		return
	}
	var items strings.Builder
	for _, t := range results {
		items.WriteString("<item>" + ec2ImportSnapshotTaskFieldsXML(t) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImportSnapshotTasksResponse %s><requestId>%s</requestId><importSnapshotTaskSet>%s</importSnapshotTaskSet>%s</DescribeImportSnapshotTasksResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String(), ec2NextTokenXML(nextToken))
}
