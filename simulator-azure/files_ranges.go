package main

import (
	"os"

	"github.com/e6qu/sockerless-cloud/sim/sparse"
)

// filesDataExtents asks the filesystem which extents of a share file hold
// data, which is what List Ranges answers: the space Create File allocates is
// a hole until something writes into it, through Upload Range or from a
// workload with the share mounted.
func filesDataExtents(hostPath string) ([]sparse.Extent, error) {
	f, err := os.Open(hostPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return sparse.DataExtents(f)
}
