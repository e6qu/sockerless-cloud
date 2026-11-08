package main

import (
	"context"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// crmPurgeAfter is how long a shut-down project stays DELETE_REQUESTED before
// Google Cloud deletes it permanently, as
// resource-manager-delete-restore-projects.md.gz states.
const crmPurgeAfter = 30 * 24 * time.Hour

// crmPurgedProject tombstones a permanently deleted project's ID:
// resource-manager-creating-managing-projects.md.gz says a project ID "cannot
// be in use or previously used; this includes deleted projects".
type crmPurgedProject struct {
	ProjectID string `json:"projectId"`
	PurgeTime string `json:"purgeTime"`
}

var crmPurgedProjects sim.Store[crmPurgedProject]

// crmPurgeWake tells the purger a project's deletion state changed.
var crmPurgeWake = make(chan struct{}, 1)

func crmWakePurger() {
	select {
	case crmPurgeWake <- struct{}{}:
	default:
	}
}

// crmProjectIDTaken reports whether a project ID is in use or was used by a
// project that has since been permanently deleted.
func crmProjectIDTaken(id string) bool {
	if _, ok := crmProjects.Get(id); ok {
		return true
	}
	_, purged := crmPurgedProjects.Get(id)
	return purged
}

// crmPurgeDue permanently deletes every project whose 30-day recovery window
// has closed by now, and answers when the next window closes.
func crmPurgeDue(now time.Time) (next time.Time, pending bool) {
	for _, p := range crmProjects.Filter(func(p CRMProject) bool { return p.State == "DELETE_REQUESTED" }) {
		deleted, err := time.Parse(time.RFC3339Nano, p.DeleteTime)
		if err != nil {
			continue
		}
		due := deleted.Add(crmPurgeAfter)
		if !due.After(now) {
			crmPurgedProjects.Put(p.ProjectId, crmPurgedProject{ProjectID: p.ProjectId, PurgeTime: now.UTC().Format(time.RFC3339Nano)})
			crmProjects.Delete(p.ProjectId)
			continue
		}
		if !pending || due.Before(next) {
			next, pending = due, true
		}
	}
	return next, pending
}

// crmProjectPurger runs the purge as each recovery window closes, and looks
// again whenever a project is shut down or restored.
func crmProjectPurger(ctx context.Context) {
	for {
		next, pending := crmPurgeDue(time.Now())
		var fire <-chan time.Time
		var timer *time.Timer
		if pending {
			timer = time.NewTimer(time.Until(next))
			fire = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-crmPurgeWake:
		case <-fire:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
