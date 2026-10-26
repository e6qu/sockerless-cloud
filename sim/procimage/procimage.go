// Package procimage reads a process's memory image out of its /proc entry: its
// mapping table, the modules that table names, and an ELF core a debugger
// opens. It runs where the process is visible with permission to trace it,
// which for a workload is a platform helper inside the workload's pid
// namespace, never the simulator's own process on the host.
package procimage

import (
	"bufio"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Mapping is one line of a process's mapping table.
type Mapping struct {
	Low, High uint64
	Readable  bool
	Path      string
}

// ParseMappings reads a mapping table in the format of /proc/<pid>/maps.
func ParseMappings(r io.Reader) ([]Mapping, error) {
	var out []Mapping
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		lowText, highText, ok := strings.Cut(fields[0], "-")
		if !ok {
			continue
		}
		low, lowErr := strconv.ParseUint(lowText, 16, 64)
		high, highErr := strconv.ParseUint(highText, 16, 64)
		if lowErr != nil || highErr != nil || high <= low {
			continue
		}
		path := ""
		if len(fields) >= 6 {
			path = strings.Join(fields[5:], " ")
		}
		out = append(out, Mapping{Low: low, High: high, Readable: strings.HasPrefix(fields[1], "r"), Path: path})
	}
	return out, scanner.Err()
}

// ReadMappings reads the mapping table of a process this process may trace.
func ReadMappings(pid int) ([]Mapping, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseMappings(f)
}

// Module is a file a process mapped, spanning from its lowest mapping to its
// highest.
type Module struct {
	Path        string
	BaseAddress uint64
	Size        uint64
}

// Modules folds a mapping table into the files it maps, lowest address first,
// which is the order the table itself is in. An anonymous mapping, the stack,
// the heap and the kernel's own mappings are not modules.
func Modules(mappings []Mapping) []Module {
	type extent struct{ low, high uint64 }
	byPath := map[string]extent{}
	for _, m := range mappings {
		if !strings.HasPrefix(m.Path, "/") {
			continue
		}
		current, seen := byPath[m.Path]
		if !seen {
			byPath[m.Path] = extent{low: m.Low, high: m.High}
			continue
		}
		current.low = min(current.low, m.Low)
		current.high = max(current.high, m.High)
		byPath[m.Path] = current
	}
	modules := make([]Module, 0, len(byPath))
	for path, span := range byPath {
		modules = append(modules, Module{Path: path, BaseAddress: span.low, Size: span.high - span.low})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].BaseAddress < modules[j].BaseAddress })
	return modules
}

// WriteCore writes an ELF core of a process to w: one PT_LOAD segment per run
// of readable pages, read out of mem, which is the process's /proc/<pid>/mem.
// It leaves out a mapping it cannot read rather than zero-filling it: a
// segment of zeroes would claim the process holds zeroes there. The core
// carries no register set, which PTRACE_GETREGSET reads only from a stopped
// process; a debugger opens it, reads memory, and says so about the threads.
func WriteCore(w io.Writer, mem io.ReaderAt, mappings []Mapping, pageSize int, machine elf.Machine) error {
	type segment struct {
		low    uint64
		data   []byte
		offset uint64
	}
	var segments []segment
	page := uint64(pageSize)
	buf := make([]byte, page)
	for _, m := range mappings {
		if !m.Readable || m.Path == "[vvar]" || m.Path == "[vsyscall]" {
			continue
		}
		var run *segment
		for addr := m.Low; addr < m.High; addr += page {
			n := min(page, m.High-addr)
			if _, err := mem.ReadAt(buf[:n], int64(addr)); err != nil {
				run = nil
				continue
			}
			if run == nil {
				segments = append(segments, segment{low: addr})
				run = &segments[len(segments)-1]
			}
			run.data = append(run.data, buf[:n]...)
		}
	}
	if len(segments) == 0 {
		return errors.New("no mapping of the process could be read")
	}

	const headerSize = 64
	const programHeaderSize = 56
	offset := uint64(headerSize + programHeaderSize*len(segments))
	for i := range segments {
		segments[i].offset = offset
		offset += uint64(len(segments[i].data))
	}
	header := make([]byte, headerSize)
	copy(header, []byte{0x7f, 'E', 'L', 'F'})
	header[4] = byte(elf.ELFCLASS64)
	header[5] = byte(elf.ELFDATA2LSB)
	header[6] = byte(elf.EV_CURRENT)
	header[7] = byte(elf.ELFOSABI_LINUX)
	order := binary.LittleEndian
	order.PutUint16(header[16:], uint16(elf.ET_CORE))
	order.PutUint16(header[18:], uint16(machine))
	order.PutUint32(header[20:], uint32(elf.EV_CURRENT))
	order.PutUint64(header[32:], headerSize)        // e_phoff
	order.PutUint16(header[52:], headerSize)        // e_ehsize
	order.PutUint16(header[54:], programHeaderSize) // e_phentsize
	order.PutUint16(header[56:], uint16(len(segments)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	for _, s := range segments {
		size := uint64(len(s.data))
		ph := make([]byte, programHeaderSize)
		order.PutUint32(ph[0:], uint32(elf.PT_LOAD))
		order.PutUint32(ph[4:], uint32(elf.PF_R))
		order.PutUint64(ph[8:], s.offset)
		order.PutUint64(ph[16:], s.low) // p_vaddr
		order.PutUint64(ph[32:], size)  // p_filesz
		order.PutUint64(ph[40:], size)  // p_memsz
		order.PutUint64(ph[48:], 1)     // p_align
		if _, err := w.Write(ph); err != nil {
			return err
		}
	}
	for _, s := range segments {
		if _, err := w.Write(s.data); err != nil {
			return err
		}
	}
	return nil
}

// Machine is the ELF machine of the processes this program runs beside.
func Machine() elf.Machine {
	if runtime.GOARCH == "arm64" {
		return elf.EM_AARCH64
	}
	return elf.EM_X86_64
}

// DumpProcess writes an ELF core of a process this process may trace.
func DumpProcess(w io.Writer, pid int) error {
	mappings, err := ReadMappings(pid)
	if err != nil {
		return fmt.Errorf("read the process's mapping table: %w", err)
	}
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
	if err != nil {
		return fmt.Errorf("open the process's memory: %w", err)
	}
	defer mem.Close()
	return WriteCore(w, mem, mappings, os.Getpagesize(), Machine())
}
