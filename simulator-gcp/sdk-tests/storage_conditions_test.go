package gcp_sdk_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
)

// Generation preconditions are what a client builds compare-and-swap on:
// ifGenerationMatch=0 creates only what does not exist, ifGenerationMatch=N
// replaces only version N, and a generation is never given to two versions.

func writeObject(t *testing.T, object *storage.ObjectHandle, content string) (*storage.ObjectAttrs, error) {
	t.Helper()
	w := object.NewWriter(ctx)
	if _, err := w.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return w.Attrs(), nil
}

func requirePreconditionFailed(t *testing.T, err error, what string) {
	t.Helper()
	var refused *googleapi.Error
	require.True(t, errors.As(err, &refused), "%s: want a *googleapi.Error, got %T: %v", what, err, err)
	require.Equal(t, http.StatusPreconditionFailed, refused.Code, "%s: %v", what, err)
}

func TestGCS_GenerationPreconditions(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := client.Bucket("preconditions-bucket")
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	object := bucket.Object("manifest")

	first, err := writeObject(t, object.If(storage.Conditions{DoesNotExist: true}), "one")
	require.NoError(t, err)
	_, err = writeObject(t, object.If(storage.Conditions{DoesNotExist: true}), "two")
	requirePreconditionFailed(t, err, "a create of an object that exists")

	second, err := writeObject(t, object.If(storage.Conditions{GenerationMatch: first.Generation}), "two")
	require.NoError(t, err)
	_, err = writeObject(t, object.If(storage.Conditions{GenerationMatch: first.Generation}), "three")
	requirePreconditionFailed(t, err, "a replace of a version since replaced")

	_, err = object.If(storage.Conditions{MetagenerationMatch: second.Metageneration + 1}).
		Update(ctx, storage.ObjectAttrsToUpdate{ContentType: "text/plain"})
	requirePreconditionFailed(t, err, "a metadata update at another metageneration")

	_, err = object.If(storage.Conditions{GenerationMatch: first.Generation}).Attrs(ctx)
	requirePreconditionFailed(t, err, "a read of a version since replaced")

	err = object.If(storage.Conditions{GenerationMatch: first.Generation}).Delete(ctx)
	requirePreconditionFailed(t, err, "a delete of a version since replaced")
	require.NoError(t, object.If(storage.Conditions{GenerationMatch: second.Generation}).Delete(ctx))

	// A recreated object is a new version, and its generation says so.
	recreated, err := writeObject(t, object.If(storage.Conditions{DoesNotExist: true}), "again")
	require.NoError(t, err)
	require.Greater(t, recreated.Generation, second.Generation, "a recreated object's generation")

	copied := bucket.Object("copied")
	_, err = copied.CopierFrom(object.If(storage.Conditions{GenerationMatch: first.Generation})).Run(ctx)
	requirePreconditionFailed(t, err, "a copy from a version since replaced")
	_, err = copied.If(storage.Conditions{DoesNotExist: true}).CopierFrom(object).Run(ctx)
	require.NoError(t, err)
	_, err = copied.If(storage.Conditions{DoesNotExist: true}).CopierFrom(object).Run(ctx)
	requirePreconditionFailed(t, err, "a copy onto an object that exists, if absent")
}

// TestGCS_ConditionalWritesArbitrate races writers that each require the state
// they read. Cloud Storage evaluates a write's preconditions and applies it as
// one step, so exactly one writer of each round succeeds.
func TestGCS_ConditionalWritesArbitrate(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := client.Bucket("arbitrate-bucket")
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	object := bucket.Object("contended")
	const writers = 16

	race := func(write func(i int) error) (won int, lost []error) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := write(i)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					won++
				} else {
					lost = append(lost, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		return won, lost
	}

	won, lost := race(func(i int) error {
		_, err := writeObject(t, object.If(storage.Conditions{DoesNotExist: true}), fmt.Sprint(i))
		return err
	})
	require.Equal(t, 1, won, "conditional creates that succeeded")
	for _, err := range lost {
		requirePreconditionFailed(t, err, "a create that lost")
	}

	attrs, err := object.Attrs(ctx)
	require.NoError(t, err)
	won, lost = race(func(i int) error {
		_, err := writeObject(t, object.If(storage.Conditions{GenerationMatch: attrs.Generation}), fmt.Sprint("replace", i))
		return err
	})
	require.Equal(t, 1, won, "conditional replaces of one generation that succeeded")
	for _, err := range lost {
		requirePreconditionFailed(t, err, "a replace that lost")
	}
}

