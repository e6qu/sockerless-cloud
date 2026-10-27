package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/revisions"
)

// acaRevision: a change to the app's template creates a revision; a change to
// its configuration applies to the revisions it has.
//
// Real API: https://learn.microsoft.com/en-us/azure/container-apps/revisions
type acaRevision struct {
	ID             string                `json:"id"`
	AppID          string                `json:"appId"`
	Name           string                `json:"name"`
	Sequence       int                   `json:"sequence"`
	CreatedTime    time.Time             `json:"createdTime"`
	LastActiveTime time.Time             `json:"lastActiveTime"`
	Active         bool                  `json:"active"`
	Fqdn           string                `json:"fqdn"`
	Template       *ContainerAppTemplate `json:"template"`
	// ProvisioningState is Provisioning while the replicas start, then Provisioned
	// or Failed; a record without one was provisioned.
	ProvisioningState string `json:"provisioningState,omitempty"`
}

const (
	acaRevisionProvisioning = "Provisioning"
	acaRevisionProvisioned  = "Provisioned"
	acaRevisionFailed       = "Failed"
)

func (rev acaRevision) provisioned() bool {
	return rev.ProvisioningState == "" || rev.ProvisioningState == acaRevisionProvisioned
}

var acaRevisions sim.Store[acaRevision]

const acaDefaultMaxInactiveRevisions = 100

// Ingress reads this on every request to an app's FQDN.
var acaRevisionsByApp sim.GenerationIndex[acaRevision]

func acaAppRevisions(appID string) []acaRevision {
	revisions := append([]acaRevision(nil), acaRevisionsByApp.LookupAll(acaRevisions, appID, func(rev acaRevision) []string {
		return []string{rev.AppID}
	})...)
	sort.Slice(revisions, func(i, j int) bool {
		if !revisions[i].CreatedTime.Equal(revisions[j].CreatedTime) {
			return revisions[i].CreatedTime.Before(revisions[j].CreatedTime)
		}
		return revisions[i].Name < revisions[j].Name
	})
	return revisions
}

func acaAppRevision(appID, name string) (acaRevision, bool) {
	return acaRevisions.Get(armChildID(appID, "revisions", name))
}

func acaSingleRevisionMode(app ContainerApp) bool {
	return app.Properties.Configuration == nil || !strings.EqualFold(app.Properties.Configuration.ActiveRevisionsMode, "Multiple")
}

// A revision's hostname puts the revision name where the app name stands.
func acaAppFqdn(app ContainerApp) string {
	fqdn, latest := app.Properties.LatestRevisionFqdn, app.Properties.LatestRevisionName
	if latest != "" && strings.HasPrefix(fqdn, latest+".") {
		return app.Name + strings.TrimPrefix(fqdn, latest)
	}
	return fqdn
}

func acaRevisionFqdn(appName, appFqdn, revisionName string) string {
	return revisionName + strings.TrimPrefix(appFqdn, appName)
}

func acaTemplatesEqual(a, b *ContainerAppTemplate) bool {
	left, errLeft := revisions.Signature(a)
	right, errRight := revisions.Signature(b)
	return errLeft == nil && errRight == nil && left == right
}

// The template's revisionSuffix when set; otherwise a random suffix for the
// first revision and a seven-digit sequence number after it.
func acaNextRevisionName(app ContainerApp, existing []acaRevision) (string, int, error) {
	if suffix := app.Properties.Template.RevisionSuffix; suffix != "" {
		name := app.Name + "--" + suffix
		for _, rev := range existing {
			if rev.Name == name {
				return "", 0, fmt.Errorf("revision '%s' already exists in container app '%s'; set a revisionSuffix no revision of the app uses", name, app.Name)
			}
		}
		return name, 0, nil
	}
	if len(existing) == 0 {
		return app.Name + "--" + randomSuffix(7), 0, nil
	}
	seq := 0
	for _, rev := range existing {
		seq = max(seq, rev.Sequence)
	}
	seq++
	return fmt.Sprintf("%s--%07d", app.Name, seq), seq, nil
}

