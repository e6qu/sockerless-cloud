//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

// statLinkCount reports how many names the filesystem has for a file.
func statLinkCount(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Nlink), true
}
