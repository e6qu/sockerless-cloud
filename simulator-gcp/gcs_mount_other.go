//go:build !linux

package main

import "context"

// Off Linux the host has no inotify to watch a mounted bucket's directory
// with, so a write through a mount stays in the directory and never becomes an
// object; BUGS.md tracks it. The directory still shows every live generation.

func gcsMountAcquire(string) error { return nil }

func gcsMountRelease(string) {}

func gcsMountBarrier(context.Context) error { return nil }
