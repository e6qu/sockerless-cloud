package main

// Cross-resource-group move for a standalone Microsoft.Logic/workflows
// (Consumption) resource, dispatched from resource_move.go and from the
// provider's own Workflows_Move; a Standard workflow is a child of its site
// and moves with it (webMoveSiteTree). The subtree keys by resource ID and
// re-homes through the repointing pass. The access keys derive from the
// resource ID and every callback `sig` is an HMAC under the primary key, so
// pin the material onto the moved ID: Logic Apps invalidates callback URLs only
// on regenerateAccessKey. The accessEndpoint names no resource group and moves
// untouched.

var logicWorkflowKeySlots = []string{"logic-access-primary", "logic-access-secondary"}

func moveLogicWorkflowARM(oldID, newID string) {
	workflow, ok := logicWorkflows.Get(oldID)
	if !ok {
		return
	}
	pinAzureKeySlots(oldID, newID, azureKeyMaterial32, logicWorkflowKeySlots...)
	logicWorkflows.Delete(oldID)
	workflow.ID = newID
	workflow.Name = logicLastSeg(newID)
	logicWorkflows.Put(workflow.ID, workflow)
}
