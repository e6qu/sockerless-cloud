package main

import "testing"

// activeTestProject puts an active project with id in Cloud Resource Manager
// for the test.
func activeTestProject(t *testing.T, id string) {
	t.Helper()
	crmProjects.Put(id, CRMProject{Name: "projects/" + gcpNumericID(12), ProjectId: id, DisplayName: id,
		Parent: "organizations/1", State: "ACTIVE"})
	t.Cleanup(func() { crmProjects.Delete(id) })
}
