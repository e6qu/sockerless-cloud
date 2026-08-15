package simulator

import (
	"fmt"
	"reflect"
	"sync"
)

// Cross-cutting state passes.
//
// Almost every simulator operation belongs to one slice and reaches only the
// stores that slice owns. A few do not: Azure Resource Manager's
// cross-resource-group move re-homes a resource onto a new resource ID, and
// every row stored beneath that ID — and every reference held to it from any
// other slice — has to follow. There is no slice that owns "every reference in
// the simulator", so the pass needs the whole set of stores.
//
// MakeStore records each store it creates here, so the set is exactly the
// stores the running simulator built rather than a hand-maintained list that
// silently rots as slices are added.

// TrackedStore is one store MakeStore created, exposed for a cross-cutting
// pass.
type TrackedStore struct {
	// Table is the store's table name, which is unique per simulator build.
	Table string
	// Remap rewrites every stored key with rekey and every string reachable
	// from every stored value with edit, writing back only the rows one of
	// them changed.
	Remap func(rekey func(string) string, edit func(string) string)
}

var (
	trackedStoresMu sync.Mutex
	trackedStores   []TrackedStore
)

// keyedStore is the key enumeration a tracked store needs. Both store
// implementations provide it; the interface is unexported because enumerating
// keys is only ever a cross-cutting pass's business — a slice addresses its own
// rows by the key it composed.
type keyedStore interface {
	keys() []string
}

// ResetTrackedStores drops the recorded set. buildSimulator calls it before it
// creates a build's stores, so a second build in the same process — which the
// in-process test servers do — scans its own stores rather than the previous
// build's abandoned ones.
func ResetTrackedStores() {
	trackedStoresMu.Lock()
	defer trackedStoresMu.Unlock()
	trackedStores = nil
}

// TrackedStores returns a snapshot of the stores created since the last reset.
func TrackedStores() []TrackedStore {
	trackedStoresMu.Lock()
	defer trackedStoresMu.Unlock()
	return append([]TrackedStore(nil), trackedStores...)
}

// trackStore records one store so a cross-cutting pass can reach it.
func trackStore[T any](table string, store Store[T]) {
	keyed, ok := store.(keyedStore)
	if !ok {
		// Both implementations expose keys(); a third that does not would be
		// invisible to every cross-cutting pass, which is a silent correctness
		// hole rather than a missing feature.
		panic(fmt.Sprintf("MakeStore[%s]: %T enumerates no keys", table, store))
	}
	entry := TrackedStore{
		Table: table,
		Remap: func(rekey func(string) string, edit func(string) string) {
			for _, key := range keyed.keys() {
				row, ok := store.Get(key)
				if !ok {
					// The key list is a snapshot; a concurrent delete is not an
					// error, the row simply no longer exists to rewrite.
					continue
				}
				changed := remapStrings(reflect.ValueOf(&row).Elem(), edit)
				newKey := rekey(key)
				if !changed && newKey == key {
					continue
				}
				if newKey != key {
					store.Delete(key)
				}
				store.Put(newKey, row)
			}
		},
	}
	trackedStoresMu.Lock()
	defer trackedStoresMu.Unlock()
	trackedStores = append(trackedStores, entry)
}

// remapStrings applies edit to every settable string reachable from v and
// reports whether any of them changed.
//
// It walks the value rather than its JSON form so a stored row keeps everything
// it holds: the exported `json:"-"` members the persistence envelope carries in
// its hidden sidecar survive a rewrite that a marshal/unmarshal round trip
// would drop. Unexported members are skipped — reflect cannot set them, and
// every reference a client can observe is an exported wire member.
func remapStrings(v reflect.Value, edit func(string) string) bool {
	switch v.Kind() {
	case reflect.String:
		if !v.CanSet() {
			return false
		}
		edited := edit(v.String())
		if edited == v.String() {
			return false
		}
		v.SetString(edited)
		return true

	case reflect.Pointer:
		if v.IsNil() {
			return false
		}
		return remapStrings(v.Elem(), edit)

	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return false
		}
		// An interface's dynamic value is not addressable, so it is rewritten
		// in a copy and stored back. This is the `map[string]any` properties
		// document most Azure resources hold.
		inner := reflect.New(v.Elem().Type()).Elem()
		inner.Set(v.Elem())
		if !remapStrings(inner, edit) {
			return false
		}
		v.Set(inner)
		return true

	case reflect.Struct:
		changed := false
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanSet() {
				continue
			}
			if remapStrings(field, edit) {
				changed = true
			}
		}
		return changed

	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			// Byte slices are opaque payloads (blob content, manifest bodies),
			// never reference strings.
			return false
		}
		changed := false
		for i := 0; i < v.Len(); i++ {
			if remapStrings(v.Index(i), edit) {
				changed = true
			}
		}
		return changed

	case reflect.Map:
		if v.IsNil() {
			return false
		}
		changed := false
		for _, key := range v.MapKeys() {
			element := reflect.New(v.Type().Elem()).Elem()
			element.Set(v.MapIndex(key))
			if !remapStrings(element, edit) {
				continue
			}
			v.SetMapIndex(key, element)
			changed = true
		}
		return changed

	default:
		return false
	}
}
