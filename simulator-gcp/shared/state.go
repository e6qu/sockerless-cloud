package simulator

import (
	"reflect"
	"sync"
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
}

// StateStore is an alias for backward compatibility.
// New code should use Store[T] interface or MemoryStore[T] directly.
type StateStore[T any] = MemoryStore[T]

// MemoryStore is an in-memory implementation of Store backed by a map.
type MemoryStore[T any] struct {
	mu    sync.RWMutex
	items map[string]T
}

// NewStateStore creates a new in-memory store. Returns Store[T] for interface compatibility.
func NewStateStore[T any]() *MemoryStore[T] {
	return &MemoryStore[T]{
		items: make(map[string]T),
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
}

func (s *MemoryStore[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[id]
	if ok {
		delete(s.items, id)
	}
	return ok
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
	return true
}