// Each entry names a revision or the latest one, and the weights add up to 100.
func acaValidateTraffic(app ContainerApp, revisions []acaRevision) error {
	if acaSingleRevisionMode(app) || app.Properties.Configuration.Ingress == nil {
		return nil
	}
	traffic := app.Properties.Configuration.Ingress.Traffic
	if len(traffic) == 0 {
		return nil
	}
	known := make(map[string]bool, len(revisions))
	for _, rev := range revisions {
		known[rev.Name] = true
	}
	total := int32(0)
	for _, entry := range traffic {
		latest := entry.LatestRevision != nil && *entry.LatestRevision
		if latest == (entry.RevisionName != "") {
			return fmt.Errorf("each traffic weight names either a revisionName or latestRevision, not both and not neither")
		}
		if entry.RevisionName != "" && !known[entry.RevisionName] {
			return fmt.Errorf("traffic weight names revision '%s', which container app '%s' does not have", entry.RevisionName, app.Name)
		}
		if entry.Weight != nil {
			total += *entry.Weight
		}
	}
	if total != 100 {
		return fmt.Errorf("traffic weights of container app '%s' add up to %d; they must add up to 100", app.Name, total)
	}
	return nil
}

// A single-revision app sends everything to its latest ready revision, so a
// revision takes traffic once it has provisioned.
func acaTrafficWeights(app ContainerApp) map[string]int32 {
	latest := app.Properties.LatestReadyRevisionName
	if acaSingleRevisionMode(app) || app.Properties.Configuration.Ingress == nil || len(app.Properties.Configuration.Ingress.Traffic) == 0 {
		return map[string]int32{latest: 100}
	}
	weights := map[string]int32{}
	for _, entry := range app.Properties.Configuration.Ingress.Traffic {
		if entry.Weight == nil {
			continue
		}
		name := entry.RevisionName
		if entry.LatestRevision != nil && *entry.LatestRevision {
			name = latest
		}
		weights[name] += *entry.Weight
	}
	return weights
}

// In proportion to the weights among active revisions with replicas.
func acaPickRevision(app ContainerApp) (acaRevision, bool) {
	weights := acaTrafficWeights(app)
	var candidates []revisions.Weighted[acaRevision]
	for _, rev := range acaAppRevisions(app.ID) {
		if rev.Active && acaRevisionServes(rev.ID) {
			candidates = append(candidates, revisions.Weighted[acaRevision]{Revision: rev, Weight: int(weights[rev.Name])})
		}
	}
	return revisions.Pick(candidates)
}

// acaReconcileRevisions: a template the latest revision does not carry, or
// carried into a failed provisioning, becomes a new active revision that
// provisions once the write is answered. Then the revision mode applies and
// inactive revisions beyond maxInactiveRevisions go, oldest first.
func acaReconcileRevisions(app *ContainerApp, appFqdn string) (*acaRevision, error) {
	revisions := acaAppRevisions(app.ID)
	latest, hasLatest := acaAppRevision(app.ID, app.Properties.LatestRevisionName)
	if hasLatest && latest.ProvisioningState != acaRevisionFailed && acaTemplatesEqual(latest.Template, app.Properties.Template) {
		if err := acaValidateTraffic(*app, revisions); err != nil {
			return nil, &acaRevisionRequestError{err}
		}
		acaEnforceRevisionMode(*app)
		return nil, nil
	}
	name, seq, err := acaNextRevisionName(*app, revisions)
	if err != nil {
		return nil, &acaRevisionRequestError{err}
	}
	app.Properties.LatestRevisionName = name
	app.Properties.LatestRevisionFqdn = acaRevisionFqdn(app.Name, appFqdn, name)
	if err := acaValidateTraffic(*app, append(revisions, acaRevision{Name: name})); err != nil {
		return nil, &acaRevisionRequestError{err}
	}
	rev := acaRevision{
		ID:                armChildID(app.ID, "revisions", name),
		AppID:             app.ID,
		Name:              name,
		Sequence:          seq,
		CreatedTime:       time.Now().UTC(),
		Active:            true,
		Fqdn:              app.Properties.LatestRevisionFqdn,
		Template:          app.Properties.Template,
		ProvisioningState: acaRevisionProvisioning,
	}
	acaRevisions.Put(rev.ID, rev)
	acaEnforceRevisionMode(*app)
	return &rev, nil
}

