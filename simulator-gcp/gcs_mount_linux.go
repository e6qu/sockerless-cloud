//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// gcsMountWatchMask selects the events Cloud Storage FUSE turns into object
// changes. IN_EXCL_UNLINK drops the close of a file the simulator has already
// replaced with a newer generation, which Cloud Storage FUSE would refuse as
// clobbered.
const gcsMountWatchMask = unix.IN_CLOSE_WRITE | unix.IN_MODIFY | unix.IN_CREATE | unix.IN_DELETE |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

// gcsMtimeMetadataKey is the custom metadata key Cloud Storage FUSE records a
// file's modification time under.
const gcsMtimeMetadataKey = "gcsfuse_mtime"

// gcsMountBarrierFile names the file under the data root whose close marks a
// point in the event stream. A bucket name starts with a letter or digit, so
// no bucket directory takes it.
const gcsMountBarrierFile = ".mount-barrier"

type gcsMountPath struct{ bucket, rel string }

type gcsMountMove struct {
	cookie uint32
	from   gcsMountPath
	dir    bool
}

// gcsMountWatcher reads one inotify instance watching every directory of
// every mounted bucket. One goroutine handles its events in order; the maps
// are guarded by gcsMountMu.
type gcsMountWatcher struct {
	fd       int
	dirs     map[int32]gcsMountPath
	watched  map[gcsMountPath]int32
	barriers map[int32]string
	barrier  map[string]int32
	dirty    map[gcsMountPath]bool
	signal   chan struct{}
	dead     chan struct{}
	// pending is a rename's IN_MOVED_FROM, which the next event pairs with
	// its IN_MOVED_TO or settles as a move out of the bucket. Only the
	// reading goroutine touches it.
	pending *gcsMountMove
	// barrierMu keeps one barrier in flight, so the close it waits for is
	// its own.
	barrierMu sync.Mutex
}

var (
	gcsMountWatcherOnce sync.Once
	gcsMountW           *gcsMountWatcher
	gcsMountWatcherErr  error
)

func gcsMountStartWatcher() (*gcsMountWatcher, error) {
	gcsMountWatcherOnce.Do(func() {
		fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
		if err != nil {
			gcsMountWatcherErr = fmt.Errorf("inotify: %w", err)
			return
		}
		gcsMountW = &gcsMountWatcher{
			fd:       fd,
			dirs:     map[int32]gcsMountPath{},
			watched:  map[gcsMountPath]int32{},
			barriers: map[int32]string{},
			barrier:  map[string]int32{},
			dirty:    map[gcsMountPath]bool{},
			dead:     make(chan struct{}),
		}
		go gcsMountW.run()
	})
	return gcsMountW, gcsMountWatcherErr
}

func gcsMountHostPath(at gcsMountPath) string {
	return filepath.Join(GCSBucketHostDir(at.bucket), filepath.FromSlash(at.rel))
}

