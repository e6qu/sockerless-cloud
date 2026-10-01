package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func arTestTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func arTestGzip(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// arTestDeb builds a Debian binary package the way dpkg-deb lays one out: an
// ar archive of debian-binary, control.tar and data.tar.
func arTestDeb(t *testing.T, control, compression string) []byte {
	t.Helper()
	controlTar := arTestTar(t, map[string]string{"./control": control})
	var member []byte
	switch compression {
	case "":
		member = controlTar
	case ".gz":
		member = arTestGzip(t, controlTar)
	case ".xz":
		var buf bytes.Buffer
		xw, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := xw.Write(controlTar); err != nil {
			t.Fatal(err)
		}
		if err := xw.Close(); err != nil {
			t.Fatal(err)
		}
		member = buf.Bytes()
	case ".zst":
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		member = enc.EncodeAll(controlTar, nil)
	}
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	add := func(name string, data []byte) {
		fmt.Fprintf(&buf, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(data))
		buf.Write(data)
		if len(data)%2 == 1 {
			buf.WriteByte('\n')
		}
	}
	add("debian-binary", []byte("2.0\n"))
	add("control.tar"+compression, member)
	add("data.tar.gz", arTestGzip(t, arTestTar(t, map[string]string{"./usr/share/doc/hello/README": "hello\n"})))
	return buf.Bytes()
}

// arTestRPM builds an RPM package: the lead, an empty signature header, and a
// header carrying the name, version, release, epoch and arch tags.
func arTestRPM(name, version, release, arch string, epoch uint32, source bool) []byte {
	lead := make([]byte, 96)
	copy(lead, []byte{0xed, 0xab, 0xee, 0xdb, 3, 0})
	if source {
		binary.BigEndian.PutUint16(lead[6:8], 1)
	}
	binary.BigEndian.PutUint16(lead[8:10], 1)
	copy(lead[10:76], name+"-"+version+"-"+release)
	binary.BigEndian.PutUint16(lead[76:78], 1)
	binary.BigEndian.PutUint16(lead[78:80], 5)

	header := func(entries [][4]uint32, store []byte) []byte {
		var b bytes.Buffer
		b.Write([]byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0})
		_ = binary.Write(&b, binary.BigEndian, uint32(len(entries)))
		_ = binary.Write(&b, binary.BigEndian, uint32(len(store)))
		for _, e := range entries {
			_ = binary.Write(&b, binary.BigEndian, e)
		}
		b.Write(store)
		return b.Bytes()
	}
	var store bytes.Buffer
	var entries [][4]uint32
	if epoch != 0 {
		entries = append(entries, [4]uint32{rpmTagEpoch, 4, uint32(store.Len()), 1})
		_ = binary.Write(&store, binary.BigEndian, epoch)
	}
	for _, tag := range []struct {
		tag   uint32
		value string
	}{{rpmTagName, name}, {rpmTagVersion, version}, {rpmTagRelease, release}, {rpmTagArch, arch}} {
		entries = append(entries, [4]uint32{tag.tag, 6, uint32(store.Len()), 1})
		store.WriteString(tag.value)
		store.WriteByte(0)
	}
	out := append(lead, header(nil, nil)...)
	out = append(out, header(entries, store.Bytes())...)
	return append(out, []byte("payload")...)
}

func arTestGoo(t *testing.T, name, version, arch string) []byte {
	t.Helper()
	spec := fmt.Sprintf(`{"Name":%q,"Version":%q,"Arch":%q,"Description":"test package"}`, name, version, arch)
	return arTestGzip(t, arTestTar(t, map[string]string{name + ".pkgspec": spec, "files/tool.exe": "MZ"}))
}

