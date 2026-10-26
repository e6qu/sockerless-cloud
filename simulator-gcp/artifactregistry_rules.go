package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerARRulesAndAttachments(srv *sim.Server, repos sim.Store[Repository], attachments sim.Store[ARAttachment], rules sim.Store[ARRule]) {
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		prefix := repo + "/rules/"
		gcpWriteList(w, r, rules, prefix, "rules", func(v ARRule) string { return v.Name })
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := repo + "/rules/" + sim.PathParam(r, "rule")
		ru, ok := rules.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, ru)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		ruleID := r.URL.Query().Get("ruleId")
		if ruleID == "" {
			GCPError(w, http.StatusBadRequest, "ruleId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var ru ARRule
		if err := sim.ReadJSON(r, &ru); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ru.Name = repo + "/rules/" + ruleID
		rules.Put(ru.Name, ru)
		sim.WriteJSON(w, http.StatusOK, ru)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := repo + "/rules/" + sim.PathParam(r, "rule")
		ru, ok := rules.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", name)
			return
		}
		var patch ARRule
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Action != "" {
			ru.Action = patch.Action
		}
		if patch.Operation != "" {
			ru.Operation = patch.Operation
		}
		if patch.Condition != nil {
			ru.Condition = patch.Condition
		}
		if patch.PackageID != "" {
			ru.PackageID = patch.PackageID
		}
		rules.Put(name, ru)
		sim.WriteJSON(w, http.StatusOK, ru)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := repo + "/rules/" + sim.PathParam(r, "rule")
		if _, ok := rules.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", name)
			return
		}
		rules.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		prefix := repo + "/attachments/"
		gcpWriteList(w, r, attachments, prefix, "attachments", func(v ARAttachment) string { return v.Name })
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := repo + "/attachments/" + sim.PathParam(r, "attachment")
		a, ok := attachments.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "attachment %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, a)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		attachmentID := r.URL.Query().Get("attachmentId")
		if attachmentID == "" {
			GCPError(w, http.StatusBadRequest, "attachmentId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var a ARAttachment
		if err := sim.ReadJSON(r, &a); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		a.Name = repo + "/attachments/" + attachmentID
		a.CreateTime = nowTimestamp()
		a.UpdateTime = nowTimestamp()
		attachments.Put(a.Name, a)
		lro := artifactRegistryLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), a, attachType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := repo + "/attachments/" + sim.PathParam(r, "attachment")
		_, ok = attachments.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "attachment %q not found", name)
			return
		}
		attachments.Delete(name)
		lro := artifactRegistryLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, gcpEmptyType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})
}