func gcsMountChild(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func gcsMountUnder(rel, prefix string) bool {
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

// gcsMountAcquire starts watching bucket for a workload about to mount it
// writable. The first mount records what the directory already holds: a path
// whose object is live stands for it, and a file no live object accounts for
// is ingested.
func gcsMountAcquire(bucket string) error {
	w, err := gcsMountStartWatcher()
	if err != nil {
		return err
	}
	gcsMountMu.Lock()
	if view := gcsMountViews[bucket]; view != nil {
		view.refs++
		gcsMountMu.Unlock()
		return nil
	}
	root := GCSBucketHostDir(bucket)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		gcsMountMu.Unlock()
		return fmt.Errorf("bucket directory %s: %v", root, err)
	}
	view := &gcsMountView{refs: 1, entries: map[string]gcsMountEntry{"": gcsMountEntryOf(info, 0)}}
	gcsMountViews[bucket] = view
	gcsMountsWatched.Add(1)
	var work gcsMountWork
	err = w.scanLocked(bucket, "", view, true, &work)
	gcsMountMu.Unlock()
	if err != nil {
		gcsMountRelease(bucket)
		return err
	}
	work.apply(bucket)
	return nil
}

// gcsMountRelease stops watching bucket for a workload that has stopped,
// once every event the workload caused has been handled.
func gcsMountRelease(bucket string) {
	w := gcsMountW
	if err := gcsMountBarrier(context.Background()); err != nil {
		log.Printf("cloud storage: settle writes through mounts of %s: %v", bucket, err)
	}
	gcsMountMu.Lock()
	defer gcsMountMu.Unlock()
	view := gcsMountViews[bucket]
	if view == nil {
		return
	}
	if view.refs--; view.refs > 0 {
		return
	}
	delete(gcsMountViews, bucket)
	gcsMountsWatched.Add(-1)
	for at, wd := range w.watched {
		if at.bucket == bucket {
			_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
			delete(w.watched, at)
			delete(w.dirs, wd)
		}
	}
	for at := range w.dirty {
		if at.bucket == bucket {
			delete(w.dirty, at)
		}
	}
}

// gcsMountBarrier returns once every event queued before it was called has
// been handled. It closes a file of its own under the data root and waits for
// that close to come through the same event queue.
func gcsMountBarrier(ctx context.Context) error {
	w := gcsMountW
	if w == nil || gcsMountsWatched.Load() == 0 {
		return nil
	}
	w.barrierMu.Lock()
	defer w.barrierMu.Unlock()
	root := gcsDataRoot()
	marker := filepath.Join(root, gcsMountBarrierFile)
	gcsMountMu.Lock()
	if _, ok := w.barrier[marker]; !ok {
		if err := os.MkdirAll(root, 0o755); err != nil {
			gcsMountMu.Unlock()
			return err
		}
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			gcsMountMu.Unlock()
			return err
		}
		wd, err := unix.InotifyAddWatch(w.fd, marker, unix.IN_CLOSE_WRITE)
		if err != nil {
			gcsMountMu.Unlock()
			return fmt.Errorf("watch %s: %w", marker, err)
		}
		w.barrier[marker] = int32(wd)
		w.barriers[int32(wd)] = marker
	}
	done := make(chan struct{})
	w.signal = done
	gcsMountMu.Unlock()
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-w.dead:
		return errors.New("the mount watch stopped")
	case <-ctx.Done():
		gcsMountMu.Lock()
		if w.signal == done {
			w.signal = nil
		}
		gcsMountMu.Unlock()
		return ctx.Err()
	}
}

func (w *gcsMountWatcher) run() {
	defer close(w.dead)
	buf := make([]byte, 64*1024)
	for {
		n, err := unix.Read(w.fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			log.Printf("cloud storage: read mount events: %v", err)
			return
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			wd := int32(binary.NativeEndian.Uint32(buf[offset:]))
			mask := binary.NativeEndian.Uint32(buf[offset+4:])
			cookie := binary.NativeEndian.Uint32(buf[offset+8:])
			length := int(binary.NativeEndian.Uint32(buf[offset+12:]))
			raw := buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+length]
			if i := bytes.IndexByte(raw, 0); i >= 0 {
				raw = raw[:i]
			}
			offset += unix.SizeofInotifyEvent + length
			w.handle(wd, mask, cookie, string(raw))
		}
	}
}

func (w *gcsMountWatcher) handle(wd int32, mask, cookie uint32, name string) {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		w.settleMove()
		w.resync()
		return
	}
	gcsMountMu.Lock()
	if marker, ok := w.barriers[wd]; ok {
		if mask&unix.IN_IGNORED != 0 {
			delete(w.barriers, wd)
			delete(w.barrier, marker)
			gcsMountMu.Unlock()
			return
		}
		gcsMountMu.Unlock()
		w.settleMove()
		gcsMountMu.Lock()
		if w.signal != nil {
			close(w.signal)
			w.signal = nil
		}
		gcsMountMu.Unlock()
		return
	}
	if mask&unix.IN_IGNORED != 0 {
		if dir, ok := w.dirs[wd]; ok {
			delete(w.dirs, wd)
			if w.watched[dir] == wd {
				delete(w.watched, dir)
			}
		}
		gcsMountMu.Unlock()
		return
	}
	dir, ok := w.dirs[wd]
	gcsMountMu.Unlock()
	if !ok || name == "" {
		return
	}
	at := gcsMountPath{dir.bucket, gcsMountChild(dir.rel, name)}
	isDir := mask&unix.IN_ISDIR != 0
	if w.pending != nil && (mask&unix.IN_MOVED_TO == 0 || cookie != w.pending.cookie) {
		w.settleMove()
	}
	switch {
	case mask&unix.IN_MOVED_FROM != 0:
		w.pending = &gcsMountMove{cookie: cookie, from: at, dir: isDir}
	case mask&unix.IN_MOVED_TO != 0:
		if w.pending != nil {
			move := *w.pending
			w.pending = nil
			w.renamed(move.from, at, isDir)
			return
		}
		w.arrived(at, isDir)
	case mask&unix.IN_CREATE != 0:
		if isDir {
			w.arrived(at, true)
			return
		}
		w.markDirty(at)
	case mask&unix.IN_MODIFY != 0:
		w.markDirty(at)
	case mask&unix.IN_CLOSE_WRITE != 0:
		w.closed(at)
	case mask&unix.IN_DELETE != 0:
		w.removed(at, isDir)
	}
}

