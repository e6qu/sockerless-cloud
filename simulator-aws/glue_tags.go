package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleGlueTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string            `json:"ResourceArn"`
		TagsToAdd   map[string]string `json:"TagsToAdd"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	resType, name := glueResourceFromARN(req.ResourceArn)
	switch resType {
	case "database":
		db, ok := glueDatabases.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		if db.Tags == nil {
			db.Tags = make(map[string]string)
		}
		for k, v := range req.TagsToAdd {
			db.Tags[k] = v
		}
		glueDatabases.Put(name, db)
	default:
		job, ok := glueJobs.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		if job.Tags == nil {
			job.Tags = make(map[string]string)
		}
		for k, v := range req.TagsToAdd {
			job.Tags[k] = v
		}
		glueJobs.Put(name, job)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn  string   `json:"ResourceArn"`
		TagsToRemove []string `json:"TagsToRemove"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	resType, name := glueResourceFromARN(req.ResourceArn)
	switch resType {
	case "database":
		db, ok := glueDatabases.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		for _, k := range req.TagsToRemove {
			delete(db.Tags, k)
		}
		glueDatabases.Put(name, db)
	default:
		job, ok := glueJobs.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		for _, k := range req.TagsToRemove {
			delete(job.Tags, k)
		}
		glueJobs.Put(name, job)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	resType, name := glueResourceFromARN(req.ResourceArn)
	var tags map[string]string
	switch resType {
	case "database":
		db, ok := glueDatabases.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = db.Tags
	case "crawler":
		cr, ok := glueCrawlers.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = cr.Tags
	case "trigger":
		tr, ok := glueTriggers.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = tr.Tags
	default:
		job, ok := glueJobs.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = job.Tags
	}
	if tags == nil {
		tags = map[string]string{}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}
