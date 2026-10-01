package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"go.yaml.in/yaml/v3"
)

// arPackageArtifact is what an Apt, Yum or GooGet artifact says about itself:
// the package, version and architecture its own metadata declares, and the
// file name its format gives it.
type arPackageArtifact struct {
	PackageName  string
	Version      string
	Architecture string
	FileName     string
	PackageType  string
	ControlFile  []byte
}

// arParseDeb reads a Debian binary package: an ar archive whose first member
// is debian-binary and whose control.tar member holds the control file
// (deb(5)). The file name is the one dpkg-name gives the package,
// package_version_architecture.deb with the epoch dropped.
func arParseDeb(data []byte) (arPackageArtifact, error) {
	members, err := arReadArArchive(data)
	if err != nil {
		return arPackageArtifact{}, err
	}
	if len(members) == 0 || members[0].name != "debian-binary" {
		return arPackageArtifact{}, errors.New("the archive is not a Debian package: its first member is not debian-binary")
	}
	if !strings.HasPrefix(string(members[0].data), "2.") {
		return arPackageArtifact{}, fmt.Errorf("unsupported Debian package format %q", strings.TrimSpace(string(members[0].data)))
	}
	var control []byte
	for _, m := range members[1:] {
		if !strings.HasPrefix(m.name, "control.tar") {
			continue
		}
		tarball, err := arDecompress(strings.TrimPrefix(m.name, "control.tar"), m.data)
		if err != nil {
			return arPackageArtifact{}, fmt.Errorf("read %s: %w", m.name, err)
		}
		if control, err = arTarMember(tarball, func(name string) bool {
			return path.Clean(strings.TrimPrefix(name, "./")) == "control"
		}); err != nil {
			return arPackageArtifact{}, fmt.Errorf("read %s: %w", m.name, err)
		}
		break
	}
	if control == nil {
		return arPackageArtifact{}, errors.New("the Debian package has no control file")
	}
	fields := arParseControl(control)
	for _, field := range []string{"Package", "Version", "Architecture"} {
		if fields[field] == "" {
			return arPackageArtifact{}, fmt.Errorf("the Debian control file has no %s field", field)
		}
	}
	upstream := fields["Version"]
	if _, rest, found := strings.Cut(upstream, ":"); found {
		upstream = rest
	}
	return arPackageArtifact{
		PackageName:  fields["Package"],
		Version:      fields["Version"],
		Architecture: fields["Architecture"],
		FileName:     fields["Package"] + "_" + upstream + "_" + fields["Architecture"] + ".deb",
		PackageType:  "BINARY",
		ControlFile:  control,
	}, nil
}

type arArMember struct {
	name string
	data []byte
}

// arReadArArchive reads the common ar format: the global header, then per
// member a 60-byte header (name, mtime, uid, gid, mode, size, "`\n") and the
// member's bytes padded to an even length.
func arReadArArchive(data []byte) ([]arArMember, error) {
	const magic = "!<arch>\n"
	if !bytes.HasPrefix(data, []byte(magic)) {
		return nil, errors.New("the file is not an ar archive")
	}
	var members []arArMember
	for offset := len(magic); offset < len(data); {
		if len(data)-offset < 60 {
			return nil, errors.New("the ar archive ends inside a member header")
		}
		header := data[offset : offset+60]
		if string(header[58:60]) != "`\n" {
			return nil, errors.New("an ar member header is malformed")
		}
		size, err := strconv.Atoi(strings.TrimSpace(string(header[48:58])))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("an ar member size %q is malformed", strings.TrimSpace(string(header[48:58])))
		}
		offset += 60
		if len(data)-offset < size {
			return nil, errors.New("the ar archive ends inside a member")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[0:16])), "/")
		members = append(members, arArMember{name: name, data: data[offset : offset+size]})
		offset += size + size%2
	}
	return members, nil
}

// arDecompress undoes the compression a control.tar member's suffix names;
// dpkg-deb writes it uncompressed, or with gzip, xz or zstd.
func arDecompress(suffix string, data []byte) ([]byte, error) {
	var reader io.Reader
	switch suffix {
	case "":
		return data, nil
	case ".gz":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		reader = zr
	case ".xz":
		xr, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		reader = xr
	case ".zst":
		zr, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		reader = zr
	default:
		return nil, fmt.Errorf("unknown compression %q", suffix)
	}
	return io.ReadAll(reader)
}

// arTarMember returns the contents of the first regular file in a tar archive
// whose name match accepts.
func arTarMember(tarball []byte, match func(string) bool) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(tarball))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && match(header.Name) {
			return io.ReadAll(tr)
		}
	}
}

