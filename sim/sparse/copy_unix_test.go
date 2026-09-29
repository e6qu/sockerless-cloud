//go:build darwin || linux

package sparse

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyTreePreservesSparseBlockImages(t *testing.T) {
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

	require.NoError(t, CopyTree(dst, src))

	copied, err := os.Open(filepath.Join(dst, "ebs.raw"))
	require.NoError(t, err)
	defer func() { _ = copied.Close() }()
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
		"copying a sparse block image must not allocate its logical size")
}

func TestDataExtentsFollowWritesAndClears(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "file"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	require.NoError(t, err)
	block, ok := BlockSize(info)
	require.True(t, ok)
	require.NoError(t, f.Truncate(16*block))

	extents, err := DataExtents(f)
	require.NoError(t, err)
	require.Empty(t, extents, "a file nothing has written is one hole")

	_, err = f.WriteAt(make([]byte, 2*block), 4*block)
	require.NoError(t, err)
	extents, err = DataExtents(f)
	require.NoError(t, err)
	require.Equal(t, []Extent{{Start: 4 * block, End: 6*block - 1}}, extents,
		"a write of zeros still allocates its extent")

	// Clearing from the middle of the first block to the end of the second
	// zeroes the partial block and deallocates the whole one.
	_, err = f.WriteAt([]byte{1}, 4*block+10)
	require.NoError(t, err)
	require.NoError(t, Clear(f, 4*block+5, 2*block-5))
	extents, err = DataExtents(f)
	require.NoError(t, err)
	require.Equal(t, []Extent{{Start: 4 * block, End: 5*block - 1}}, extents)
	got := make([]byte, 1)
	_, err = f.ReadAt(got, 4*block+10)
	require.NoError(t, err)
	require.Equal(t, byte(0), got[0], "a cleared byte reads as zero")
}