// acaProvisionRevision settles the revision, and the app while the revision is
// still its latest: Provisioned and Succeeded, or both Failed with the error
// the write's operation reports.
func acaProvisionRevision(appID string, rev acaRevision) *AsyncOperationError {
	app, ok := acaApps.Get(appID)
	if !ok {
		return nil
	}
	var err error
	if app.Properties.RunningStatus != acaStopped {
		err = startACARevisionReplicas(context.Background(), app, rev)
	}
	state := acaRevisionProvisioned
	if err != nil {
		state = acaRevisionFailed
	}
	if !acaRevisions.Update(rev.ID, func(r *acaRevision) {
		r.ProvisioningState = state
		if err != nil {
			r.Active = false
			r.LastActiveTime = time.Now().UTC()
		}
	}) {
		stopACAAppReplicas(rev.ID)
		return nil
	}
	var settled ContainerApp
	acaApps.Update(appID, func(a *ContainerApp) {
		if a.Properties.LatestRevisionName == rev.Name {
			if err != nil {
				a.Properties.ProvisioningState = "Failed"
			} else {
				a.Properties.ProvisioningState = "Succeeded"
				a.Properties.LatestReadyRevisionName = rev.Name
			}
		}
		settled = *a
	})
	if settled.ID != "" {
		acaEnforceRevisionMode(settled)
	}
	if err != nil {
		return &AsyncOperationError{
			Code:    "ContainerAppOperationError",
			Message: fmt.Sprintf("Failed to provision revision for container app '%s'. Error details: %v", app.Name, err),
		}
	}
	return nil
}

// acaRevisionRequestError is fixed by changing the request.
type acaRevisionRequestError struct{ err error }

func (e *acaRevisionRequestError) Error() string { return e.err.Error() }

// acaEnforceRevisionMode deactivates every revision of a single-revision app
// but the latest, and the latest ready one while the latest provisions, and
// removes the inactive revisions the app keeps no room for.
func acaEnforceRevisionMode(app ContainerApp) {
	if acaSingleRevisionMode(app) {
		keep := map[string]bool{app.Properties.LatestRevisionName: true}
		if latest, ok := acaAppRevision(app.ID, app.Properties.LatestRevisionName); !ok || !latest.provisioned() {
			keep[app.Properties.LatestReadyRevisionName] = true
		}
		for _, rev := range acaAppRevisions(app.ID) {
			if rev.Active && !keep[rev.Name] {
				acaDeactivateRevision(rev)
			}
		}
	}
	acaPruneInactiveRevisions(app)
}

func acaDeactivateRevision(rev acaRevision) {
	stopACAAppReplicas(rev.ID)
	rev.Active = false
	rev.LastActiveTime = time.Now().UTC()
	acaRevisions.Put(rev.ID, rev)
}

func acaPruneInactiveRevisions(app ContainerApp) {
	keep := acaDefaultMaxInactiveRevisions
	if app.Properties.Configuration != nil && app.Properties.Configuration.MaxInactiveRevisions != nil {
		keep = int(*app.Properties.Configuration.MaxInactiveRevisions)
	}
	var inactive []acaRevision
	for _, rev := range acaAppRevisions(app.ID) {
		if !rev.Active {
			inactive = append(inactive, rev)
		}
	}
	for len(inactive) > keep {
		acaRevisions.Delete(inactive[0].ID)
		inactive = inactive[1:]
	}
}

func startACAAppRevisions(ctx context.Context, app ContainerApp) error {
	for _, rev := range acaAppRevisions(app.ID) {
		if rev.Active {
			if err := startACARevisionReplicas(ctx, app, rev); err != nil {
				return err
			}
		}
	}
	return nil
}

func stopACAAppRevisions(appID string) {
	for _, rev := range acaAppRevisions(appID) {
		stopACAAppReplicas(rev.ID)
	}
}

// Forget the revisions before stopping their replicas, so a background start
// landing in between finds its revision gone.
func deleteACAAppRevisions(appID string) {
	for _, rev := range acaAppRevisions(appID) {
		acaRevisions.Delete(rev.ID)
		stopACAAppReplicasAndWait(rev.ID)
	}
}

var acaRevisionsByFqdn sim.GenerationIndex[acaRevision]

func acaRevisionByFqdn(host string) (acaRevision, bool) {
	return acaRevisionsByFqdn.Lookup(acaRevisions, host, func(rev acaRevision) []string {
		return []string{rev.Fqdn}
	})
}
