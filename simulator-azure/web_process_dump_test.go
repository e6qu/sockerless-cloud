package main

import (
	"bytes"
	"debug/elf"
	"os"
	"runtime"
	"testing"
)

// TestProcessCoreDumpIsAnELFCoreADebuggerOpens writes a core of this test's own
// process and reads it back with the standard ELF reader. The point is that the
// bytes are a real image: the segments are this process's real mappings, and
// the contents at a known address are the bytes that are actually there.
func TestProcessCoreDumpIsAnELFCoreADebuggerOpens(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("a process's memory is read from /proc/<pid>, which %s does not have", runtime.GOOS)
	}
	pid := os.Getpid()
	mappings, ok := webProcessMappings(pid)
	if !ok {
		t.Fatal("this process's own mapping table could not be read")
	}

	var core bytes.Buffer
	if err := webProcessCoreDump(pid, mappings, &core); err != nil {
		t.Fatalf("write a core of this process: %v", err)
	}

	file, err := elf.NewFile(bytes.NewReader(core.Bytes()))
	if err != nil {
		t.Fatalf("the core is not an ELF file a reader opens: %v", err)
	}
	if file.Type != elf.ET_CORE {
		t.Fatalf("core type = %v, want ET_CORE", file.Type)
	}
	if file.Class != elf.ELFCLASS64 {
		t.Fatalf("core class = %v, want ELFCLASS64", file.Class)
	}

	loads := 0
	for _, p := range file.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		loads++
		if p.Filesz != p.Memsz {
			t.Fatalf("segment at %#x has filesz %d and memsz %d; a segment read out of the process carries every byte it spans",
				p.Vaddr, p.Filesz, p.Memsz)
		}
	}
	if loads == 0 {
		t.Fatal("the core carries no PT_LOAD segment, so it images nothing")
	}

	// The contents are this process's own memory, not zeroes: a known string in
	// this binary must be findable at the address the mapping table gives it.
	marker := []byte("sockerless-core-dump-marker-9f13c2")
	found := false
	for _, p := range file.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		body := make([]byte, p.Filesz)
		if _, err := p.ReadAt(body, 0); err != nil {
			continue
		}
		if bytes.Contains(body, marker) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the core does not contain a string this process holds in memory, so it is not an image of it")
	}
}

// coreDumpMarker keeps the marker the test looks for in this binary's memory.
var coreDumpMarker = "sockerless-core-dump-marker-9f13c2"

func init() { _ = coreDumpMarker }