func (w *gcsMountWatcher) markDirty(at gcsMountPath) {
	gcsMountMu.Lock()
	w.dirty[at] = true
	gcsMountMu.Unlock()
}

// settleMove settles a rename whose IN_MOVED_TO never followed: the file or
// directory left the watched tree.
func (w *gcsMountWatcher) settleMove() {
	if w.pending == nil {
		return
	}
	move := *w.pending
	w.pending = nil
	w.movedOut(move.from, move.dir)
}

// watchLocked adds a watch on the directory at.
func (w *gcsMountWatcher) watchLocked(at gcsMountPath) error {
	wd, err := unix.InotifyAddWatch(w.fd, gcsMountHostPath(at), gcsMountWatchMask)
	if err != nil {
		return fmt.Errorf("watch %s: %w", gcsMountHostPath(at), err)
	}
	if old, ok := w.dirs[int32(wd)]; ok && w.watched[old] == int32(wd) {
		delete(w.watched, old)
	}
	w.dirs[int32(wd)] = at
	w.watched[at] = int32(wd)
	return nil
}

// unwatchLocked removes the watches on the directory at and every directory
// under it.
func (w *gcsMountWatcher) unwatchLocked(at gcsMountPath) {
	for watched, wd := range w.watched {
		if watched.bucket == at.bucket && gcsMountUnder(watched.rel, at.rel) {
			_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
			delete(w.watched, watched)
			delete(w.dirs, wd)
		}
	}
}

// gcsMountWork is what a scan of a directory found to do outside the lock:
// directories a workload made, which get placeholder objects, and files no
// object holds yet.
type gcsMountWork struct {
	dirs  []string
	files []gcsMountIngestion
}

type gcsMountIngestion struct {
	rel  string
	base int64
}

func (work gcsMountWork) apply(bucket string) {
	for _, rel := range work.dirs {
		gcsMountCreateDirectory(bucket, rel)
	}
	for _, file := range work.files {
		gcsMountIngest(gcsMountPath{bucket, file.rel}, file.base)
	}
}

// scanLocked watches the directory rel and everything under it, and records
// what it holds. With adopt, a directory or a file a live object accounts
// for stands for that object, as when a mount begins; otherwise every
// directory the view does not know is one a workload made.
func (w *gcsMountWatcher) scanLocked(bucket, rel string, view *gcsMountView, adopt bool, work *gcsMountWork) error {
	if err := w.watchLocked(gcsMountPath{bucket, rel}); err != nil {
		return err
	}
	dirPath := gcsMountHostPath(gcsMountPath{bucket, rel})
	children, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}
	for _, child := range children {
		childRel := gcsMountChild(rel, child.Name())
		info, err := os.Lstat(filepath.Join(dirPath, child.Name()))
		if err != nil {
			continue
		}
		known, isKnown := view.entries[childRel]
		switch {
		case info.IsDir():
			if !isKnown || !known.matches(info) {
				generation := int64(0)
				if adopt {
					if live, ok := gcsObjects.Get(bucket + "/" + childRel + "/"); ok {
						generation = gcsGenerationNumber(live.Generation)
					}
				} else {
					work.dirs = append(work.dirs, childRel)
				}
				view.entries[childRel] = gcsMountEntryOf(info, generation)
			}
			if err := w.scanLocked(bucket, childRel, view, adopt, work); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if isKnown && !known.dir && known.matches(info) {
				continue
			}
			base := int64(0)
			live, isLive := gcsObjects.Get(bucket + "/" + childRel)
			if isLive {
				base = gcsGenerationNumber(live.Generation)
			}
			if adopt && isLive && gcsObjectSize(live) == info.Size() {
				view.entries[childRel] = gcsMountEntryOf(info, base)
				continue
			}
			if isKnown && !known.dir {
				base = known.generation
			}
			work.files = append(work.files, gcsMountIngestion{rel: childRel, base: base})
		}
	}
	return nil
}

