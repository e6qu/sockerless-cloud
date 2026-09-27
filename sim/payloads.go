package sim

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Payloads keeps object contents in files of their own, so a store row holds a
// reference and not the bytes. A row is decoded whole on every read, and a
// body inside it made a 1 KiB range of a 6 MiB object cost the whole 6 MiB.
//
// Every write goes to a new file and returns its reference; nothing is
// rewritten in place. The row that points at a file is written after the file,
// and the file it replaced is removed after the row, so a crash between the
// two leaves an unreferenced file (which Sweep removes) and never a row
// pointing at nothing.
type Payloads struct {
	dir string
}

// ErrPayloadGone reports a reference whose file no longer exists: the row that
// named it was replaced after it was read.
var ErrPayloadGone = errors.New("payload no longer exists")

// Payloads returns the payload store called name. A persistent server keeps it
// beside its database, so a restart serves the same bytes; otherwise it lives
// as long as the process, like the in-memory stores.
func (s *Server) Payloads(name string) (*Payloads, error) {
	if strings.ContainsAny(name, `/\`) || name == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("payload store name %q is not a single path element", name)
	}
	var dir string
	if s.config.Persist {
		dir = filepath.Join(s.config.DataDir, "payloads", name)
	} else {
		temp, err := os.MkdirTemp("", "sim-payloads-"+name+"-")
		if err != nil {
			return nil, fmt.Errorf("payload store %s: %w", name, err)
		}
		dir = temp
	}
	return OpenPayloads(dir)
}

// OpenPayloads opens the payload store in dir, creating it when absent.
func OpenPayloads(dir string) (*Payloads, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("payload store %s: %w", dir, err)
	}
	return &Payloads{dir: dir}, nil
}

func (p *Payloads) path(ref string) (string, error) {
	if len(ref) != 32 || strings.Trim(ref, "0123456789abcdef") != "" {
		return "", fmt.Errorf("payload reference %q is malformed", ref)
	}
	return filepath.Join(p.dir, ref), nil
}

// Write stores data in a new file and returns its reference.
func (p *Payloads) Write(data []byte) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("payload reference: %w", err)
	}
	ref := hex.EncodeToString(id[:])
	final, err := p.path(ref)
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(p.dir, ".write-*")
	if err != nil {
		return "", fmt.Errorf("write payload: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return "", fmt.Errorf("write payload: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(temp.Name())
		return "", fmt.Errorf("write payload: %w", err)
	}
	if err := os.Rename(temp.Name(), final); err != nil {
		_ = os.Remove(temp.Name())
		return "", fmt.Errorf("write payload: %w", err)
	}
	return ref, nil
}

// Open opens the payload ref names for reading, so a caller can serve a range
// of it without reading the rest. It returns ErrPayloadGone when the file has
// been removed.
func (p *Payloads) Open(ref string) (*os.File, error) {
	path, err := p.path(ref)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("payload %s: %w", ref, ErrPayloadGone)
	}
	return file, err
}

// Read returns the whole payload ref names.
func (p *Payloads) Read(ref string) ([]byte, error) {
	file, err := p.Open(ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

// Remove deletes the payload ref names. A reference that is already gone is
// not an error: removing is how a replaced row lets go of its file, and two
// writers can race to let go of the same one.
func (p *Payloads) Remove(ref string) error {
	if ref == "" {
		return nil
	}
	path, err := p.path(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove payload %s: %w", ref, err)
	}
	return nil
}

// Sweep removes every file referenced reports false for: the files a crash
// left behind between writing a payload and writing the row that names it,
// and interrupted writes. Run it once at startup, before anything writes.
func (p *Payloads) Sweep(referenced func(ref string) bool) (int, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return 0, fmt.Errorf("sweep payloads: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if _, err := p.path(name); err == nil && referenced(name) {
			continue
		}
		if err := os.Remove(filepath.Join(p.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("sweep payload %s: %w", name, err)
		}
		removed++
	}
	return removed, nil
}
