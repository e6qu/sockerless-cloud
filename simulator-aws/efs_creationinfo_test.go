package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureAccessPointRootDirAppliesCreationInfo verifies an EFS access
// point's root directory is materialised with the RootDirectory.CreationInfo
// permissions — not the umask-reduced MkdirAll mode. A gitlab-runner build
// volume created with CreationInfo{0777} must be world-writable so the
// (non-root / uid-mapped) job container can write to it (BUG-1800).
func TestEnsureAccessPointRootDirAppliesCreationInfo(t *testing.T) {
	base := t.TempDir()

	cases := []struct {
		name string
		rd   *EFSRootDirectory
		want os.FileMode
	}{
		{
			name: "creationinfo-0777",
			rd:   &EFSRootDirectory{CreationInfo: &EFSCreationInfo{OwnerUid: 1000, OwnerGid: 1000, Permissions: "0777"}},
			want: 0o777,
		},
		{
			name: "no-creationinfo-defaults-0777",
			rd:   nil,
			want: 0o777,
		},
		{
			name: "explicit-0750-honored",
			rd:   &EFSRootDirectory{CreationInfo: &EFSCreationInfo{Permissions: "0750"}},
			want: 0o750,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(base, tc.name)
			ensureAccessPointRootDir(root, tc.rd)
			fi, err := os.Stat(root)
			if err != nil {
				t.Fatalf("stat %s: %v", root, err)
			}
			if got := fi.Mode().Perm(); got != tc.want {
				t.Errorf("root dir perms = %#o, want %#o (umask must not reduce the CreationInfo mode)", got, tc.want)
			}
		})
	}
}

// TestEnsureAccessPointRootDirDoesNotClobberExisting verifies CreationInfo is
// applied only on creation — a workload that changes the root dir's perms
// keeps them across subsequent mounts (matching real EFS).
func TestEnsureAccessPointRootDirDoesNotClobberExisting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ap")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("setup chmod: %v", err)
	}
	ensureAccessPointRootDir(root, &EFSRootDirectory{CreationInfo: &EFSCreationInfo{Permissions: "0777"}})
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("existing dir perms = %#o, want 0700 preserved (CreationInfo applies only on creation)", got)
	}
}