// closed handles a close of a file opened for writing: a file the workload
// changed, or made, becomes a new generation, as Cloud Storage FUSE writes
// one when it flushes a dirty file.
func (w *gcsMountWatcher) closed(at gcsMountPath) {
	gcsMountMu.Lock()
	view := gcsMountViews[at.bucket]
	if view == nil {
		gcsMountMu.Unlock()
		return
	}
	dirty := w.dirty[at]
	delete(w.dirty, at)
	entry, known := view.entries[at.rel]
	gcsMountMu.Unlock()
	if known && !entry.dir && !dirty {
		return
	}
	base := int64(0)
	if known && !entry.dir {
		base = entry.generation
	}
	gcsMountIngest(at, base)
}

// arrived handles a file or directory that appeared at a path by a rename
// from outside the watched tree, or a directory a workload made. The
// simulator's own mirror arrives this way too, and the view already records
// it.
func (w *gcsMountWatcher) arrived(at gcsMountPath, isDir bool) {
	gcsMountMu.Lock()
	view := gcsMountViews[at.bucket]
	if view == nil {
		gcsMountMu.Unlock()
		return
	}
	info, err := os.Lstat(gcsMountHostPath(at))
	if err != nil {
		gcsMountMu.Unlock()
		return
	}
	entry, known := view.entries[at.rel]
	if !isDir {
		gcsMountMu.Unlock()
		if !info.Mode().IsRegular() || (known && !entry.dir && entry.matches(info)) {
			return
		}
		base := int64(0)
		if known && !entry.dir {
			base = entry.generation
		}
		gcsMountIngest(at, base)
		return
	}
	if !info.IsDir() {
		gcsMountMu.Unlock()
		return
	}
	var work gcsMountWork
	if !known || !entry.matches(info) {
		view.entries[at.rel] = gcsMountEntryOf(info, 0)
		work.dirs = append(work.dirs, at.rel)
	}
	if err := w.scanLocked(at.bucket, at.rel, view, false, &work); err != nil {
		log.Printf("cloud storage: scan %s/%s: %v", at.bucket, at.rel, err)
	}
	gcsMountMu.Unlock()
	work.apply(at.bucket)
}

// removed handles an unlink or rmdir: the object of a removed file is
// deleted, and so is the placeholder of a removed directory.
func (w *gcsMountWatcher) removed(at gcsMountPath, isDir bool) {
	gcsMountMu.Lock()
	view := gcsMountViews[at.bucket]
	if view == nil {
		gcsMountMu.Unlock()
		return
	}
	entry, known := view.entries[at.rel]
	if !known || entry.dir != isDir {
		gcsMountMu.Unlock()
		return
	}
	if info, err := os.Lstat(gcsMountHostPath(at)); err == nil && info.IsDir() == isDir && entry.ino == gcsMountEntryOf(info, 0).ino {
		gcsMountMu.Unlock()
		return
	}
	gone := w.forgetTreeLocked(view, at)
	gcsMountMu.Unlock()
	gcsMountDeleteAll(at.bucket, gone)
}

// movedOut handles a file or directory renamed out of the watched tree:
// every object it held is deleted.
func (w *gcsMountWatcher) movedOut(from gcsMountPath, isDir bool) {
	gcsMountMu.Lock()
	view := gcsMountViews[from.bucket]
	if view == nil {
		gcsMountMu.Unlock()
		return
	}
	entry, known := view.entries[from.rel]
	if !known || entry.dir != isDir {
		gcsMountMu.Unlock()
		return
	}
	if isDir {
		w.unwatchLocked(from)
	}
	gone := w.forgetTreeLocked(view, from)
	gcsMountMu.Unlock()
	gcsMountDeleteAll(from.bucket, gone)
}

