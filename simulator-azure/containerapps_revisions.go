package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// acaRevision is one revision of a container app: the template the app carried
// when the revision was created, and whether the revision is active and so runs
// replicas. A change to the app's template creates a revision; a change to its
// configuration applies to the revisions it already has.
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
}

var acaRevisions sim.Store[acaRevision]

// acaDefaultMaxInactiveRevisions is the configuration.maxInactiveRevisions
// Azure Container Apps stamps on an app that sets none.
const acaDefaultMaxInactiveRevisions = 100

func acaRevisionID(appID, name string) string { return appID + "/revisions/" + name }

// acaRevisionsByApp indexes revisions by the app they belong to; ingress reads
// it on every request an app's FQDN receives.
var acaRevisionsByApp sim.GenerationIndex[acaRevision]

// acaAppRevisions returns an app's revisions, oldest first.
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
	return acaRevisions.Get(acaRevisionID(appID, name))
}

func acaSingleRevisionMode(app ContainerApp) bool {
	return app.Properties.Configuration == nil || !strings.EqualFold(app.Properties.Configuration.ActiveRevisionsMode, "Multiple")
}

// acaAppFqdn is the hostname the app's ingress answers on; a revision's own
// hostname puts the revision name where the app name stands.
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
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

// acaNextRevisionName names the revision a template creates: the template's
// revisionSuffix when it sets one; otherwise a random suffix for an app's first
// revision and a seven-digit sequence number for each one after it.
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

// acaValidateTraffic checks the ingress traffic split of a multiple-revision
// app: each entry names a revision or the latest one, and the weights add up to
// 100.
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

// acaTrafficWeights returns the share of the app's ingress traffic each
// revision carries. A single-revision app sends all of it to its latest
// revision.
func acaTrafficWeights(app ContainerApp) map[string]int32 {
	latest := app.Properties.LatestRevisionName
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

// acaPickRevision chooses the revision an ingress request goes to, in
// proportion to the traffic weights among active revisions with replicas.
func acaPickRevision(app ContainerApp) (acaRevision, bool) {
	weights := acaTrafficWeights(app)
	var candidates []acaRevision
	total := int32(0)
	for _, rev := range acaAppRevisions(app.ID) {
		if w := weights[rev.Name]; w > 0 && rev.Active && len(acaRevisionHandles(rev.ID)) > 0 {
			candidates = append(candidates, rev)
			total += w
		}
	}
	if total == 0 {
		return acaRevision{}, false
	}
	n := rand.Int32N(total) // #nosec G404 -- traffic splitting, not a secret.
	for _, rev := range candidates {
		n -= weights[rev.Name]
		if n < 0 {
			return rev, true
		}
	}
	return candidates[len(candidates)-1], true
}

// acaReconcileRevisions brings an app's revisions in line with the app it is
// about to store: a template the latest revision does not carry becomes a new
// active revision with replicas of its own; a configuration change restarts the
// active revisions under the new configuration. A single-revision app then
// keeps only its latest revision active, and inactive revisions beyond
// maxInactiveRevisions are removed, oldest first.
func acaReconcileRevisions(ctx context.Context, app *ContainerApp, appFqdn string) error {
	revisions := acaAppRevisions(app.ID)
	latest, hasLatest := acaAppRevision(app.ID, app.Properties.LatestRevisionName)
	if !hasLatest || !acaTemplatesEqual(latest.Template, app.Properties.Template) {
		name, seq, err := acaNextRevisionName(*app, revisions)
		if err != nil {
			return &acaRevisionRequestError{err}
		}
		app.Properties.LatestRevisionName = name
		app.Properties.LatestRevisionFqdn = acaRevisionFqdn(app.Name, appFqdn, name)
		if err := acaValidateTraffic(*app, append(revisions, acaRevision{Name: name})); err != nil {
			return &acaRevisionRequestError{err}
		}
		rev := acaRevision{
			ID:          acaRevisionID(app.ID, name),
			AppID:       app.ID,
			Name:        name,
			Sequence:    seq,
			CreatedTime: time.Now().UTC(),
			Active:      true,
			Fqdn:        app.Properties.LatestRevisionFqdn,
			Template:    app.Properties.Template,
		}
		if err := startACARevisionReplicas(ctx, *app, rev); err != nil {
			return err
		}
		acaRevisions.Put(rev.ID, rev)
		app.Properties.LatestReadyRevisionName = name
	} else {
		if err := acaValidateTraffic(*app, revisions); err != nil {
			return &acaRevisionRequestError{err}
		}
		for _, rev := range revisions {
			if rev.Active {
				if err := startACARevisionReplicas(ctx, *app, rev); err != nil {
					return err
				}
			}
		}
	}
	acaEnforceRevisionMode(*app)
	return nil
}

// acaRevisionRequestError is a revision change the request itself makes
// impossible, which the client answers by changing the request.
type acaRevisionRequestError struct{ err error }

func (e *acaRevisionRequestError) Error() string { return e.err.Error() }

// acaEnforceRevisionMode deactivates every revision but the latest of a
// single-revision app and removes the inactive revisions the app keeps no
// room for.
func acaEnforceRevisionMode(app ContainerApp) {
	if acaSingleRevisionMode(app) {
		for _, rev := range acaAppRevisions(app.ID) {
			if rev.Active && rev.Name != app.Properties.LatestRevisionName {
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

// startACAAppRevisions starts the replicas of every active revision of the app.
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

// stopACAAppRevisions stops the replicas of every revision of the app.
func stopACAAppRevisions(appID string) {
	for _, rev := range acaAppRevisions(appID) {
		stopACAAppReplicas(rev.ID)
	}
}

func deleteACAAppRevisions(appID string) {
	for _, rev := range acaAppRevisions(appID) {
		stopACAAppReplicas(rev.ID)
		acaRevisions.Delete(rev.ID)
	}
}

func acaRevisionHandles(revisionID string) []*sim.ContainerHandle {
	if v, ok := acaAppReplicaHandles.Load(revisionID); ok {
		handles, _ := v.([]*sim.ContainerHandle)
		return handles
	}
	return nil
}

// acaRevisionsByFqdn indexes revisions by the hostname each answers on.
var acaRevisionsByFqdn sim.GenerationIndex[acaRevision]

func acaRevisionByFqdn(host string) (acaRevision, bool) {
	return acaRevisionsByFqdn.Lookup(acaRevisions, host, func(rev acaRevision) []string {
		return []string{rev.Fqdn}
	})
}
