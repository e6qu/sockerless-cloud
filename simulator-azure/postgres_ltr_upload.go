package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A long-term-retention backup lands in the Blob container each SAS URI
// names, written through the Blob service with that SAS, as one block blob
// named after the backup holding the captured volume as a tar archive.

const pgLtrBlobVersion = "2021-08-06"

// pgLtrBlobURL is the URL of the backup's blob inside the container a SAS
// URI names, carrying the SAS.
func pgLtrBlobURL(sasURI, backupName string) (string, error) {
	u, err := url.Parse(sasURI)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("the SAS URI %q is not a Blob container URL", sasURI)
	}
	container := strings.Trim(u.Path, "/")
	if container == "" || strings.Contains(container, "/") {
		return "", fmt.Errorf("the SAS URI %q does not name one Blob container", sasURI)
	}
	if u.Query().Get("sig") == "" {
		return "", fmt.Errorf("the SAS URI %q carries no signature", sasURI)
	}
	u.Path = "/" + container + "/" + backupName
	return u.String(), nil
}

// pgLtrArchive reads the backup volume as a tar archive; a server that never
// started, with no volume, archives nothing.
func pgLtrArchive(ctx context.Context, volume string) ([]byte, error) {
	if !sim.VolumeExists(volume) {
		var empty bytes.Buffer
		if err := tar.NewWriter(&empty).Close(); err != nil {
			return nil, err
		}
		return empty.Bytes(), nil
	}
	archive, err := sim.ArchiveVolume(ctx, volume)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.Close() }()
	return io.ReadAll(archive)
}

// pgLtrUpload puts the archive at each blob URL, as the server would through
// the Blob service.
func pgLtrUpload(ctx context.Context, serverID string, blobURLs []string, archive []byte) error {
	for _, target := range blobURLs {
		header := http.Header{}
		header.Set("x-ms-blob-type", "BlockBlob")
		header.Set("x-ms-version", pgLtrBlobVersion)
		header.Set("Content-Type", "application/x-tar")
		status, body, err := azureCallAs(ctx, "Microsoft.DBforPostgreSQL", serverID, http.MethodPut, target, header, archive)
		if err != nil {
			return fmt.Errorf("write the backup to its storage container: %w", err)
		}
		if status != http.StatusCreated {
			return fmt.Errorf("the storage container refused the backup: %d %s: %s", status, http.StatusText(status), strings.TrimSpace(string(body)))
		}
	}
	return nil
}