// gcsMountGone is an object a path stood for, which a removal deletes.
type gcsMountGone struct {
	name       string
	generation int64
}

// forgetTreeLocked drops at and everything under it from the view and
// returns the objects they stood for, deepest first.
func (w *gcsMountWatcher) forgetTreeLocked(view *gcsMountView, at gcsMountPath) []gcsMountGone {
	var gone []gcsMountGone
	for rel, entry := range view.entries {
		if !gcsMountUnder(rel, at.rel) {
			continue
		}
		delete(view.entries, rel)
		delete(w.dirty, gcsMountPath{at.bucket, rel})
		if entry.generation == 0 {
			continue
		}
		name := rel
		if entry.dir {
			name += "/"
		}
		gone = append(gone, gcsMountGone{name: name, generation: entry.generation})
	}
	sortGoneDeepestFirst(gone)
	return gone
}

func sortGoneDeepestFirst(gone []gcsMountGone) {
	slices.SortFunc(gone, func(a, b gcsMountGone) int { return strings.Compare(b.name, a.name) })
}

func gcsMountDeleteAll(bucket string, gone []gcsMountGone) {
	for _, object := range gone {
		gcsMountDelete(bucket, object.name, object.generation)
	}
}

// gcsMountRenamePlan is one object a rename moves.
type gcsMountRenamePlan struct {
	from, to   string
	generation int64
	dir        bool
	ino        uint64
}

// renamed handles a rename within the watched tree. Cloud Storage FUSE
// renames a file by copying its object to the new name and deleting the old
// one, and a directory by doing so for every object under it.
func (w *gcsMountWatcher) renamed(from, to gcsMountPath, isDir bool) {
	if from.bucket != to.bucket {
		w.movedOut(from, isDir)
		w.arrived(to, isDir)
		return
	}
	gcsMountMu.Lock()
	view := gcsMountViews[from.bucket]
	if view == nil {
		gcsMountMu.Unlock()
		return
	}
	entry, known := view.entries[from.rel]
	if !known || entry.dir != isDir {
		gcsMountMu.Unlock()
		w.arrived(to, isDir)
		return
	}
	var plan []gcsMountRenamePlan
	destBase := map[string]int64{}
	for rel, e := range view.entries {
		if !gcsMountUnder(rel, from.rel) {
			continue
		}
		target := to.rel + strings.TrimPrefix(rel, from.rel)
		plan = append(plan, gcsMountRenamePlan{from: rel, to: target, generation: e.generation, dir: e.dir, ino: e.ino})
		if existing, ok := view.entries[target]; ok && existing.dir == e.dir {
			destBase[target] = existing.generation
		}
	}
	for _, p := range plan {
		delete(view.entries, p.from)
	}
	for _, p := range plan {
		entry := gcsMountEntry{dir: p.dir, ino: p.ino}
		if info, err := os.Lstat(gcsMountHostPath(gcsMountPath{to.bucket, p.to})); err == nil {
			entry = gcsMountEntryOf(info, 0)
		}
		entry.generation = destBase[p.to]
		view.entries[p.to] = entry
		if w.dirty[gcsMountPath{from.bucket, p.from}] {
			delete(w.dirty, gcsMountPath{from.bucket, p.from})
			w.dirty[gcsMountPath{to.bucket, p.to}] = true
		}
	}
	if isDir {
		for watched, wd := range w.watched {
			if watched.bucket == from.bucket && gcsMountUnder(watched.rel, from.rel) {
				moved := gcsMountPath{to.bucket, to.rel + strings.TrimPrefix(watched.rel, from.rel)}
				delete(w.watched, watched)
				w.watched[moved] = wd
				w.dirs[wd] = moved
			}
		}
	}
	gcsMountMu.Unlock()
	for _, p := range plan {
		if p.generation == 0 {
			continue
		}
		fromName, toName := p.from, p.to
		if p.dir {
			fromName, toName = fromName+"/", toName+"/"
		}
		if !gcsMountCopy(from.bucket, fromName, p.generation, toName, destBase[p.to]) && !p.dir {
			gcsMountIngest(gcsMountPath{to.bucket, p.to}, destBase[p.to])
		}
		gcsMountDelete(from.bucket, fromName, p.generation)
	}
}

