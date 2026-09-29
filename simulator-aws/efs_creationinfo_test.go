package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// efsTestCreationInfo is CreationInfo owned by the test process, which can
// always chown to itself whether or not it runs as root.
func efsTestCreationInfo(permissions string) *EFSCreationInfo {
	return &EFSCreationInfo{OwnerUid: int64(os.Getuid()), OwnerGid: int64(os.Getgid()), Permissions: permissions}
}

// The root directory takes the CreationInfo mode exactly, not the
// umask-reduced one MkdirAll would leave, and the CreationInfo owner.
func TestEnsureAccessPointRootDirAppliesCreationInfo(t *testing.T) {
	base := t.TempDir()
	for name, c := range map[string]struct {
		permissions string
		want        os.FileMode
	}{
		"world-writable": {"0777", 0o777},
		"three-digit":    {"750", 0o750},
		"sticky":         {"1777", 0o777 | os.ModeSticky},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(base, name, "nested")
			if err := ensureAccessPointRootDir(root, &EFSRootDirectory{Path: "/" + name + "/nested", CreationInfo: efsTestCreationInfo(c.permissions)}); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(root)
			if err != nil {
				t.Fatalf("stat %s: %v", root, err)
			}
			if got := fi.Mode() & (os.ModePerm | os.ModeSticky | os.ModeSetgid | os.ModeSetuid); got != c.want {
				t.Errorf("root dir mode = %v, want %v", got, c.want)
			}
			stat, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatalf("no ownership in %T", fi.Sys())
			}
			if int(stat.Uid) != os.Getuid() || int(stat.Gid) != os.Getgid() {
				t.Errorf("root dir owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, os.Getuid(), os.Getgid())
			}
		})
	}
}

// Amazon EFS creates an access point's missing root directory only from
// CreationInfo; without it a mount of the missing path fails.
func TestEnsureAccessPointRootDirWithoutCreationInfoFailsTheMount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	for _, rd := range []*EFSRootDirectory{nil, {Path: "/missing"}} {
		err := ensureAccessPointRootDir(root, rd)
		want := "root directory " + efsRootDirectoryPath(rd) + " does not exist and the access point has no CreationInfo to create it"
		if err == nil || err.Error() != want {
			t.Fatalf("RootDirectory %+v: err = %v, want %q", rd, err, want)
		}
		if _, statErr := os.Stat(root); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("RootDirectory %+v: the failed mount created %s (%v)", rd, root, statErr)
		}
	}
}

// A failure to look up, create or chown the directory fails the mount rather than
// leaving a directory with the wrong owner or mode.
func TestEnsureAccessPointRootDirSurfacesCreationFailures(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureAccessPointRootDir(filepath.Join(parent, "ap"), &EFSRootDirectory{CreationInfo: efsTestCreationInfo("0755")})
	if err == nil || !strings.HasPrefix(err.Error(), "root directory: ") || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("err = %v, want the lookup failure (ENOTDIR)", err)
	}
	if os.Getuid() == 0 {
		return // root may chown to anyone; the refused chown needs an unprivileged process
	}
	root := filepath.Join(t.TempDir(), "foreign")
	err = ensureAccessPointRootDir(root, &EFSRootDirectory{CreationInfo: &EFSCreationInfo{OwnerUid: int64(os.Getuid()) + 1, OwnerGid: int64(os.Getgid()), Permissions: "0755"}})
	if err == nil || !errors.Is(err, syscall.EPERM) {
		t.Fatalf("chown to another user: err = %v, want EPERM", err)
	}
}

// CreationInfo applies only on creation: a workload that changes the root
// directory's mode keeps it across later mounts.
func TestEnsureAccessPointRootDirDoesNotClobberExisting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ap")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("setup chmod: %v", err)
	}
	if err := ensureAccessPointRootDir(root, &EFSRootDirectory{CreationInfo: efsTestCreationInfo("0777")}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("existing dir perms = %#o, want 0700 preserved (CreationInfo applies only on creation)", got)
	}
}

