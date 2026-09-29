//go:build !darwin && !linux

package main

import "os"

// statLinkCount reports how many names the filesystem has for a file.
func statLinkCount(os.FileInfo) (int, bool) {
	return 0, false
}
