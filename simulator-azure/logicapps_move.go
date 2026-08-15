package main

// Cross-resource-group move for a standalone Microsoft.Logic/workflows
// resource — a Consumption Logic App, which is its own top-level ARM resource.
// (The Standard workflows hosted inside a Microsoft.Web site are children of
// that site and move with it, through webMoveSiteTree.) The hook table in
// resource_move.go dispatches Resources_MoveResources here, and the provider's
// own Workflows_Move operation performs the same re-homing.
//
// Azure Resource Manager moves a workflow between resource groups ("Azure
// resource types for move operations", Microsoft.Logic / workflows: Resource
// group = Yes).
//
// A workflow keys its ARM record — and its versions, triggers, trigger
// histories, runs and run actions — by resource ID, so the whole subtree
// re-homes onto the destination group through the repointing pass in
// resource_move.go.
//
// Two things a naive re-key would break:
//
//   - The credential. A workflow's access keys are derived from its resource
//     ID, and the `sig` of every callback URL listCallbackUrl has already
//     issued is an HMAC under the primary key, so re-deriving would invalidate
//     every outstanding callback URL. Real Logic Apps invalidates callback URLs
//     on regenerateAccessKey and on nothing else, so the material is pinned
//     onto the moved ID.
//   - The advertised endpoint. A workflow's accessEndpoint is the identifier it
//     was issued on the Logic Apps service host, which names no resource group,
//     so it moves with the record untouched and every callback URL already
//     issued keeps addressing the workflow.

// logicWorkflowKeySlots are the two access-key slots a workflow signs callback
// URLs with and rotates through regenerateAccessKey.
var logicWorkflowKeySlots = []string{"logic-access-primary", "logic-access-secondary"}

// moveLogicWorkflowARM re-homes one standalone workflow's ARM record onto a
// new resource ID, pinning its access keys so a callback URL issued before the
// move still carries a valid signature after it.
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