// An Amazon ECS task mounting an access point whose root directory cannot be
// created fails to start with a ResourceInitializationError, never falling
// back to the file system's root.
func TestECSEFSVolumeHostFailsWhereEFSRefusesTheMount(t *testing.T) {
	t.Setenv("SIM_EFS_DATA_DIR", t.TempDir())
	efsFileSystems = sim.MakeStore[EFSFileSystem](nil, "efs_file_systems")
	efsAccessPoints = sim.MakeStore[EFSAccessPoint](nil, "efs_access_points")
	efsFileSystems.Put("fs-mount0001", EFSFileSystem{FileSystemId: "fs-mount0001", LifeCycleState: "available"})
	efsAccessPoints.Put("fsap-bare0001", EFSAccessPoint{AccessPointId: "fsap-bare0001", FileSystemId: "fs-mount0001", RootDirectory: &EFSRootDirectory{Path: "/builds"}})
	efsAccessPoints.Put("fsap-made0001", EFSAccessPoint{AccessPointId: "fsap-made0001", FileSystemId: "fs-mount0001", RootDirectory: &EFSRootDirectory{Path: "/cache", CreationInfo: efsTestCreationInfo("0770")}})

	for _, c := range []struct {
		name string
		cfg  ECSEfsVolumeConfig
		want string
	}{
		{"access point without CreationInfo", ECSEfsVolumeConfig{FileSystemId: "fs-mount0001", AuthorizationConfig: &ECSEfsAuthorizationConfig{AccessPointId: "fsap-bare0001"}},
			"access point fsap-bare0001: root directory /builds does not exist and the access point has no CreationInfo to create it"},
		{"unknown access point", ECSEfsVolumeConfig{FileSystemId: "fs-mount0001", AuthorizationConfig: &ECSEfsAuthorizationConfig{AccessPointId: "fsap-gone0001"}},
			"access point fsap-gone0001 does not exist"},
		{"missing rootDirectory", ECSEfsVolumeConfig{FileSystemId: "fs-mount0001", RootDirectory: "/absent"},
			"root directory /absent of file system fs-mount0001: "},
		{"unknown file system", ECSEfsVolumeConfig{FileSystemId: "fs-gone0001"}, "file system fs-gone0001 does not exist"},
	} {
		host, err := ecsEFSVolumeHost(&c.cfg)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || host != "" {
			t.Errorf("%s: host %q err %v, want no host and %q", c.name, host, err, c.want)
		}
	}

	host, err := ecsEFSVolumeHost(&ECSEfsVolumeConfig{FileSystemId: "fs-mount0001", AuthorizationConfig: &ECSEfsAuthorizationConfig{AccessPointId: "fsap-made0001"}})
	if err != nil {
		t.Fatal(err)
	}
	if fi, statErr := os.Stat(host); statErr != nil || fi.Mode().Perm() != 0o770 || filepath.Base(host) != "cache" {
		t.Fatalf("access point with CreationInfo: host %s (%v, %v), want a 0770 cache directory", host, fi, statErr)
	}

	startErr := &ecsResourceInitializationError{err: fmt.Errorf("failed to invoke EFS utils commands to set up EFS volumes: volume %q: %w", "builds", errors.New("x"))}
	if got := startErr.Error(); got != `ResourceInitializationError: failed to invoke EFS utils commands to set up EFS volumes: volume "builds": x` {
		t.Fatalf("stopped reason %q", got)
	}
}

func TestEFSCreateAccessPointRefusesUnparsablePermissions(t *testing.T) {
	efsFileSystems = sim.MakeStore[EFSFileSystem](nil, "efs_file_systems")
	efsAccessPoints = sim.MakeStore[EFSAccessPoint](nil, "efs_access_points")
	efsFileSystems.Put("fs-perm0001", EFSFileSystem{FileSystemId: "fs-perm0001", LifeCycleState: "available"})
	for _, permissions := range []string{"", "rwx", "0778", "77", "07777", "0o755"} {
		body := `{"FileSystemId":"fs-perm0001","RootDirectory":{"Path":"/p","CreationInfo":{"OwnerUid":1,"OwnerGid":1,"Permissions":"` + permissions + `"}}}`
		rec := httptest.NewRecorder()
		handleEFSCreateAccessPoint(rec, httptest.NewRequest(http.MethodPost, "/2015-02-01/access-points", strings.NewReader(body)))
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%q: %v", permissions, err)
		}
		want := "Value '" + permissions + "' at 'rootDirectory.creationInfo.permissions' failed to satisfy constraint: Member must satisfy regular expression pattern: ^[0-7]{3,4}$"
		if rec.Code != http.StatusBadRequest || got["__type"] != "BadRequest" || got["ErrorCode"] != "BadRequest" ||
			rec.Header().Get("X-Amzn-ErrorType") != "BadRequest" || !strings.Contains(got["Message"], want) {
			t.Errorf("Permissions %q: %d %v %v, want 400 BadRequest (type, header and ErrorCode) carrying %q", permissions, rec.Code, rec.Header(), got, want)
		}
		if _, lower := got["message"]; lower {
			t.Errorf("Permissions %q: the message is also under %q, which EFS does not model: %v", permissions, "message", got)
		}
	}
	if stored := efsAccessPoints.List(); len(stored) != 0 {
		t.Fatalf("a refused CreateAccessPoint stored %v", stored)
	}
	for permissions, want := range map[string]os.FileMode{
		"755":  0o755,
		"0750": 0o750,
		"1777": 0o777 | os.ModeSticky,
		"2750": 0o750 | os.ModeSetgid,
		"4755": 0o755 | os.ModeSetuid,
	} {
		if mode, err := efsCreationInfoMode(permissions); err != nil || mode != want {
			t.Errorf("efsCreationInfoMode(%q) = %v, %v; want %v", permissions, mode, err, want)
		}
	}
}