func arTestGoModuleZip(t *testing.T, module, version, goMod string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{module + "@" + version + "/hello.go": "package hello\n"}
	if goMod != "" {
		files[module+"@"+version+"/go.mod"] = goMod
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const arTestControl = "Package: hello\nVersion: 1:2.10-2\nArchitecture: amd64\nMaintainer: Test <test@example.com>\nDescription: greeting\n  a longer description\n"

func TestARParseDebReadsEveryControlCompression(t *testing.T) {
	for _, compression := range []string{"", ".gz", ".xz", ".zst"} {
		t.Run("control.tar"+compression, func(t *testing.T) {
			parsed, err := arParseDeb(arTestDeb(t, arTestControl, compression))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.PackageName != "hello" || parsed.Version != "1:2.10-2" || parsed.Architecture != "amd64" {
				t.Fatalf("parsed %+v", parsed)
			}
			if parsed.FileName != "hello_2.10-2_amd64.deb" || parsed.PackageType != "BINARY" {
				t.Fatalf("file %q type %q", parsed.FileName, parsed.PackageType)
			}
			if string(parsed.ControlFile) != arTestControl {
				t.Fatalf("control file %q", parsed.ControlFile)
			}
		})
	}
	if _, err := arParseDeb([]byte("not an archive")); err == nil {
		t.Fatal("a file that is not an ar archive parsed")
	}
	if _, err := arParseDeb(arTestDeb(t, "Package: hello\n", ".gz")); err == nil || !strings.Contains(err.Error(), "Version") {
		t.Fatalf("a control file without Version: %v", err)
	}
}

func TestARParseRPMTellsBinaryFromSource(t *testing.T) {
	parsed, err := arParseRPM(arTestRPM("hello", "2.10", "1.el9", "x86_64", 2, false))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.PackageName != "hello" || parsed.Version != "2:2.10-1.el9" || parsed.Architecture != "x86_64" ||
		parsed.FileName != "hello-2.10-1.el9.x86_64.rpm" || parsed.PackageType != "BINARY" {
		t.Fatalf("binary package parsed as %+v", parsed)
	}
	parsed, err = arParseRPM(arTestRPM("hello", "2.10", "1", "x86_64", 0, true))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != "2.10-1" || parsed.FileName != "hello-2.10-1.src.rpm" || parsed.PackageType != "SOURCE" {
		t.Fatalf("source package parsed as %+v", parsed)
	}
	if _, err := arParseRPM([]byte("#!/bin/sh")); err == nil {
		t.Fatal("a file that is not an RPM parsed")
	}
}

func TestARParseGooAndGoModuleAndKfp(t *testing.T) {
	goo, err := arParseGoo(arTestGoo(t, "tool", "1.2.3@4", "x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	if goo.PackageName != "tool" || goo.Version != "1.2.3@4" || goo.FileName != "tool.x86_64.1.2.3@4.goo" {
		t.Fatalf("goo parsed as %+v", goo)
	}

	module, err := arParseGoModuleZip(arTestGoModuleZip(t, "example.com/Hello", "v1.2.0", "module example.com/Hello\n\ngo 1.22\n"))
	if err != nil {
		t.Fatal(err)
	}
	if module.Path != "example.com/Hello" || module.Version != "v1.2.0" || arGoEscapePath(module.Path) != "example.com/!hello" {
		t.Fatalf("module parsed as %+v", module)
	}
	synthesized, err := arParseGoModuleZip(arTestGoModuleZip(t, "example.com/nomod", "v0.1.0", ""))
	if err != nil || string(synthesized.GoMod) != "module example.com/nomod\n" {
		t.Fatalf("a module without go.mod: %q, %v", synthesized.GoMod, err)
	}
	if _, err := arParseGoModuleZip(arTestGoModuleZip(t, "example.com/a", "v1.0.0", "module example.com/b\n")); err == nil {
		t.Fatal("a zip whose go.mod names another module parsed")
	}
	if _, err := arParseGoModuleZip(arTestGoModuleZip(t, "example.com/a", "1.0", "module example.com/a\n")); err == nil {
		t.Fatal("a non-canonical version parsed")
	}

	template := []byte("pipelineInfo:\n  name: hello-pipeline\nschemaVersion: 2.1.0\n")
	pipeline, err := arParseKfp(template)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(template)
	if pipeline.Name != "hello-pipeline" || pipeline.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("pipeline parsed as %+v", pipeline)
	}
}

func arTestCall(t *testing.T, srv *sim.Server, method, path, contentType string, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, "http://artifactregistry.googleapis.com"+path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// arTestMultipart builds the multipart/related body a Google API client sends
// for a media upload: the request message, then the media.
func arTestMultipart(t *testing.T, request string, media []byte, mediaType string) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte(request))
	part, err = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mediaType}})
	if err != nil {
		t.Fatal(err)
	}
	part.Write(media)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return "multipart/related; boundary=" + mw.Boundary(), buf.Bytes()
}

