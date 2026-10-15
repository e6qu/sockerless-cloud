package main

import (
	"database/sql"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The exclusions API reads and writes the exclusions of the scope's _Default
// sink. Each is addressed as <parent>/exclusions/<id> and named by its id.

// loggingExclusionID is the identifier LogExclusion.name holds: up to 100
// letters, digits, underscores, hyphens and periods, starting alphanumeric.
var loggingExclusionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

func loggingExclusionName(parent, id string) string {
	return parent + "/exclusions/" + id
}

func loggingDefaultSinkName(parent string) string {
	return parent + "/sinks/" + loggingDefaultID
}

// loggingEditDefaultExclusions applies edit to the _Default sink's exclusions
// and stores the result unless edit reports an error status.
func loggingEditDefaultExclusions(parent string, edit func([]LoggingExclusion) ([]LoggingExclusion, int, string)) (int, string) {
	loggingEnsureDefaults(parent)
	code, msg := http.StatusOK, ""
	logSinks.Upsert(loggingDefaultSinkName(parent), func(sink *LoggingSink) {
		var next []LoggingExclusion
		next, code, msg = edit(slices.Clone(sink.Exclusions))
		if code == http.StatusOK {
			sink.Exclusions = next
		}
	})
	return code, msg
}

func loggingDefaultExclusions(parent string) []LoggingExclusion {
	loggingEnsureDefaults(parent)
	sink, _ := logSinks.Get(loggingDefaultSinkName(parent))
	return sink.Exclusions
}

func handleLoggingCreateExclusion(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	var ex LoggingExclusion
	if err := sim.ReadJSON(r, &ex); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid exclusion: %v", err)
		return
	}
	if !loggingExclusionID.MatchString(ex.Name) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"exclusion name %q must be 1 to 100 letters, digits, underscores, hyphens or periods, starting with a letter or digit", ex.Name)
		return
	}
	if _, err := parseLogFilter(ex.Filter); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "exclusion filter: %v", err)
		return
	}
	now := nowTimestamp()
	ex.CreateTime, ex.UpdateTime = now, now
	code, msg := loggingEditDefaultExclusions(parent, func(all []LoggingExclusion) ([]LoggingExclusion, int, string) {
		if slices.ContainsFunc(all, func(e LoggingExclusion) bool { return e.Name == ex.Name }) {
			return nil, http.StatusConflict, "exclusion " + loggingExclusionName(parent, ex.Name) + " already exists"
		}
		return append(all, ex), http.StatusOK, ""
	})
	if code != http.StatusOK {
		GCPError(w, code, msg, "ALREADY_EXISTS")
		return
	}
	sim.WriteJSON(w, http.StatusOK, ex)
}

func handleLoggingListExclusions(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	items := append([]LoggingExclusion{}, loggingDefaultExclusions(parent)...)
	slices.SortFunc(items, func(a, b LoggingExclusion) int { return strings.Compare(a.Name, b.Name) })
	page, next, ok := paginateList(w, r, items, gcpAsc(func(v LoggingExclusion) string { return v.Name }))
	if !ok {
		return
	}
	resp := map[string]any{"exclusions": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetExclusion(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	id := sim.PathParam(r, "exclusion")
	i := slices.IndexFunc(loggingDefaultExclusions(parent), func(e LoggingExclusion) bool { return e.Name == id })
	if i < 0 {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "exclusion %s not found", loggingExclusionName(parent, id))
		return
	}
	sim.WriteJSON(w, http.StatusOK, loggingDefaultExclusions(parent)[i])
}

func handleLoggingPatchExclusion(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	id := sim.PathParam(r, "exclusion")
	var upd LoggingExclusion
	if err := sim.ReadJSON(r, &upd); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid exclusion: %v", err)
		return
	}
	if upd.Filter != "" {
		if _, err := parseLogFilter(upd.Filter); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "exclusion filter: %v", err)
			return
		}
	}
	var updated LoggingExclusion
	code, msg := loggingEditDefaultExclusions(parent, func(all []LoggingExclusion) ([]LoggingExclusion, int, string) {
		i := slices.IndexFunc(all, func(e LoggingExclusion) bool { return e.Name == id })
		if i < 0 {
			return nil, http.StatusNotFound, "exclusion " + loggingExclusionName(parent, id) + " not found"
		}
		cur := all[i]
		if upd.Description != "" {
			cur.Description = upd.Description
		}
		if upd.Filter != "" {
			cur.Filter = upd.Filter
		}
		cur.Disabled = upd.Disabled
		cur.UpdateTime = nowTimestamp()
		all[i], updated = cur, cur
		return all, http.StatusOK, ""
	})
	if code != http.StatusOK {
		GCPError(w, code, msg, "NOT_FOUND")
		return
	}
	sim.WriteJSON(w, http.StatusOK, updated)
}

func handleLoggingDeleteExclusion(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	id := sim.PathParam(r, "exclusion")
	code, msg := loggingEditDefaultExclusions(parent, func(all []LoggingExclusion) ([]LoggingExclusion, int, string) {
		i := slices.IndexFunc(all, func(e LoggingExclusion) bool { return e.Name == id })
		if i < 0 {
			return nil, http.StatusNotFound, "exclusion " + loggingExclusionName(parent, id) + " not found"
		}
		return slices.Delete(all, i, i+1), http.StatusOK, ""
	})
	if code != http.StatusOK {
		GCPError(w, code, msg, "NOT_FOUND")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// loggingMigrateExclusions moves the exclusions an earlier build kept apart
// from the sinks into their scope's _Default sink.
func loggingMigrateExclusions(db *sql.DB) error {
	return sim.Migrate(db, "logging_exclusions/default_sink", func() error {
		legacy, rows, err := sim.LegacyRows[LoggingExclusion](db, "logging_exclusions")
		if err != nil {
			return err
		}
		for _, row := range rows {
			parent, id, found := strings.Cut(row.ID, "/exclusions/")
			if !found {
				continue
			}
			ex := row.Item
			ex.Name = id
			loggingEditDefaultExclusions(parent, func(all []LoggingExclusion) ([]LoggingExclusion, int, string) {
				if slices.ContainsFunc(all, func(e LoggingExclusion) bool { return e.Name == id }) {
					return all, http.StatusOK, ""
				}
				return append(all, ex), http.StatusOK, ""
			})
			legacy.Delete(row.ID)
		}
		return nil
	})
}
