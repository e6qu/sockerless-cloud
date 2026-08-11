//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEBSCopyDirPreservesSparseBlockImages(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	dst := filepath.Join(t.TempDir(), "destination")
	require.NoError(t, os.MkdirAll(src, 0o755))

	const imageSize = int64(8 * 1024 * 1024 * 1024)
	imagePath := filepath.Join(src, "ebs.raw")
	image, err := os.Create(imagePath)
	require.NoError(t, err)
	require.NoError(t, image.Truncate(imageSize))
	_, err = image.WriteAt([]byte("first allocated extent"), 0)
	require.NoError(t, err)
	_, err = image.WriteAt([]byte("last allocated extent"), imageSize-21)
	require.NoError(t, err)
	require.NoError(t, image.Close())

	require.NoError(t, ebsCopyDir(dst, src))

	copiedPath := filepath.Join(dst, "ebs.raw")
	copied, err := os.Open(copiedPath)
	require.NoError(t, err)
	defer copied.Close()
	info, err := copied.Stat()
	require.NoError(t, err)
	require.Equal(t, imageSize, info.Size())

	first := make([]byte, len("first allocated extent"))
	_, err = copied.ReadAt(first, 0)
	require.NoError(t, err)
	require.Equal(t, "first allocated extent", string(first))
	last := make([]byte, len("last allocated extent"))
	_, err = copied.ReadAt(last, imageSize-int64(len(last)))
	require.NoError(t, err)
	require.Equal(t, "last allocated extent", string(last))

	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	const maxSparseAllocation int64 = 16 * 1024 * 1024
	require.Less(t, stat.Blocks*512, maxSparseAllocation,
		"copying a sparse EBS block image must not allocate its logical size")
}