func arTestUpload(t *testing.T, srv *sim.Server, repo, kind, request string, media []byte, mediaType string) (int, map[string]any) {
	t.Helper()
	contentType, body := arTestMultipart(t, request, media, mediaType)
	method := kind + ":create"
	if kind == "files" {
		method = "files:upload"
	}
	code, out := arTestCall(t, srv, http.MethodPost, "/upload/v1/"+repo+"/"+method+"?uploadType=multipart", contentType, body)
	decoded := map[string]any{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("upload answered %d %s", code, out)
	}
	return code, decoded
}

// arTestOperationResponse decodes a media upload's operation response into the
// message the method declares. The Go protos carry no Upload* metadata
// messages, so the operation is read from the wire rather than over gRPC.
func arTestOperationResponse(t *testing.T, upload map[string]any, response proto.Message) {
	t.Helper()
	op, _ := upload["operation"].(map[string]any)
	if op["done"] != true {
		t.Fatalf("upload operation = %v", op)
	}
	fields := map[string]any{}
	for k, v := range op["response"].(map[string]any) {
		if k != "@type" {
			fields[k] = v
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal(encoded, response); err != nil {
		t.Fatalf("operation response %s: %v", encoded, err)
	}
}

func arTestRepository(t *testing.T, srv *sim.Server, repoID, format string) string {
	t.Helper()
	gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/us-central1/repositories?repositoryId="+repoID, `{"format":"`+format+`"}`)
	return "projects/p/locations/us-central1/repositories/" + repoID
}

func arTestDownload(t *testing.T, srv *sim.Server, fileName string) []byte {
	t.Helper()
	code, body := arTestCall(t, srv, http.MethodGet, "/download/v1/"+fileName+":download?alt=media", "", nil)
	if code != http.StatusOK {
		t.Fatalf("download %s answered %d %s", fileName, code, body)
	}
	return body
}

// A generic upload stores the bytes as the File UploadGenericArtifactRequest
// names, under the Package and Version it names; a second upload of the file
// is ALREADY_EXISTS, and deleting the version deletes the file.
func TestARGenericUploadStoresTheArtifact(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "gen-repo", "GENERIC")
	content := []byte("generic artifact bytes\n")

	code, out := arTestUpload(t, srv, repo, "genericArtifacts",
		`{"packageId":"tools","versionId":"1.0.0","filename":"tool.tar.gz","versionAnnotations":{"team":"build"}}`, content, "application/gzip")
	if code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	artifact := &artifactregistrypb.GenericArtifact{}
	arTestOperationResponse(t, out, artifact)
	if artifact.GetName() != repo+"/genericArtifacts/tools:1.0.0" || artifact.GetVersion() != "1.0.0" || artifact.GetCreateTime() == nil {
		t.Fatalf("generic artifact = %v", artifact)
	}

	fileName := repo + "/files/tools:1.0.0:tool.tar.gz"
	file := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+fileName, ``)
	version := repo + "/packages/tools/versions/1.0.0"
	sum := sha256.Sum256(content)
	hashes, _ := file["hashes"].([]any)
	if file["owner"] != version || file["sizeBytes"] != fmt.Sprint(len(content)) || len(hashes) != 1 ||
		hashes[0].(map[string]any)["value"] != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("file = %v", file)
	}
	if got := arTestDownload(t, srv, fileName); !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q", got)
	}
	v := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+version, ``)
	if annotations, _ := v["annotations"].(map[string]any); annotations["team"] != "build" {
		t.Fatalf("version = %v", v)
	}

	code, out = arTestUpload(t, srv, repo, "genericArtifacts", `{"packageId":"tools","versionId":"1.0.0","filename":"tool.tar.gz"}`, content, "application/gzip")
	if code != http.StatusConflict {
		t.Fatalf("a second upload of the file answered %d %v", code, out)
	}
	code, out = arTestUpload(t, srv, repo, "genericArtifacts",
		`{"packageId":"tools","versionId":"1.0.0","filename":"other.txt","versionAnnotations":{"x":"y"}}`, content, "text/plain")
	if code != http.StatusBadRequest {
		t.Fatalf("annotations on an existing version answered %d %v", code, out)
	}
	for _, request := range []string{
		`{"packageId":"tools","versionId":"latest","filename":"a"}`,
		`{"packageId":"tools","versionId":"Upper","filename":"a"}`,
		`{"packageId":"-tools","versionId":"1","filename":"a"}`,
		`{"packageId":"tools","versionId":"1","filename":"a/b"}`,
	} {
		if code, out := arTestUpload(t, srv, repo, "genericArtifacts", request, content, "text/plain"); code != http.StatusBadRequest {
			t.Errorf("%s answered %d %v", request, code, out)
		}
	}

	dockerRepo := arTestRepository(t, srv, "gen-docker", "DOCKER")
	if code, out := arTestUpload(t, srv, dockerRepo, "genericArtifacts", `{"packageId":"a","versionId":"1","filename":"a"}`, content, "text/plain"); code != http.StatusBadRequest {
		t.Fatalf("a generic upload to a Docker repository answered %d %v", code, out)
	}

	gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodDelete, "/v1/"+version, ``)
	if code, _ := gcpHostCall(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+fileName, ``); code != http.StatusNotFound {
		t.Fatalf("the deleted version's file answered %d", code)
	}
}

