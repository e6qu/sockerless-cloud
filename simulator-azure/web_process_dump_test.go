package main

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestProcessCoreDumpIsAnELFCoreADebuggerOpens writes a core of a real running
// process and reads it back with the standard ELF reader. The point is that the
// bytes are a real image: the segments are that process's real mappings, and a
// string the process is known to hold is in them.
//
// The subject is a child process rather than this test's own, which is both
// what the operation does — a site's workload process is dumped, not the
// simulator — and what keeps the dump bounded: under the race detector this
// process maps shadow regions far larger than a runner's memory, and imaging
// them exhausted it.
func TestProcessCoreDumpIsAnELFCoreADebuggerOpens(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("a process's memory is read from /proc/<pid>, which %s does not have", runtime.GOOS)
	}

	// The marker reaches the child's memory in its environment block, which
	// lives in the stack mapping the core images.
	const marker = "sockerless-core-dump-marker-9f13c2"
	child := exec.Command("/bin/sleep", "30")
	child.Env = append(os.Environ(), "SOCKERLESS_CORE_DUMP_MARKER="+marker)
	if err := child.Start(); err != nil {
		t.Fatalf("start the process to dump: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	pid := child.Process.Pid

	// Wait for the exec to complete: until it does, the mapping table is this
	// shell's rather than the program's.
	var mappings []webProcMapping
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		read, ok := webProcessMappings(pid)
		if ok && len(read) > 0 {
			mappings = read
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(mappings) == 0 {
		t.Fatal("the process's mapping table could not be read")
	}

	var core bytes.Buffer
	if err := webProcessCoreDump(pid, mappings, &core); err != nil {
		t.Fatalf("write a core of the process: %v", err)
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

	// The contents are the process's own memory, not zeroes.
	found := false
	for _, p := range file.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		body := make([]byte, p.Filesz)
		if _, err := p.ReadAt(body, 0); err != nil {
			continue
		}
		if bytes.Contains(body, []byte(marker)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the core does not contain a string the process holds in memory, so it is not an image of it")
	}
}
