package sim

import "sync"

// KeyedLocks serializes work on one key at a time — the writes to one stored
// object, say. An object store evaluates a write's conditions and applies the
// write as one step: of two writers that each require the version they read,
// exactly one succeeds. A caller holds the key's lock from reading the object
// to storing what replaces it. A key's entry lives only while someone holds or
// awaits it, so the table stays as small as the set of keys in use.
type KeyedLocks struct {
	mu   sync.Mutex
	held map[string]*keyedLock
}

type keyedLock struct {
	sync.Mutex
	users int
}

// NewKeyedLocks returns an empty lock table.
func NewKeyedLocks() *KeyedLocks {
	return &KeyedLocks{held: map[string]*keyedLock{}}
}

// Lock takes key's lock and returns its release.
func (l *KeyedLocks) Lock(key string) func() {
	l.mu.Lock()
	entry := l.held[key]
	if entry == nil {
		entry = &keyedLock{}
		l.held[key] = entry
	}
	entry.users++
	l.mu.Unlock()

	entry.Lock()
	return func() {
		entry.Unlock()
		l.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(l.held, key)
		}
		l.mu.Unlock()
	}
}
