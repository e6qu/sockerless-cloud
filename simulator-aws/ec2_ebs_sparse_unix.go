//go:build darwin || linux

package main

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func ebsCopySparseFile(dst, src string, mode fs.FileMode) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	inClosed := false
	defer func() {
		if !inClosed {
			if err := in.Close(); retErr == nil {
				retErr = err
			}
		}
	}()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	outClosed := false
	defer func() {
		if !outClosed {
			if err := out.Close(); retErr == nil {
				retErr = err
			}
		}
	}()
	if err := out.Truncate(info.Size()); err != nil {
		return err
	}

	for offset := int64(0); offset < info.Size(); {
		dataOffset, err := unix.Seek(int(in.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			return nil
		}
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
			if err := out.Close(); err != nil {
				return err
			}
			outClosed = true
			if err := in.Close(); err != nil {
				return err
			}
			inClosed = true
			return ebsCopySparseFileByContent(dst, src, mode)
		}
		if err != nil {
			return err
		}
		holeOffset, err := unix.Seek(int(in.Fd()), dataOffset, unix.SEEK_HOLE)
		if errors.Is(err, unix.ENXIO) {
			holeOffset = info.Size()
		} else if err != nil {
			return err
		}
		if holeOffset > info.Size() {
			holeOffset = info.Size()
		}
		if _, err := in.Seek(dataOffset, io.SeekStart); err != nil {
			return err
		}
		if _, err := out.Seek(dataOffset, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(out, in, holeOffset-dataOffset); err != nil {
			return err
		}
		offset = holeOffset
	}
	return nil
}
