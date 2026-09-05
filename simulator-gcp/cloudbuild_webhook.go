package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Cloud Build webhook receivers. A delivery arrives from a source host,
// Cloud Build decides which triggers it is for, and each of those starts a
// build. Answering Empty without starting one is what the API returns on
// success, so nothing in the response could tell a caller the build never
// happened — the gap only shows as a build that never appears.
//
// There are two shapes. A trigger's own webhook is addressed by name and
// authenticated by the secret the trigger declares. The three shared receivers
// are addressed by a source host's webhook key and carry the event itself, so
// which triggers a delivery is for is decided by the repository the event
// names.

// cbWebhookDelivery is what a receiver reads out of a delivery: the repository
// it concerns and the ref it moved. Both GitHub and Bitbucket Server spell
// these differently, so each is read where that host puts it.
type cbWebhookDelivery struct {
	Owner      string
	Repository string
	Ref        string
	Revision   string
}

// cbReadWebhookDelivery parses a delivery body. An unreadable body is not a
// delivery, and a delivery naming no repository is for no trigger.
func cbReadWebhookDelivery(body []byte) (cbWebhookDelivery, bool) {
	var event struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Name  string `json:"name"`
				Login string `json:"login"`
			} `json:"owner"`
			// Bitbucket Server spells the project the repository belongs to
			// where GitHub spells the owner.
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
			Slug string `json:"slug"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &event) != nil {
		return cbWebhookDelivery{}, false
	}
	delivery := cbWebhookDelivery{Ref: event.Ref, Revision: event.After}
	delivery.Repository = event.Repository.Name
	if delivery.Repository == "" {
		delivery.Repository = event.Repository.Slug
	}
	switch {
	case event.Repository.Owner.Login != "":
		delivery.Owner = event.Repository.Owner.Login
	case event.Repository.Owner.Name != "":
		delivery.Owner = event.Repository.Owner.Name
	default:
		delivery.Owner = event.Repository.Project.Key
	}
	if delivery.Repository == "" && event.Repository.FullName != "" {
		owner, repo, found := strings.Cut(event.Repository.FullName, "/")
		if found {
			delivery.Owner, delivery.Repository = owner, repo
		}
	}
	return delivery, delivery.Repository != ""
}

// cbWebhookKeyOwner reports whether a webhook key belongs to a source host
// this project has configured. A delivery presenting a key no host was issued
// is not from a host Cloud Build knows.
func cbWebhookKeyOwner(key string) bool {
	if key == "" {
		return false
	}
	for _, config := range cbGHEConfigs.List() {
		if config.WebhookKey == key {
			return true
		}
	}
	for _, config := range cbBitbucketConfigs.List() {
		if config.WebhookKey == key {
			return true
		}
	}
	return false
}

// cbTriggersForDelivery finds the triggers a delivery is for: those whose
// source repository is the one the event names, and whose push filter admits
// the ref it moved. A disabled trigger is not one of them.
func cbTriggersForDelivery(delivery cbWebhookDelivery) []BuildTrigger {
	var matched []BuildTrigger
	for _, trigger := range cbTriggers.List() {
		if trigger.Disabled {
			continue
		}
		if !cbTriggerWatchesRepository(trigger, delivery) {
			continue
		}
		if !cbTriggerPushFilterAdmits(trigger, delivery.Ref) {
			continue
		}
		matched = append(matched, trigger)
	}
	return matched
}

// cbTriggerWatchesRepository reports whether a trigger's source is the
// repository a delivery names. A trigger declares that source in one of three
// places depending on how its repository is connected.
func cbTriggerWatchesRepository(trigger BuildTrigger, delivery cbWebhookDelivery) bool {
	if github := trigger.Github; github != nil {
		if cbStringField(github, "name") == delivery.Repository &&
			(delivery.Owner == "" || cbStringField(github, "owner") == delivery.Owner) {
			return true
		}
	}
	if template := trigger.TriggerTemplate; template != nil {
		if cbStringField(template, "repoName") == delivery.Repository {
			return true
		}
	}
	if event := trigger.RepositoryEventConfig; event != nil {
		// The repository is a Repo API resource name, whose last segment is
		// the repository's own name.
		name := cbStringField(event, "repository")
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name != "" && name == delivery.Repository {
			return true
		}
	}
	return false
}

// cbTriggerPushFilterAdmits reports whether a trigger's push filter admits a
// ref. A trigger with no filter builds every push.
func cbTriggerPushFilterAdmits(trigger BuildTrigger, ref string) bool {
	push := cbTriggerPushFilter(trigger)
	if push == nil {
		return true
	}
	branch, tag := cbStringField(push, "branch"), cbStringField(push, "tag")
	invert, _ := push["invertRegex"].(bool)
	matched := false
	switch {
	case branch != "":
		matched = cbRefMatches(ref, "refs/heads/", branch)
	case tag != "":
		matched = cbRefMatches(ref, "refs/tags/", tag)
	default:
		return true
	}
	if invert {
		return !matched
	}
	return matched
}

func cbTriggerPushFilter(trigger BuildTrigger) map[string]any {
	for _, source := range []map[string]any{trigger.Github, trigger.RepositoryEventConfig} {
		if source == nil {
			continue
		}
		if push, ok := source["push"].(map[string]any); ok {
			return push
		}
	}
	return nil
}

// cbRefMatches reports whether a ref of the given kind matches a filter's
// regular expression, which the filter states over the ref's short name.
func cbRefMatches(ref, prefix, expression string) bool {
	name, found := strings.CutPrefix(ref, prefix)
	if !found {
		return false
	}
	re, err := regexp.Compile(expression)
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

func cbStringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// cbStartTriggeredBuild starts the build a trigger declares, attributed to the
// trigger and to the revision the delivery carried.
func cbStartTriggeredBuild(r *http.Request, trigger BuildTrigger, delivery cbWebhookDelivery) (Build, bool) {
	if trigger.Build == nil {
		return Build{}, false
	}
	project := cbTriggerProject(trigger)
	started := *trigger.Build
	started.ID = generateUUID()
	started.ProjectID = project
	started.Status = "QUEUED"
	started.CreateTime = time.Now().UTC().Format(time.RFC3339)
	started.Name = fmt.Sprintf("projects/%s/locations/global/builds/%s", project, started.ID)
	started.BuildTriggerID = trigger.ID
	// The delivery's revision has nowhere faithful to land: a Build's source is
	// a storage source here, and substitutions are where a trigger's build
	// reads the commit it was started for.
	if delivery.Revision != "" {
		if started.Substitutions == nil {
			started.Substitutions = map[string]string{}
		}
		started.Substitutions["COMMIT_SHA"] = delivery.Revision
		if branch, found := strings.CutPrefix(delivery.Ref, "refs/heads/"); found {
			started.Substitutions["BRANCH_NAME"] = branch
		}
		if tag, found := strings.CutPrefix(delivery.Ref, "refs/tags/"); found {
			started.Substitutions["TAG_NAME"] = tag
		}
	}
	cbBuilds.Put(started.ID, started)
	return executeCancellableBuild(r.Context(), started), true
}

// cbTriggerProject reads the project a trigger belongs to out of its resource
// name, which is where the only copy of it lives.
func cbTriggerProject(trigger BuildTrigger) string {
	parts := strings.Split(trigger.ResourceName, "/")
	if len(parts) >= 2 && parts[0] == "projects" {
		return parts[1]
	}
	return ""
}

// cbHandleSharedWebhook serves the three receivers a source host posts to.
// They differ only in the path the host was given; what each does is the same.
func cbHandleSharedWebhook(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("webhookKey")
	if !cbWebhookKeyOwner(key) {
		GCPErrorf(w, http.StatusUnauthorized, "UNAUTHENTICATED",
			"the webhookKey does not belong to a configured source host")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "could not read the delivery: %v", err)
		return
	}
	delivery, ok := cbReadWebhookDelivery(body)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"the delivery names no repository, so it is for no trigger")
		return
	}
	for _, trigger := range cbTriggersForDelivery(delivery) {
		cbStartTriggeredBuild(r, trigger, delivery)
	}
	// Empty either way: a delivery matching no trigger is not an error, it is
	// a repository nothing is watching.
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
