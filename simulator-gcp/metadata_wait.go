package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// gceMetadataChanges wakes the metadata reads that wait for a change: every
// store the metadata tree renders from announces each write to it.
var gceMetadataChanges = signals.New[string]()

// gceMetadataAnswer holds an answer rendered before it is sent.
type gceMetadataAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (a *gceMetadataAnswer) Header() http.Header         { return a.header }
func (a *gceMetadataAnswer) Write(p []byte) (int, error) { return a.body.Write(p) }
func (a *gceMetadataAnswer) WriteHeader(status int) {
	if a.status == 0 {
		a.status = status
	}
}

// etag is the entity tag the metadata server sends with an answer: it names
// the content, so a client long-polling with last_etag learns of a change.
func (a *gceMetadataAnswer) etag() string {
	sum := sha256.Sum256(a.body.Bytes())
	return hex.EncodeToString(sum[:8])
}

func (a *gceMetadataAnswer) send(w http.ResponseWriter) {
	for name, values := range a.header {
		w.Header()[name] = values
	}
	w.Header().Set("ETag", a.etag())
	if a.status == 0 {
		a.status = http.StatusOK
	}
	w.WriteHeader(a.status)
	_, _ = w.Write(a.body.Bytes())
}

// gceMetadataServe answers a metadata read with h, and a read that asks to
// wait for a change once its content differs from last_etag — or, without
// one, from the content when the read arrived — or timeout_sec has passed,
// with the content as it is then.
func gceMetadataServe(w http.ResponseWriter, r *http.Request, h http.HandlerFunc) {
	query := r.URL.Query()
	if !strings.EqualFold(query.Get("wait_for_change"), "true") {
		answer := &gceMetadataAnswer{header: http.Header{}}
		h(answer, r)
		answer.send(w)
		return
	}
	var timeout <-chan time.Time
	if raw := query.Get("timeout_sec"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 {
			http.Error(w, "Non-negative integer expected for timeout_sec", http.StatusBadRequest)
			return
		}
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		timeout = timer.C
	}
	last := query.Get("last_etag")
	for {
		change := gceMetadataChanges.Any()
		answer := &gceMetadataAnswer{header: http.Header{}}
		h(answer, r)
		if answer.status != 0 && answer.status != http.StatusOK {
			answer.send(w)
			return
		}
		switch etag := answer.etag(); {
		case last == "":
			last = etag
		case etag != last:
			answer.send(w)
			return
		}
		select {
		case <-change:
		case <-timeout:
			answer.send(w)
			return
		case <-r.Context().Done():
			return
		}
	}
}