// resync reconciles every mounted bucket with its directory after the event
// queue overflowed and events were lost.
func (w *gcsMountWatcher) resync() {
	gcsMountMu.Lock()
	type bucketWork struct {
		bucket string
		gone   []gcsMountGone
		work   gcsMountWork
	}
	var all []bucketWork
	for bucket, view := range gcsMountViews {
		item := bucketWork{bucket: bucket}
		for rel, entry := range view.entries {
			if rel == "" {
				continue
			}
			info, err := os.Lstat(gcsMountHostPath(gcsMountPath{bucket, rel}))
			if err == nil && info.IsDir() == entry.dir && gcsMountEntryOf(info, 0).ino == entry.ino {
				continue
			}
			item.gone = append(item.gone, w.forgetTreeLocked(view, gcsMountPath{bucket, rel})...)
		}
		if err := w.scanLocked(bucket, "", view, false, &item.work); err != nil {
			log.Printf("cloud storage: rescan %s: %v", bucket, err)
		}
		all = append(all, item)
	}
	gcsMountMu.Unlock()
	for _, item := range all {
		sortGoneDeepestFirst(item.gone)
		gcsMountDeleteAll(item.bucket, item.gone)
		item.work.apply(item.bucket)
	}
}

// gcsMountRecordPlaced returns the place function for a generation whose file
// is already at its path: it records the file the generation was read from.
func gcsMountRecordPlaced(bucket, rel string, info os.FileInfo) func(GCSObject) {
	return func(obj GCSObject) {
		gcsMountMu.Lock()
		defer gcsMountMu.Unlock()
		if view := gcsMountViews[bucket]; view != nil {
			view.entries[rel] = gcsMountEntryOf(info, gcsGenerationNumber(obj.Generation))
		}
	}
}

// gcsMountOpen opens the regular file at rel in a bucket's directory without
// following a symbolic link anywhere on the way, so a link a workload makes
// never reads a file outside the bucket.
func gcsMountOpen(at gcsMountPath) (*os.File, error) {
	root, err := unix.Open(GCSBucketHostDir(at.bucket), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(root) }()
	fd, err := unix.Openat2(root, filepath.FromSlash(at.rel), &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), gcsMountHostPath(at)), nil
}

// gcsMountIngest writes the file at a path of a mounted bucket as a new
// generation of the object of its name, on condition that base is still the
// live generation (0: that no object has the name), as Cloud Storage FUSE
// conditions the write on the generation it read. A file still changing
// while it is read is left for the close that ends the change.
func gcsMountIngest(at gcsMountPath, base int64) {
	file, err := gcsMountOpen(at)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, unix.ELOOP) {
			log.Printf("cloud storage: read %s/%s written through a mount: %v", at.bucket, at.rel, err)
		}
		return
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return
	}
	ref, digests, err := gcsBodies.WriteFrom(file)
	if err != nil {
		log.Printf("cloud storage: read %s/%s written through a mount: %v", at.bucket, at.rel, err)
		return
	}
	after, err := file.Stat()
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || after.Size() != digests.Size {
		gcsReleaseBody(ref)
		return
	}
	attrs := GCSObject{}
	if base != 0 {
		if live, ok := gcsObjects.Get(at.bucket + "/" + at.rel); ok && live.Generation == strconv.FormatInt(base, 10) {
			attrs = GCSObject{
				ContentType:        live.ContentType,
				ContentEncoding:    live.ContentEncoding,
				ContentLanguage:    live.ContentLanguage,
				ContentDisposition: live.ContentDisposition,
				CacheControl:       live.CacheControl,
				StorageClass:       live.StorageClass,
				CustomTime:         live.CustomTime,
				Metadata:           cloneStringMap(live.Metadata),
			}
			attrs.metadataCloned = true
		}
	} else {
		attrs.ContentType = mime.TypeByExtension(path.Ext(at.rel))
	}
	if attrs.Metadata == nil {
		attrs.Metadata, attrs.metadataCloned = map[string]string{}, true
	}
	attrs.Metadata[gcsMtimeMetadataKey] = after.ModTime().UTC().Format(time.RFC3339Nano)
	_, err = persistGCSObject(gcsObjects, at.bucket, at.rel, ref, digests, attrs,
		gcsPreconditions{GenerationMatch: &base}, gcsMountRecordPlaced(at.bucket, at.rel, after))
	gcsMountReport("write", at.bucket, at.rel, err)
}