// files.upload stores the bytes it is sent, not the multipart envelope, under
// the fileId the request names or the content's sha256 digest.
func TestARFileUploadStoresTheMedia(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "file-repo", "GENERIC")
	content := []byte("attachment")
	code, out := arTestUpload(t, srv, repo, "files", `{"fileId":"docs/readme.txt"}`, content, "text/plain")
	if code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	fileName := repo + "/files/docs%2Freadme.txt"
	file := &artifactregistrypb.File{}
	arTestOperationResponse(t, out, file)
	sum := sha256.Sum256(content)
	if file.GetName() != fileName || file.GetSizeBytes() != int64(len(content)) || !bytes.Equal(file.GetHashes()[0].GetValue(), sum[:]) {
		t.Fatalf("file = %v", file)
	}
	if got := arTestDownload(t, srv, fileName); !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q", got)
	}

	code, out = arTestUpload(t, srv, repo, "files", `{}`, content, "text/plain")
	if code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	digestName := repo + "/files/sha256:" + hex.EncodeToString(sum[:])
	if got := arTestDownload(t, srv, digestName); !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q", got)
	}
}

// Apt, Yum and GooGet uploads record the package and version the artifact's
// own metadata declares; an upload that conflicts with an existing file is
// ignored and reports no artifact.
func TestARPackageUploadsRecordWhatTheArtifactDeclares(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	for _, tc := range []struct {
		kind, format, mediaType string
		artifact                []byte
		version, file           string
	}{
		{kind: "aptArtifacts", format: "APT", mediaType: "application/vnd.debian.binary-package",
			artifact: arTestDeb(t, arTestControl, ".xz"), version: "/packages/hello/versions/1:2.10-2", file: "/files/hello_2.10-2_amd64.deb"},
		{kind: "yumArtifacts", format: "YUM", mediaType: "application/x-rpm",
			artifact: arTestRPM("hello", "2.10", "1", "x86_64", 0, false), version: "/packages/hello/versions/2.10-1", file: "/files/hello-2.10-1.x86_64.rpm"},
		{kind: "googetArtifacts", format: "GOOGET", mediaType: "application/octet-stream",
			artifact: arTestGoo(t, "tool", "1.0.0@1", "x86_64"), version: "/packages/tool/versions/1.0.0@1", file: "/files/tool.x86_64.1.0.0@1.goo"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			repo := arTestRepository(t, srv, strings.ToLower(tc.format)+"-repo", tc.format)
			code, out := arTestUpload(t, srv, repo, tc.kind, `{}`, tc.artifact, tc.mediaType)
			if code != http.StatusOK {
				t.Fatalf("upload answered %d %v", code, out)
			}
			op := out["operation"].(map[string]any)
			published, _ := op["response"].(map[string]any)[tc.kind].([]any)
			if len(published) != 1 {
				t.Fatalf("upload response = %v", op["response"])
			}
			gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+repo+tc.version, ``)
			file := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+repo+tc.file, ``)
			if file["owner"] != repo+tc.version {
				t.Fatalf("file = %v", file)
			}
			if got := arTestDownload(t, srv, repo+tc.file); !bytes.Equal(got, tc.artifact) {
				t.Fatal("the downloaded file differs from the upload")
			}
			code, out = arTestUpload(t, srv, repo, tc.kind, `{}`, tc.artifact, tc.mediaType)
			if code != http.StatusOK {
				t.Fatalf("a conflicting upload answered %d %v", code, out)
			}
			if again, _ := out["operation"].(map[string]any)["response"].(map[string]any)[tc.kind].([]any); len(again) != 0 {
				t.Fatalf("a conflicting upload reported %v", again)
			}
		})
	}

	repo := "projects/p/locations/us-central1/repositories/apt-repo"
	code, out := arTestUpload(t, srv, repo, "aptArtifacts", `{}`, arTestDeb(t, strings.ReplaceAll(arTestControl, "hello", "world"), ".gz"), "application/octet-stream")
	if code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	published, _ := out["operation"].(map[string]any)["response"].(map[string]any)["aptArtifacts"].([]any)
	if len(published) != 1 {
		t.Fatalf("upload response = %v", out)
	}
	encoded, err := json.Marshal(published[0])
	if err != nil {
		t.Fatal(err)
	}
	artifact := &artifactregistrypb.AptArtifact{}
	if err := protojson.Unmarshal(encoded, artifact); err != nil {
		t.Fatalf("%s is not an AptArtifact: %v", encoded, err)
	}
	if artifact.GetPackageName() != "world" || artifact.GetArchitecture() != "amd64" ||
		artifact.GetPackageType() != artifactregistrypb.AptArtifact_BINARY ||
		string(artifact.GetControlFile()) != strings.ReplaceAll(arTestControl, "hello", "world") {
		t.Fatalf("apt artifact = %v", artifact)
	}
	if code, out := arTestUpload(t, srv, repo, "aptArtifacts", `{}`, []byte("not a package"), "application/octet-stream"); code != http.StatusBadRequest {
		t.Fatalf("a file that is not a package answered %d %v", code, out)
	}
}

