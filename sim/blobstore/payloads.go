package blobstore

import (
	"bytes"
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
// Every write goes to a new file and returns its reference; nothing a reader
// can hold is rewritten in place. The row that points at a file is written
// after the file, and the file it replaced is removed after the row, so a
// crash between the two leaves an unreferenced file (which Sweep removes) and
// never a row pointing at nothing.
//
// The empty reference names the empty payload: a write of no bytes stores no
// file, and opening "" reads nothing.
type Payloads struct {
	dir string
}

// ErrPayloadGone reports a reference whose file no longer exists: the row that
// named it was replaced after it was read.
var ErrPayloadGone = errors.New("payload no longer exists")

// Reader reads one payload, sequentially or at an offset.
type Reader interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
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

func newRef() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("payload reference: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

// WriteFrom stores everything r yields in a new payload and returns its
// reference and the digests of what it stored, computed as the bytes pass.
func (p *Payloads) WriteFrom(r io.Reader) (string, Digests, error) {
	temp, err := os.CreateTemp(p.dir, ".write-*")
	if err != nil {
		return "", Digests{}, fmt.Errorf("write payload: %w", err)
	}
	discard := func(err error) (string, Digests, error) {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return "", Digests{}, fmt.Errorf("write payload: %w", err)
	}
	digester := newDigester()
	if _, err := io.Copy(io.MultiWriter(temp, digester), r); err != nil {
		return discard(err)
	}
	if err := temp.Close(); err != nil {
		return discard(err)
	}
	digests := digester.sum()
	if digests.Size == 0 {
		_ = os.Remove(temp.Name())
		return "", digests, nil
	}
	ref, err := newRef()
	if err != nil {
		_ = os.Remove(temp.Name())
		return "", Digests{}, err
	}
	final, err := p.path(ref)
	if err != nil {
		_ = os.Remove(temp.Name())
		return "", Digests{}, err
	}
	if err := os.Rename(temp.Name(), final); err != nil {
		_ = os.Remove(temp.Name())
		return "", Digests{}, fmt.Errorf("write payload: %w", err)
	}
	return ref, digests, nil
}

// Write stores data in a new payload.
func (p *Payloads) Write(data []byte) (string, Digests, error) {
	return p.WriteFrom(bytes.NewReader(data))
}

// Concat stores the payloads refs name, one after another, in a new payload,
// streaming each rather than holding the whole in memory.
func (p *Payloads) Concat(refs ...string) (string, Digests, error) {
	readers := make([]io.Reader, 0, len(refs))
	for _, ref := range refs {
		part, err := p.Open(ref)
		if err != nil {
			return "", Digests{}, err
		}
		defer func() { _ = part.Close() }()
		readers = append(readers, part)
	}
	return p.WriteFrom(io.MultiReader(readers...))
}

// WriteAt writes what r yields into the staging payload ref at offset,
// creating the payload when ref is empty, and returns its reference and its
// length afterwards. It rewrites in place, so it is for a payload being
// assembled across requests that no reader holds yet.
func (p *Payloads) WriteAt(ref string, offset int64, r io.Reader) (string, int64, error) {
	if ref == "" {
		created, err := newRef()
		if err != nil {
			return "", 0, err
		}
		ref = created
	}
	path, err := p.path(ref)
	if err != nil {
		return "", 0, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("stage payload %s: %w", ref, err)
	}
	if _, err := io.Copy(io.NewOffsetWriter(file, offset), r); err != nil {
		_ = file.Close()
		return "", 0, fmt.Errorf("stage payload %s: %w", ref, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return "", 0, fmt.Errorf("stage payload %s: %w", ref, err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("stage payload %s: %w", ref, err)
	}
	return ref, info.Size(), nil
}

// Truncate cuts the staging payload ref to size bytes.
func (p *Payloads) Truncate(ref string, size int64) error {
	path, err := p.path(ref)
	if err != nil {
		return err
	}
	if err := os.Truncate(path, size); err != nil {
		return fmt.Errorf("truncate payload %s: %w", ref, err)
	}
	return nil
}

// Digest reads the payload ref names once and returns its digests.
func (p *Payloads) Digest(ref string) (Digests, error) {
	file, err := p.Open(ref)
	if err != nil {
		return Digests{}, err
	}
	defer func() { _ = file.Close() }()
	digester := newDigester()
	if _, err := io.Copy(digester, file); err != nil {
		return Digests{}, fmt.Errorf("digest payload %s: %w", ref, err)
	}
	return digester.sum(), nil
}

type emptyReader struct{ *bytes.Reader }

func (emptyReader) Close() error { return nil }

// Open opens the payload ref names for reading, so a caller can serve a range
// of it without reading the rest. It returns ErrPayloadGone when the file has
// been removed.
func (p *Payloads) Open(ref string) (Reader, error) {
	if ref == "" {
		return emptyReader{bytes.NewReader(nil)}, nil
	}
	path, err := p.path(ref)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("payload %s: %w", ref, ErrPayloadGone)
	}
	if err != nil {
		return nil, err
	}
	return file, nil
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

// Materialize writes a copy of the payload ref names to dest, replacing what
// is there in one rename, so a process reading dest sees the old file or the
// new one and never a partial one. The copy is dest's own: writing to it never
// reaches the payload.
func (p *Payloads) Materialize(ref, dest string) error {
	source, err := p.Open(ref)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	dir := filepath.Dir(dest)
	temp, err := os.CreateTemp(dir, ".materialize-*")
	if err != nil {
		return fmt.Errorf("materialize %s: %w", dest, err)
	}
	fail := func(err error) error {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return fmt.Errorf("materialize %s: %w", dest, err)
	}
	if _, err := io.Copy(temp, source); err != nil {
		return fail(err)
	}
	if err := temp.Chmod(0o644); err != nil {
		return fail(err)
	}
	if err := temp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(temp.Name(), dest); err != nil {
		_ = os.Remove(temp.Name())
		return fmt.Errorf("materialize %s: %w", dest, err)
	}
	return nil
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

// Release removes the payload a write replaced, unless the row that replaced
// it still names it among kept.
func (p *Payloads) Release(replaced string, kept ...string) error {
	for _, ref := range kept {
		if ref == replaced {
			return nil
		}
	}
	return p.Remove(replaced)
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

// Adoption is one pass over the rows that reference a payload store, run as
// the store is opened: it moves contents a row still carries inline into
// payloads of their own and collects every reference a row holds.
type Adoption struct {
	payloads   *Payloads
	referenced map[string]bool
}

// Move stores contents a row carried inline and keeps the new payload.
func (a *Adoption) Move(inline []byte) (string, Digests, error) {
	ref, digests, err := a.payloads.Write(inline)
	if err != nil {
		return "", Digests{}, err
	}
	a.Keep(ref)
	return ref, digests, nil
}

// MoveFrom stores contents a row kept outside the store, in the file path
// names, and keeps the new payload.
func (a *Adoption) MoveFrom(path string) (string, Digests, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", Digests{}, err
	}
	defer func() { _ = file.Close() }()
	ref, digests, err := a.payloads.WriteFrom(file)
	if err != nil {
		return "", Digests{}, err
	}
	a.Keep(ref)
	return ref, digests, nil
}

// Keep records references a row holds, so the sweep that ends the adoption
// leaves their files.
func (a *Adoption) Keep(refs ...string) {
	for _, ref := range refs {
		a.referenced[ref] = true
	}
}

// Adopt runs migrate over the rows that reference p and then removes every
// payload no row kept.
func (p *Payloads) Adopt(migrate func(*Adoption) error) error {
	adoption := &Adoption{payloads: p, referenced: map[string]bool{}}
	if err := migrate(adoption); err != nil {
		return err
	}
	_, err := p.Sweep(func(ref string) bool { return adoption.referenced[ref] })
	return err
}
