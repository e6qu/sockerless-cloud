package sim

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/signals"
)

func TestAnnouncingStoreAnnouncesEveryWriteUnderItsID(t *testing.T) {
	changes := signals.New[string]()
	store := Announcing(MakeStore[int](nil, "announcing_test"), changes)
	isClosed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	writes := []struct {
		name  string
		write func()
	}{
		{"Put", func() { store.Put("a", 1) }},
		{"Update", func() { store.Update("a", func(v *int) { *v++ }) }},
		{"Upsert", func() { store.Upsert("a", func(v *int) { *v++ }) }},
		{"Delete", func() { store.Delete("a") }},
	}
	for _, w := range writes {
		a, b := changes.Changed("a"), changes.Changed("b")
		w.write()
		if !isClosed(a) {
			t.Fatalf("%s of a did not announce a", w.name)
		}
		if isClosed(b) {
			t.Fatalf("%s of a announced b", w.name)
		}
	}

	a := changes.Changed("a")
	if store.Update("a", func(*int) {}) || store.Delete("a") {
		t.Fatal("a write to a missing id reported success")
	}
	if isClosed(a) {
		t.Fatal("a write that changed nothing announced")
	}

	store.Put("b", 1)
	a, b := changes.Changed("a"), changes.Changed("b")
	if store.Prune(func(v int) bool { return v == 1 }) != 1 {
		t.Fatal("Prune did not delete b")
	}
	if !isClosed(a) || !isClosed(b) {
		t.Fatal("Prune did not wake every waiter")
	}
}