func TestARGoModuleAndKfpUploads(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	goRepo := arTestRepository(t, srv, "go-repo", "GO")
	moduleZip := arTestGoModuleZip(t, "example.com/Hello", "v1.0.0", "module example.com/Hello\n")
	code, out := arTestUpload(t, srv, goRepo, "goModules", `{}`, moduleZip, "application/zip")
	if code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	module := &artifactregistrypb.GoModule{}
	arTestOperationResponse(t, out, module)
	if module.GetVersion() != "v1.0.0" {
		t.Fatalf("go module = %v", module)
	}
	if got := arTestDownload(t, srv, goRepo+"/files/example.com%2F%21hello%2F@v%2Fv1.0.0.zip"); !bytes.Equal(got, moduleZip) {
		t.Fatal("the module zip differs from the upload")
	}
	if got := arTestDownload(t, srv, goRepo+"/files/example.com%2F%21hello%2F@v%2Fv1.0.0.mod"); string(got) != "module example.com/Hello\n" {
		t.Fatalf("go.mod = %q", got)
	}
	gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+goRepo+"/packages/example.com%2FHello/versions/v1.0.0", ``)
	if code, out := arTestUpload(t, srv, goRepo, "goModules", `{}`, moduleZip, "application/zip"); code != http.StatusConflict {
		t.Fatalf("a second upload of the module answered %d %v", code, out)
	}

	kfpRepo := arTestRepository(t, srv, "kfp-repo", "KFP")
	template := []byte("pipelineInfo:\n  name: hello-pipeline\nschemaVersion: 2.1.0\n")
	sum := sha256.Sum256(template)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	code, out = arTestUpload(t, srv, kfpRepo, "kfpArtifacts", `{"tags":["v1","latest"],"description":"first"}`, template, "application/x-yaml")
	if code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	artifact := &artifactregistrypb.KfpArtifact{}
	arTestOperationResponse(t, out, artifact)
	version := kfpRepo + "/packages/hello-pipeline/versions/" + digest
	if artifact.GetName() != kfpRepo+"/kfpArtifacts/"+digest || artifact.GetVersion() != version {
		t.Fatalf("kfp artifact = %v", artifact)
	}
	tag := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+kfpRepo+"/packages/hello-pipeline/tags/v1", ``)
	if tag["version"] != version {
		t.Fatalf("tag = %v", tag)
	}
	if v := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+version, ``); v["description"] != "first" {
		t.Fatalf("version = %v", v)
	}
	if got := arTestDownload(t, srv, kfpRepo+"/files/"+digest); !bytes.Equal(got, template) {
		t.Fatalf("template = %q", got)
	}
}

