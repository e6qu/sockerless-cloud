package main

import (
	"net/http"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Resource Manager v3 operations carry the fields their metadata
// messages declare, not only the message type.
func TestCRMv3OperationMetadataFields(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "cloudresourcemanager.googleapis.com"

	project := gcpHostOK(t, srv, host, http.MethodPost, "/v3/projects",
		`{"projectId":"meta-proj","parent":"organizations/123456789012"}`)
	meta, _ := project["metadata"].(map[string]any)
	if meta["gettable"] != true || meta["ready"] != true || meta["createTime"] == "" || meta["createTime"] == nil {
		t.Fatalf("CreateProjectMetadata = %v", meta)
	}

	folder := gcpHostOK(t, srv, host, http.MethodPost, "/v3/folders",
		`{"displayName":"meta-folder","parent":"organizations/123456789012"}`)
	meta, _ = folder["metadata"].(map[string]any)
	if meta["displayName"] != "meta-folder" || meta["parent"] != "organizations/123456789012" {
		t.Fatalf("CreateFolderMetadata = %v", meta)
	}
	response, _ := folder["response"].(map[string]any)
	name, _ := response["name"].(string)

	moved := gcpHostOK(t, srv, host, http.MethodPost, "/v3/"+name+":move", `{"destinationParent":"folders/42"}`)
	meta, _ = moved["metadata"].(map[string]any)
	if meta["displayName"] != "meta-folder" || meta["sourceParent"] != "organizations/123456789012" || meta["destinationParent"] != "folders/42" {
		t.Fatalf("MoveFolderMetadata = %v", meta)
	}
}

// createTestProject creates a project through Cloud Resource Manager, the way
// a project exists before any other service accepts resources in it.
func createTestProject(t *testing.T, srv *sim.Server, id string) {
	t.Helper()
	gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v3/projects",
		`{"projectId":"`+id+`","parent":"organizations/123456789012"}`)
}