// TestGCS_RangedReadCarriesTheObjectsDescription reads part of an object the
// way the client library does, through the XML API, which describes the object
// in the headers of the read itself.
func TestGCS_RangedReadCarriesTheObjectsDescription(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := client.Bucket("ranged-read-bucket")
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	object := bucket.Object("pack")
	written, err := writeObject(t, object, "0123456789")
	require.NoError(t, err)

	reader, err := object.NewRangeReader(ctx, 2, 4)
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, "2345", string(body))
	require.Equal(t, int64(10), reader.Attrs.Size, "a ranged read reports the whole object's size")
	require.Equal(t, written.Generation, reader.Attrs.Generation)

	_, err = object.NewRangeReader(ctx, 10, 1)
	require.Error(t, err, "a range that begins at the end of the object")
}

// TestGCS_V4SignedURLReadsWithoutCredentials signs a URL with a key minted
// through the IAM API, as a deployment would, and reads the object with no
// credential but the URL.
func TestGCS_V4SignedURLReadsWithoutCredentials(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := client.Bucket("signed-url-bucket")
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	_, err := writeObject(t, bucket.Object("dir/asset name"), "signed content")
	require.NoError(t, err)

	svc := iamService(t)
	sa, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{AccountId: "url-signer"}).Do()
	require.NoError(t, err)
	key, err := svc.Projects.ServiceAccounts.Keys.Create(sa.Name, &iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	require.NoError(t, err)
	var keyFile struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	require.NoError(t, json.Unmarshal(raw, &keyFile))

	sign := func(expires time.Duration) string {
		signed, err := storage.SignedURL("signed-url-bucket", "dir/asset name", &storage.SignedURLOptions{
			GoogleAccessID: keyFile.ClientEmail,
			PrivateKey:     []byte(keyFile.PrivateKey),
			Method:         http.MethodGet,
			Expires:        time.Now().Add(expires),
			Scheme:         storage.SigningSchemeV4,
			Hostname:       strings.TrimPrefix(baseURL, "http://"),
			Insecure:       true,
		})
		require.NoError(t, err)
		return signed
	}
	get := func(target string) (int, string) {
		response, err := http.Get(target) // #nosec G107 -- the URL the test just signed
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, string(body)
	}

	signed := sign(time.Hour)
	status, body := get(signed)
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "signed content", body)

	tampered := strings.Replace(signed, "asset%20name", "other", 1)
	status, body = get(tampered)
	require.Equal(t, http.StatusForbidden, status, "a URL whose path was changed after signing: %s", body)
	require.Contains(t, body, "SignatureDoesNotMatch")

	short := sign(3 * time.Second)
	parsed, err := url.Parse(short)
	require.NoError(t, err)
	issued, err := time.Parse("20060102T150405Z", parsed.Query().Get("X-Goog-Date"))
	require.NoError(t, err)
	seconds, err := strconv.Atoi(parsed.Query().Get("X-Goog-Expires"))
	require.NoError(t, err)
	time.Sleep(time.Until(issued.Add(time.Duration(seconds+1) * time.Second)))
	status, body = get(short)
	require.Equal(t, http.StatusBadRequest, status, "an expired URL: %s", body)
	require.Contains(t, body, "ExpiredToken")
}

// TestGCS_BatchDeletesAnswerEachCall sends object deletes in one JSON API batch
// and reads each call's answer back by its Content-ID.
func TestGCS_BatchDeletesAnswerEachCall(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := client.Bucket("batch-bucket")
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	names := []string{"a", "dir/b", "c", "never-existed"}
	for _, name := range names[:3] {
		_, err := writeObject(t, bucket.Object(name), name)
		require.NoError(t, err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for i, name := range names {
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type": {"application/http"},
			"Content-ID":   {fmt.Sprintf("<item%d>", i)},
		})
		require.NoError(t, err)
		fmt.Fprintf(part, "DELETE /storage/v1/b/batch-bucket/o/%s HTTP/1.1\r\n\r\n", url.PathEscape(name))
	}
	require.NoError(t, writer.Close())
	request, err := http.NewRequest(http.MethodPost, baseURL+"/batch/storage/v1", &body)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	response, err := simAuthHTTPClient().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	mediaType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/mixed", mediaType)
	statuses := map[string]int{}
	parts := multipart.NewReader(response.Body, parameters["boundary"])
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		answer, err := http.ReadResponse(bufio.NewReader(part), nil)
		require.NoError(t, err)
		statuses[part.Header.Get("Content-ID")] = answer.StatusCode
	}
	require.Equal(t, map[string]int{
		"<response-item0>": http.StatusNoContent,
		"<response-item1>": http.StatusNoContent,
		"<response-item2>": http.StatusNoContent,
		"<response-item3>": http.StatusNotFound,
	}, statuses)

	for _, name := range names[:3] {
		_, err := bucket.Object(name).Attrs(ctx)
		require.ErrorIs(t, err, storage.ErrObjectNotExist, "%s after its batched delete", name)
	}
}