// An import reads the Cloud Storage objects its URIs name and publishes each;
// a URI naming nothing is an error in the response, not a failed operation.
func TestARImportReadsCloudStorage(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "import-repo", "APT")
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=p", `{"name":"debs"}`)
	deb := arTestDeb(t, arTestControl, ".zst")
	if _, err := persistGCSObjectBytes("debs", "pool/hello.deb", deb, GCSObject{ContentType: "application/vnd.debian.binary-package"}, gcsPreconditions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := persistGCSObjectBytes("debs", "pool/notes.txt", []byte("not a package"), GCSObject{}, gcsPreconditions{}); err != nil {
		t.Fatal(err)
	}

	op := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodPost, "/v1/"+repo+"/aptArtifacts:import",
		`{"gcsSource":{"uris":["gs://debs/pool/*","gs://debs/absent.deb"],"useWildcards":true}}`)
	response := &artifactregistrypb.ImportAptArtifactsResponse{}
	grpcReadOperation(t, op["name"].(string), response, &artifactregistrypb.ImportAptArtifactsMetadata{})
	if len(response.GetAptArtifacts()) != 1 || response.GetAptArtifacts()[0].GetPackageName() != "hello" {
		t.Fatalf("imported %v", response.GetAptArtifacts())
	}
	if len(response.GetErrors()) != 2 {
		t.Fatalf("errors = %v", response.GetErrors())
	}
	fileName := repo + "/files/hello_2.10-2_amd64.deb"
	if got := arTestDownload(t, srv, fileName); !bytes.Equal(got, deb) {
		t.Fatal("the imported file differs from the object")
	}
	file := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+fileName, ``)
	if file["owner"] != repo+"/packages/hello/versions/1:2.10-2" {
		t.Fatalf("file = %v", file)
	}
	if code, _ := gcpHostCall(t, srv, "artifactregistry.googleapis.com", http.MethodPost, "/v1/"+repo+"/aptArtifacts:import", `{}`); code != http.StatusBadRequest {
		t.Fatalf("an import naming no source answered %d", code)
	}
}

