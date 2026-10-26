package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// armResources is one Azure Resource Manager resource type in a store keyed by
// resource ID.
type armResources[T any] struct {
	store sim.Store[T]
	id    func(*T) string
	// project refreshes the members the provider recomputes on every read.
	// Optional.
	project  func(*T)
	notFound func(w http.ResponseWriter, id string)
}

func (c armResources[T]) projected(res T) T {
	if c.project != nil {
		c.project(&res)
	}
	return res
}

func armUnder[T any](c armResources[T], prefix string) []T {
	store := c.store
	items := store.Filter(func(res T) bool {
		return strings.HasPrefix(c.id(&res), prefix)
	})
	for i := range items {
		items[i] = c.projected(items[i])
	}
	return items
}

func armGet[T any](w http.ResponseWriter, c armResources[T], id string) {
	store := c.store
	res, ok := store.Get(id)
	if !ok {
		c.notFound(w, id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c.projected(res))
}

func armList[T any](w http.ResponseWriter, c armResources[T], prefix string) {
	azureWriteList(w, armUnder(c, prefix))
}

// armDeletion answers status once the resource is gone; for an absent resource
// absentStatus, or the not-found error when absentStatus is zero.
type armDeletion[T any] struct {
	status       int
	absentStatus int
	// after releases what the deleted resource owned. Optional.
	after func(ctx context.Context, id string, deleted T)
}

func armDelete[T any](w http.ResponseWriter, r *http.Request, c armResources[T], id string, d armDeletion[T]) {
	store := c.store
	res, ok := store.Get(id)
	if !ok || !store.Delete(id) {
		if d.absentStatus == 0 {
			c.notFound(w, id)
			return
		}
		w.WriteHeader(d.absentStatus)
		return
	}
	if d.after != nil {
		d.after(r.Context(), id, res)
	}
	w.WriteHeader(d.status)
}
