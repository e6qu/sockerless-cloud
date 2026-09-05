package sim

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// Store is the interface for a typed key-value store.
// Implemented by MemoryStore (in-memory) and SQLiteStore (persistent).
type Store[T any] interface {
	Get(id string) (T, bool)
	Put(id string, item T)
	Delete(id string) bool
	List() []T
	Filter(fn func(T) bool) []T
	Len() int
	Update(id string, fn func(*T)) bool
	// Upsert atomically applies fn to the item at id under a single lock,
	// creating it from the zero value when absent (create-or-modify). Use it
	// for read-modify-write that must not race a concurrent writer the way a
	// separate Update-then-Put pair would.
	Upsert(id string, fn func(*T))
	// Generation is a counter this store advances on every write that
	// changed it. Two reads that observe the same generation observed the
	// same contents, so a caller that derives an index from List can keep
	// that index until the generation moves instead of rebuilding it per
	// request. It says nothing about *what* changed.
	//
	// The values are drawn from one counter shared by every store in the
	// process, so a generation identifies a state of a particular store and
	// not merely a number of writes. A per-store counter starting at zero
	// looked equivalent and was not: tests replace a package-level store
	// between cases, the replacement began at zero too, and an index built
	// from the old store matched the new one's generation and was served for
	// it — a load balancer that no longer existed still answering on its
	// hostname. Two stores never share a generation now.
	//
	// It remains meaningless across processes: a restarted simulator restores
	// its rows but starts counting from wherever the new process's counter
	// begins.
	Generation() uint64
}

// storeGenerations is the counter every store's Generation draws from. It is
// process-wide so that no two stores, and no two states of one store, can
// present the same generation to a caller caching something derived from it.
var storeGenerations atomic.Uint64

// nextStoreGeneration returns a generation no store has presented before.
func nextStoreGeneration() uint64 { return storeGenerations.Add(1) }

// StateStore is an alias for backward compatibility.
// New code should use Store[T] interface or MemoryStore[T] directly.
type StateStore[T any] = MemoryStore[T]

// MemoryStore is an in-memory implementation of Store backed by a map.
type MemoryStore[T any] struct {
	mu         sync.RWMutex
	items      map[string]T
	generation uint64
}

// NewStateStore creates a new in-memory store. Returns Store[T] for interface compatibility.
func NewStateStore[T any]() *MemoryStore[T] {
	return &MemoryStore[T]{
		items:      make(map[string]T),
		generation: nextStoreGeneration(),
	}
}

func cloneStoreValue[T any](v T) T {
	cv := cloneReflectValue(reflect.ValueOf(v))
	if !cv.IsValid() {
		var zero T
		return zero
	}
	out, ok := cv.Interface().(T)
	if !ok {
		panic("cloned store value has unexpected type")
	}
	return out
}

func cloneReflectValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		inner := cloneReflectValue(v.Elem())
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneReflectValue(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(cloneReflectValue(iter.Key()), cloneReflectValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Cap())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneReflectValue(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneReflectValue(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneReflectValue(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

// Get returns a snapshot of the stored item. Reference fields (maps, slices,
// pointers) are deep-copied so callers cannot mutate store-owned state.
func (s *MemoryStore[T]) Get(id string) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[id]
	if !ok {
		var zero T
		return zero, false
	}
	return cloneStoreValue(v), true
}

func (s *MemoryStore[T]) Put(id string, item T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = cloneStoreValue(item)
	s.generation = nextStoreGeneration()
}

func (s *MemoryStore[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[id]
	if ok {
		delete(s.items, id)
		s.generation = nextStoreGeneration()
	}
	return ok
}

// Generation reports the write counter described on Store.
func (s *MemoryStore[T]) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// keys returns every key the store holds, for the cross-cutting passes in
// store_scan.go that must address rows they did not compose the key of.
func (s *MemoryStore[T]) keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.items))
	for key := range s.items {
		result = append(result, key)
	}
	return result
}

// List returns snapshots of all stored items.
func (s *MemoryStore[T]) List() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]T, 0, len(s.items))
	for _, v := range s.items {
		result = append(result, cloneStoreValue(v))
	}
	return result
}

// Filter returns snapshots of the stored items matching fn.
func (s *MemoryStore[T]) Filter(fn func(T) bool) []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]T, 0)
	for _, v := range s.items {
		snap := cloneStoreValue(v)
		if fn(snap) {
			result = append(result, snap)
		}
	}
	return result
}

func (s *MemoryStore[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

func (s *MemoryStore[T]) Update(id string, fn func(*T)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[id]
	if !ok {
		return false
	}
	v = cloneStoreValue(v)
	fn(&v)
	s.items[id] = cloneStoreValue(v)
	s.generation = nextStoreGeneration()
	return true
}

// Upsert atomically create-or-modifies the item at id under the single write
// lock (absent → the zero value), avoiding the Update-then-Put race.
func (s *MemoryStore[T]) Upsert(id string, fn func(*T)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.items[id]
	v = cloneStoreValue(v)
	fn(&v)
	s.items[id] = cloneStoreValue(v)
	s.generation = nextStoreGeneration()
}