// exportArtifact resolves the version through the recorded versions: a pushed
// Docker image's manifest version exports its manifest and blobs, and a generic
// version exports the files it owns.
func TestARExportWritesTheVersionsFiles(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "artifactregistry.googleapis.com"
	dockerRepo := arTestRepository(t, srv, "exp-docker", "DOCKER")
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=p", `{"name":"exports"}`)

	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("layer bytes")
	configDigest, layerDigest := digestBytes(config), digestBytes(layer)
	arRegistry.PutBlob("", "p/exp-docker/app", configDigest, "application/octet-stream", config)
	arRegistry.PutBlob("", "p/exp-docker/app", layerDigest, "application/octet-stream", layer)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":%d}]}`,
		configDigest, len(config), layerDigest, len(layer)))
	arRegistry.PutManifest("", "p/exp-docker/app", "v1", "application/vnd.oci.image.manifest.v1+json", manifest)
	manifestDigest := digestBytes(manifest)

	op := gcpHostOK(t, srv, host, http.MethodPost, "/v1/"+dockerRepo+":exportArtifact",
		`{"sourceTag":"`+dockerRepo+`/packages/app/tags/v1","gcsPath":"exports/images"}`)
	response, metadata := &artifactregistrypb.ExportArtifactResponse{}, &artifactregistrypb.ExportArtifactMetadata{}
	grpcReadOperation(t, op["name"].(string), response, metadata)
	if response.GetExportedVersion().GetName() != dockerRepo+"/packages/app/versions/"+manifestDigest {
		t.Fatalf("exported version = %v", response.GetExportedVersion())
	}
	want := map[string][]byte{manifestDigest: manifest, configDigest: config, layerDigest: layer}
	if len(metadata.GetExportedFiles()) != len(want) {
		t.Fatalf("exported files = %v", metadata.GetExportedFiles())
	}
	for _, f := range metadata.GetExportedFiles() {
		digest := strings.TrimPrefix(f.GetName(), dockerRepo+"/files/")
		if f.GetGcsObjectPath() != "exports/images/"+digest {
			t.Fatalf("exported file %v", f)
		}
		got, err := GCSObjectBytes("exports", "images/"+digest)
		if err != nil || !bytes.Equal(got, want[digest]) {
			t.Fatalf("object images/%s = %q, %v", digest, got, err)
		}
	}
	if obj, _ := gcsObjects.Get("exports/images/" + manifestDigest); obj.ContentType != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("manifest object content type %q", obj.ContentType)
	}

	genericRepo := arTestRepository(t, srv, "exp-generic", "GENERIC")
	content := []byte("release notes")
	if code, out := arTestUpload(t, srv, genericRepo, "genericArtifacts", `{"packageId":"docs","versionId":"2.0","filename":"notes.txt"}`, content, "text/plain"); code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	op = gcpHostOK(t, srv, host, http.MethodPost, "/v1/"+genericRepo+":exportArtifact",
		`{"sourceVersion":"`+genericRepo+`/packages/docs/versions/2.0","gcsPath":"exports"}`)
	metadata = &artifactregistrypb.ExportArtifactMetadata{}
	grpcReadOperation(t, op["name"].(string), &artifactregistrypb.ExportArtifactResponse{}, metadata)
	if files := metadata.GetExportedFiles(); len(files) != 1 || files[0].GetGcsObjectPath() != "exports/docs:2.0:notes.txt" ||
		files[0].GetName() != genericRepo+"/files/docs:2.0:notes.txt" {
		t.Fatalf("exported files = %v", files)
	}
	if got, err := GCSObjectBytes("exports", "docs:2.0:notes.txt"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("object = %q, %v", got, err)
	}

	if code, _ := gcpHostCall(t, srv, host, http.MethodPost, "/v1/"+genericRepo+":exportArtifact",
		`{"sourceVersion":"`+genericRepo+`/packages/docs/versions/9.9","gcsPath":"exports"}`); code != http.StatusNotFound {
		t.Fatalf("exporting an absent version answered %d", code)
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodPost, "/v1/"+genericRepo+":exportArtifact",
		`{"sourceVersion":"`+genericRepo+`/packages/docs/versions/2.0","gcsPath":"no-such-bucket"}`); code != http.StatusNotFound {
		t.Fatalf("exporting to an absent bucket answered %d", code)
	}
}

func TestARRepositoryDeleteDeletesItsFiles(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "del-repo", "GENERIC")
	if code, out := arTestUpload(t, srv, repo, "genericArtifacts", `{"packageId":"a","versionId":"1","filename":"f"}`, []byte("x"), "text/plain"); code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodDelete, "/v1/"+repo, ``)
	arTestRepository(t, srv, "del-repo", "GENERIC")
	listed := gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodGet, "/v1/"+repo+"/files", ``)
	if files, _ := listed["files"].([]any); len(files) != 0 {
		t.Fatalf("a recreated repository lists %v", files)
	}
	if _, ok := arFileContents.Get(repo + "/files/a:1:f"); ok {
		t.Fatal("the deleted file's content outlived it")
	}
}