// gcsMountCreateDirectory writes the placeholder object Cloud Storage FUSE
// makes for a directory a workload creates.
func gcsMountCreateDirectory(bucket, rel string) {
	ref, digests, err := gcsBodies.Write(nil)
	if err != nil {
		log.Printf("cloud storage: directory %s/%s: %v", bucket, rel, err)
		return
	}
	absent := int64(0)
	_, err = persistGCSObject(gcsObjects, bucket, rel+"/", ref, digests, GCSObject{}, gcsPreconditions{GenerationMatch: &absent},
		func(obj GCSObject) {
			gcsMountMu.Lock()
			gcsMountSetDirGenerationLocked(bucket, rel, gcsGenerationNumber(obj.Generation))
			gcsMountMu.Unlock()
		})
	if errors.Is(err, errGCSPreconditionFailed) {
		if live, ok := gcsObjects.Get(bucket + "/" + rel + "/"); ok {
			gcsMountMu.Lock()
			gcsMountSetDirGenerationLocked(bucket, rel, gcsGenerationNumber(live.Generation))
			gcsMountMu.Unlock()
		}
		return
	}
	gcsMountReport("make directory", bucket, rel, err)
}

// gcsMountCopy copies generation of the object from to the name to, on
// condition that base is to's live generation, and reports whether it did.
// The file is already at its new path.
func gcsMountCopy(bucket, from string, generation int64, to string, base int64) bool {
	source, ok := gcsObjects.Get(bucket + "/" + from)
	if !ok || source.Generation != strconv.FormatInt(generation, 10) {
		return false
	}
	source, ref, digests, err := gcsCopyContents(source)
	if err != nil {
		log.Printf("cloud storage: rename %s/%s: %v", bucket, from, err)
		return false
	}
	rel := strings.TrimSuffix(to, "/")
	place := func(obj GCSObject) {
		gcsMountMu.Lock()
		defer gcsMountMu.Unlock()
		view := gcsMountViews[bucket]
		if view == nil {
			return
		}
		if entry, ok := view.entries[rel]; ok {
			entry.generation = gcsGenerationNumber(obj.Generation)
			view.entries[rel] = entry
		}
	}
	_, err = persistGCSObject(gcsObjects, bucket, to, ref, digests, source, gcsPreconditions{GenerationMatch: &base}, place)
	gcsMountReport("rename to", bucket, to, err)
	return err == nil
}

// gcsMountDelete deletes generation of the object name, unless the object
// has moved on to another generation since.
func gcsMountDelete(bucket, name string, generation int64) {
	defer gcsObjectWriters.Lock(bucket + "/" + name)()
	obj, ok := gcsObjects.Get(bucket + "/" + name)
	if !ok || obj.Generation != strconv.FormatInt(generation, 10) {
		return
	}
	b, ok := gcsBuckets.Get(bucket)
	if !ok {
		return
	}
	gcsObjects.Delete(bucket + "/" + name)
	gcsRetireDeletedObject(b, obj)
}

// gcsMountReport logs a change through a mount that Cloud Storage refused.
// Cloud Storage FUSE fails the workload's close with the error; nothing
// carries it back to the workload here.
func gcsMountReport(action, bucket, name string, err error) {
	switch {
	case err == nil:
	case errors.Is(err, errGCSPreconditionFailed):
		log.Printf("cloud storage: %s %s/%s through a mount: the object changed since the mount read it", action, bucket, name)
	default:
		log.Printf("cloud storage: %s %s/%s through a mount: %v", action, bucket, name, err)
	}
}
