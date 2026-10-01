package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// The build source a client uploads before it schedules a run lives as a blob
// in storage the registry service owns, the way ACR hands out a Shared Access
// Signature URL into its own storage account. The account name carries a
// hyphen, which no customer storage account name can, so it never collides
// with an account a client creates.
const (
	acrBuildSourceAccount    = "acr-build-source"
	acrBuildSourcePathPrefix = "/acr/v1/build-source/"
	acrBuildSourceSASVersion = "2021-08-06"
	acrBuildSourceSASWindow  = time.Hour
	// acrWorkspaceMaxBytes bounds how far a run's source may expand on the
	// build host.
	acrWorkspaceMaxBytes = 10 << 30
)

func acrIsBuildSourcePath(p string) bool {
	return strings.HasPrefix(p, acrBuildSourcePathPrefix)
}

// acrBuildSourceContainer is the container holding one registry's uploads.
// Registry names are globally unique, which is what lets them name it.
func acrBuildSourceContainer(registryName string) string {
	return strings.ToLower(registryName)
}

func acrBuildSourceKey() []byte {
	material, err := base64.StdEncoding.DecodeString(azureKeyMaterial64("/acr/build-source", "key1"))
	if err != nil {
		panic(fmt.Sprintf("build source signing key: %v", err))
	}
	return material
}

func acrBuildSourceSign(container, blob string, q url.Values) string {
	mac := hmac.New(sha256.New, acrBuildSourceKey())
	_, _ = mac.Write([]byte(blobSASStringToSign("blob", acrBuildSourceAccount, container, blob, q)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// handleACRListBuildSourceUploadURL answers Registries_GetBuildSourceUploadUrl
// with a blob URL signed for writing, and the path a run request names the
// uploaded source by.
func handleACRListBuildSourceUploadURL(w http.ResponseWriter, r *http.Request) {
	if _, ok := acrRegistryID(r); !ok {
		acrRegistryNotFound(w, r)
		return
	}
	container := acrBuildSourceContainer(sim.PathParam(r, "registryName"))
	mirrorARMContainerToBlobPlane(acrBuildSourceAccount, container, "", nil)
	now := time.Now().UTC()
	rel := fmt.Sprintf("source/%s/%s.tar.gz", now.Format("200601021504"), sim.NewUUID())
	q := url.Values{}
	q.Set("sv", acrBuildSourceSASVersion)
	q.Set("sr", "b")
	q.Set("sp", "cw")
	q.Set("se", now.Add(acrBuildSourceSASWindow).Format(time.RFC3339))
	q.Set("sig", acrBuildSourceSign(container, rel, q))
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"uploadUrl":    fmt.Sprintf("%s://%s%s%s/%s?%s", azureRequestScheme(r), r.Host, acrBuildSourcePathPrefix, container, rel, q.Encode()),
		"relativePath": rel,
	})
}

// handleACRBuildSource serves the upload URL with the Blob service's own
// protocol, Put Blob and Put Block List included, once the request's Shared
// Access Signature verifies against the key the registry service signed it
// with.
func handleACRBuildSource(w http.ResponseWriter, r *http.Request) {
	container, blob, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, acrBuildSourcePathPrefix), "/")
	if !ok || container == "" || blob == "" {
		writeStorageError(w, "InvalidUri", "The requested URI does not represent any resource on the server.", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if q.Get("sig") == "" {
		blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed",
			"Server failed to authenticate the request. Make sure the value of Authorization header is formed correctly including the signature.")
		return
	}
	for _, name := range blobAuthRequiredSASParams {
		if q.Get(name) == "" {
			blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed", "Missing mandatory parameters for valid Shared Access Signature.")
			return
		}
	}
	expiry, ok := storageSASTime(q.Get("se"))
	if !ok || time.Now().UTC().After(expiry) {
		blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed", "Signature not valid in the specified time frame.")
		return
	}
	if !blobSASPermits(q.Get("sp"), r.Method) {
		blobAuthorizationError(w, http.StatusForbidden, "AuthorizationPermissionMismatch",
			"This request is not authorized to perform this operation using this permission.")
		return
	}
	if !hmac.Equal([]byte(q.Get("sig")), []byte(acrBuildSourceSign(container, blob, q))) {
		blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed",
			"Signature did not match. String to sign used was "+
				strings.ReplaceAll(blobSASStringToSign("blob", acrBuildSourceAccount, container, blob, q), "\n", `\n`))
		return
	}
	r = StorageMarkAuthorized(r)
	r.URL.Path = "/" + container + "/" + blob
	r.URL.RawPath = ""
	handleBlobDataPlane(w, r, acrBuildSourceAccount)
}

// acrOpenSource opens the source archive a run request names: an absolute blob
// URL in a storage account, or the relative path listBuildSourceUploadUrl
// handed out for this registry. An empty location is a run without source.
func acrOpenSource(reg Registry, location string) (io.ReadCloser, error) {
	if location == "" {
		return nil, nil
	}
	var account, container, blob string
	if u, err := url.Parse(location); err == nil && u.Scheme != "" {
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("source location %q is not a tar archive URL", location)
		}
		if account, container, blob, err = parseACRBlobURL(location); err != nil {
			return nil, fmt.Errorf("parse sourceLocation: %w", err)
		}
	} else {
		account, container, blob = acrBuildSourceAccount, acrBuildSourceContainer(reg.Name), strings.TrimPrefix(location, "/")
	}
	obj, ok := blobObjects.Get(blobObjectKey(account, container, blob))
	if !ok {
		if account == acrBuildSourceAccount {
			return nil, fmt.Errorf("source %s was not found: upload it to the URL listBuildSourceUploadUrl returned", location)
		}
		return nil, fmt.Errorf("source context blob %s/%s not found in storage account %s", container, blob, account)
	}
	_, body, err := blobOpen(obj)
	if err != nil {
		return nil, fmt.Errorf("read the source: %w", err)
	}
	return body, nil
}

// acrExtractSource unpacks a source tar archive, gzip-compressed or not, into
// dir. The az CLI archives the source directory itself as an entry named "/",
// and `docker cp` names it "./"; both are the root dir already is.
func acrExtractSource(r io.Reader, dir string) error {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("open gzip stream: %w", err)
		}
		defer func() { _ = gz.Close() }()
		src = gz
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(acrDropRootEntries(src, pw))
	}()
	err := archive.ExtractTar(pr, dir, acrWorkspaceMaxBytes)
	_ = pr.CloseWithError(err)
	return err
}

func acrDropRootEntries(src io.Reader, dst io.Writer) error {
	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return tw.Close()
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if name := path.Clean("/" + hdr.Name); name == "/" {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}

// acrTarDirectory writes dir's tree to w as a tar archive, the form `docker
// cp -` reads.
func acrTarDirectory(dir string, w io.Writer) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
