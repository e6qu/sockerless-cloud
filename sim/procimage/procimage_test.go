package procimage

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// A core of a real running process is an ELF core the standard reader opens,
// whose segments are that process's real mappings and hold a string the
// process is known to hold. The subject is a child rather than this process:
// under the race detector this process maps shadow regions far larger than a
// runner's memory.
func TestDumpProcessWritesAnELFCoreADebuggerOpens(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("a process's memory is read from /proc/<pid>, which %s does not have", runtime.GOOS)
	}
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

	mappings, err := ReadMappings(pid)
	if err != nil || len(mappings) == 0 {
		t.Fatalf("the process's mapping table could not be read: %v", err)
	}
	modules := Modules(mappings)
	if len(modules) == 0 || !strings.HasPrefix(modules[0].Path, "/") {
		t.Fatalf("the process maps no module, want at least its program: %+v", modules)
	}

	var core bytes.Buffer
	if err := DumpProcess(&core, pid); err != nil {
		t.Fatalf("write a core of the process: %v", err)
	}
	file, err := elf.NewFile(bytes.NewReader(core.Bytes()))
	if err != nil {
		t.Fatalf("the core is not an ELF file a reader opens: %v", err)
	}
	if file.Type != elf.ET_CORE || file.Class != elf.ELFCLASS64 || file.Machine != Machine() {
		t.Fatalf("core is %v %v %v, want ET_CORE ELFCLASS64 %v", file.Type, file.Class, file.Machine, Machine())
	}
	loads := 0
	for _, p := range file.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		loads++
		if p.Filesz != p.Memsz || p.Filesz == 0 {
			t.Fatalf("segment at %#x has filesz %d and memsz %d", p.Vaddr, p.Filesz, p.Memsz)
		}
	}
	if loads == 0 {
		t.Fatal("the core carries no loadable segment")
	}
	if !bytes.Contains(core.Bytes(), []byte(marker)) {
		t.Fatal("the core does not hold the marker the process's environment holds")
	}
}
