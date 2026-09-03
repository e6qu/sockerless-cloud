package main

import (
	"bufio"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// A process dump: the memory image of one of the site's processes, written
// from the process itself.
//
// Azure returns this operation's body as an opaque file — what the caller does
// with it is open it in a debugger — so the only faithful answer is a real
// image of a real process, and the simulator has one: the site's workload runs
// as a container, the engine's process table reports its processes in the
// engine host's PID namespace, and where the simulator shares that kernel
// /proc/<pid> is the process's own memory.
//
// The image is an ELF core: the format a debugger opens, with one PT_LOAD
// segment per readable mapping carrying the bytes read out of /proc/<pid>/mem.
// It is written without stopping the process — reading /proc/<pid>/mem needs
// permission to trace, not an attach — so dumping a site does not interrupt it,
// which is what makes taking one safe on a running site.
//
// What it does not carry is the register set. NT_PRSTATUS is read with
// ptrace(PTRACE_GETREGSET), which requires stopping the process, and stopping
// a running site to answer a read is a worse answer than a core without
// registers: a debugger opens it and reads memory, and says so about the
// threads. Nothing here is invented — the segments are the process's real
// mappings and their real contents.

// webProcMapping is one of a process's memory mappings.
type webProcMapping struct {
	Low, High uint64
	Readable  bool
	Path      string
}

// webProcessMappings reads a process's mapping table.
func webProcessMappings(pid int) ([]webProcMapping, bool) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var out []webProcMapping
	scanner := bufio.NewScanner(f)
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
		out = append(out, webProcMapping{
			Low: low, High: high,
			Readable: strings.HasPrefix(fields[1], "r"),
			Path:     path,
		})
	}
	if scanner.Err() != nil {
		return nil, false
	}
	return out, true
}

// webProcessCoreDump writes an ELF core of the process to w. The mappings it
// cannot read are left out rather than zero-filled: a segment of zeroes claims
// the process holds zeroes there, and it does not — it holds something this
// reader was not allowed to see.
func webProcessCoreDump(pid int, mappings []webProcMapping, w io.Writer) error {
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
	if err != nil {
		return fmt.Errorf("open the process's memory: %w", err)
	}
	defer mem.Close()

	// Only the mappings that can actually be read become segments. A mapping
	// is probed rather than assumed: a device mapping is readable by its
	// permission bits and still refuses a read through /proc/<pid>/mem.
	type segment struct {
		mapping webProcMapping
		offset  uint64
	}
	var segments []segment
	probe := make([]byte, 1)
	for _, m := range mappings {
		if !m.Readable || m.Path == "[vvar]" || m.Path == "[vsyscall]" {
			continue
		}
		if _, err := mem.ReadAt(probe, int64(m.Low)); err != nil {
			continue
		}
		segments = append(segments, segment{mapping: m})
	}
	if len(segments) == 0 {
		return fmt.Errorf("no mapping of the process could be read")
	}

	const headerSize = 64
	const programHeaderSize = 56
	offset := uint64(headerSize + programHeaderSize*len(segments))
	for i := range segments {
		segments[i].offset = offset
		offset += segments[i].mapping.High - segments[i].mapping.Low
	}

	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
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
		size := s.mapping.High - s.mapping.Low
		ph := make([]byte, programHeaderSize)
		order.PutUint32(ph[0:], uint32(elf.PT_LOAD))
		order.PutUint32(ph[4:], uint32(elf.PF_R))
		order.PutUint64(ph[8:], s.offset)
		order.PutUint64(ph[16:], s.mapping.Low) // p_vaddr
		order.PutUint64(ph[32:], size)          // p_filesz
		order.PutUint64(ph[40:], size)          // p_memsz
		order.PutUint64(ph[48:], 1)             // p_align
		if _, err := w.Write(ph); err != nil {
			return err
		}
	}

	for _, s := range segments {
		size := int64(s.mapping.High - s.mapping.Low)
		if _, err := io.Copy(w, io.NewSectionReader(mem, int64(s.mapping.Low), size)); err != nil {
			return fmt.Errorf("read the mapping at %#x: %w", s.mapping.Low, err)
		}
	}
	return nil
}

// webGetProcessDump answers WebApps_GetProcessDump and its three siblings.
func webGetProcessDump(w http.ResponseWriter, r *http.Request) {
	proc, _, ok := webResolveProcess(w, r)
	if !ok {
		return
	}
	if !webProcIsProcess(proc) {
		sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"%s is not implemented by the simulator: a process dump is the memory "+
				"image of the process, read from its own /proc/<pid>/mem, and this host "+
				"does not share a kernel with the container engine, so the site's "+
				"processes are not in its /proc.", webProcessDumpOperation(r))
		return
	}
	mappings, ok := webProcessMappings(proc.PID)
	if !ok {
		sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"%s is not implemented by the simulator: the process's mapping table "+
				"could not be read.", webProcessDumpOperation(r))
		return
	}
	// The body is the image itself, so nothing may be written before it is
	// known to be producible — an error after the first byte would be a
	// truncated core the caller cannot tell from a complete one.
	var buffer strings.Builder
	if err := webProcessCoreDump(proc.PID, mappings, &buffer); err != nil {
		// Reading another process's memory needs permission to trace it, which
		// a host can refuse — Yama's ptrace_scope does by default for a process
		// that is not a descendant. That is this host declining, not the
		// simulator failing, so it declares the gap rather than reporting a
		// server error the caller would read as a defect here.
		sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"%s is not implemented on this host: a process dump is the memory image "+
				"of the process, read from its own /proc/<pid>/mem, and this host would "+
				"not let the simulator read it (%v).", webProcessDumpOperation(r), err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("dump-%d.core", proc.PID)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, buffer.String())
}

// webProcessDumpOperation names the operation the request addressed, which
// differs by scope and by whether the site is a slot.
func webProcessDumpOperation(r *http.Request) string {
	name := "WebApps_GetProcessDump"
	if webInstanceSegment(r) != "" {
		name = "WebApps_GetInstanceProcessDump"
	}
	if strings.Contains(r.URL.Path, "/slots/") {
		name += "Slot"
	}
	return name
}
