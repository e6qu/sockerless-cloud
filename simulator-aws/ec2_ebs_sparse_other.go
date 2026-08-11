//go:build !darwin && !linux

package main

import (
	"io/fs"
)

func ebsCopySparseFile(dst, src string, mode fs.FileMode) error {
	return ebsCopySparseFileByContent(dst, src, mode)
}