// arParseControl reads the fields of a Debian control paragraph; a line that
// starts with a space or tab continues the field before it.
func arParseControl(control []byte) map[string]string {
	fields := map[string]string{}
	last := ""
	for _, line := range strings.Split(string(control), "\n") {
		if line == "" {
			if len(fields) > 0 {
				break
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if last != "" {
				fields[last] += "\n" + line
			}
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		last = strings.TrimSpace(key)
		fields[last] = strings.TrimSpace(value)
	}
	return fields
}

const (
	rpmTagName    = 1000
	rpmTagVersion = 1001
	rpmTagRelease = 1002
	rpmTagEpoch   = 1003
	rpmTagArch    = 1022
)

// arParseRPM reads an RPM package: the 96-byte lead, whose type field tells a
// binary package (0) from a source package (1), the signature header padded to
// eight bytes, and the header holding the package's tags. The file name is the
// one rpmbuild gives the package, name-version-release.arch.rpm, with src as
// the architecture of a source package.
func arParseRPM(data []byte) (arPackageArtifact, error) {
	if len(data) < 96 || !bytes.Equal(data[:4], []byte{0xed, 0xab, 0xee, 0xdb}) {
		return arPackageArtifact{}, errors.New("the file is not an RPM package")
	}
	packageType := "BINARY"
	switch binary.BigEndian.Uint16(data[6:8]) {
	case 0:
	case 1:
		packageType = "SOURCE"
	default:
		return arPackageArtifact{}, errors.New("the RPM lead names neither a binary nor a source package")
	}
	_, next, err := arRPMHeader(data, 96)
	if err != nil {
		return arPackageArtifact{}, fmt.Errorf("read the RPM signature: %w", err)
	}
	if rem := next % 8; rem != 0 {
		next += 8 - rem
	}
	tags, _, err := arRPMHeader(data, next)
	if err != nil {
		return arPackageArtifact{}, fmt.Errorf("read the RPM header: %w", err)
	}
	for _, tag := range []int{rpmTagName, rpmTagVersion, rpmTagRelease, rpmTagArch} {
		if tags[tag] == "" {
			return arPackageArtifact{}, fmt.Errorf("the RPM header has no tag %d", tag)
		}
	}
	version := tags[rpmTagVersion] + "-" + tags[rpmTagRelease]
	if epoch := tags[rpmTagEpoch]; epoch != "" {
		version = epoch + ":" + version
	}
	arch := tags[rpmTagArch]
	fileArch := arch
	if packageType == "SOURCE" {
		fileArch = "src"
	}
	return arPackageArtifact{
		PackageName:  tags[rpmTagName],
		Version:      version,
		Architecture: arch,
		FileName:     tags[rpmTagName] + "-" + tags[rpmTagVersion] + "-" + tags[rpmTagRelease] + "." + fileArch + ".rpm",
		PackageType:  packageType,
	}, nil
}

// arRPMHeader reads the header structure at offset: magic 8e ad e8 01, four
// reserved bytes, the index entry count and the data store size, then the
// 16-byte index entries (tag, type, offset, count) and the store. It returns
// the string and INT32 tags this parser reads and the offset past the store.
func arRPMHeader(data []byte, offset int) (map[int]string, int, error) {
	if len(data)-offset < 16 || !bytes.Equal(data[offset:offset+4], []byte{0x8e, 0xad, 0xe8, 0x01}) {
		return nil, 0, errors.New("no header structure where one belongs")
	}
	entries := int(binary.BigEndian.Uint32(data[offset+8 : offset+12]))
	storeSize := int(binary.BigEndian.Uint32(data[offset+12 : offset+16]))
	indexStart := offset + 16
	storeStart := indexStart + 16*entries
	end := storeStart + storeSize
	if entries < 0 || storeSize < 0 || end > len(data) || end < storeStart {
		return nil, 0, errors.New("the header runs past the end of the file")
	}
	store := data[storeStart:end]
	tags := map[int]string{}
	for i := 0; i < entries; i++ {
		entry := data[indexStart+16*i : indexStart+16*(i+1)]
		tag := int(binary.BigEndian.Uint32(entry[0:4]))
		kind := binary.BigEndian.Uint32(entry[4:8])
		at := int(binary.BigEndian.Uint32(entry[8:12]))
		if at < 0 || at >= len(store) {
			continue
		}
		switch kind {
		case 4: // INT32
			if at+4 <= len(store) {
				tags[tag] = strconv.FormatUint(uint64(binary.BigEndian.Uint32(store[at:at+4])), 10)
			}
		case 6, 8, 9: // STRING, STRING_ARRAY, I18NSTRING: the first string.
			value, _, _ := bytes.Cut(store[at:], []byte{0})
			tags[tag] = string(value)
		}
	}
	return tags, end, nil
}

// arParseGoo reads a GooGet package: a gzip-compressed tar archive holding a
// .pkgspec file, the JSON package specification whose Name, Version and Arch
// identify the package. The file name is the one goopack gives the package,
// name.arch.version.goo.
func arParseGoo(data []byte) (arPackageArtifact, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return arPackageArtifact{}, fmt.Errorf("the file is not a GooGet package: %w", err)
	}
	tarball, err := io.ReadAll(zr)
	if err != nil {
		return arPackageArtifact{}, fmt.Errorf("read the GooGet package: %w", err)
	}
	specData, err := arTarMember(tarball, func(name string) bool { return path.Ext(name) == ".pkgspec" })
	if err != nil {
		return arPackageArtifact{}, fmt.Errorf("read the GooGet package: %w", err)
	}
	if specData == nil {
		return arPackageArtifact{}, errors.New("the GooGet package has no .pkgspec file")
	}
	var spec struct {
		Name, Version, Arch string
	}
	if err := json.Unmarshal(specData, &spec); err != nil {
		return arPackageArtifact{}, fmt.Errorf("read the GooGet package specification: %w", err)
	}
	for field, value := range map[string]string{"Name": spec.Name, "Version": spec.Version, "Arch": spec.Arch} {
		if value == "" {
			return arPackageArtifact{}, fmt.Errorf("the GooGet package specification has no %s", field)
		}
	}
	return arPackageArtifact{
		PackageName:  spec.Name,
		Version:      spec.Version,
		Architecture: spec.Arch,
		FileName:     spec.Name + "." + spec.Arch + "." + spec.Version + ".goo",
	}, nil
}

// arGoModule is a Go module zip file (go.dev/ref/mod#zip-files): every file
// sits under module@version/, and go.mod is the module's own, or the one the
// go command synthesizes for a module without one.
type arGoModule struct {
	Path, Version string
	GoMod         []byte
}

var arGoCanonicalVersion = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+incompatible)?$`)

func arParseGoModuleZip(data []byte) (arGoModule, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return arGoModule{}, fmt.Errorf("the file is not a Go module zip: %w", err)
	}
	if len(zr.File) == 0 {
		return arGoModule{}, errors.New("the Go module zip is empty")
	}
	first := zr.File[0].Name
	at := strings.Index(first, "@")
	slash := strings.Index(first[max(at, 0):], "/")
	if at <= 0 || slash < 0 {
		return arGoModule{}, fmt.Errorf("zip entry %q is not under a module@version/ directory", first)
	}
	prefix := first[:at+slash+1]
	module := arGoModule{Path: first[:at], Version: first[at+1 : at+slash]}
	if !arGoCanonicalVersion.MatchString(module.Version) {
		return arGoModule{}, fmt.Errorf("%q is not a canonical Go module version", module.Version)
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			return arGoModule{}, fmt.Errorf("zip entry %q is outside %s", f.Name, prefix)
		}
		if f.Name != prefix+"go.mod" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return arGoModule{}, fmt.Errorf("read go.mod: %w", err)
		}
		module.GoMod, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return arGoModule{}, fmt.Errorf("read go.mod: %w", err)
		}
	}
	if module.GoMod == nil {
		module.GoMod = []byte("module " + module.Path + "\n")
	}
	declared := ""
	for _, line := range strings.Split(string(module.GoMod), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "module" {
			declared = strings.Trim(fields[1], `"`)
			break
		}
	}
	if declared != module.Path {
		return arGoModule{}, fmt.Errorf("go.mod declares module %q, but the zip holds %q", declared, module.Path)
	}
	return module, nil
}

// arGoEscapePath applies the module proxy protocol's case encoding: each
// upper-case letter becomes "!" and the letter in lower case.
func arGoEscapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// arKfpPipeline is a Kubeflow Pipelines template: the pipeline name its
// pipelineInfo declares, and the sha256 digest its version is named by.
type arKfpPipeline struct {
	Name, Digest string
}

func arParseKfp(data []byte) (arKfpPipeline, error) {
	var spec struct {
		PipelineInfo struct {
			Name string `yaml:"name"`
		} `yaml:"pipelineInfo"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return arKfpPipeline{}, fmt.Errorf("the file is not a Kubeflow Pipelines template: %w", err)
	}
	if spec.PipelineInfo.Name == "" {
		return arKfpPipeline{}, errors.New("the Kubeflow Pipelines template has no pipelineInfo.name")
	}
	sum := sha256.Sum256(data)
	return arKfpPipeline{Name: spec.PipelineInfo.Name, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}
