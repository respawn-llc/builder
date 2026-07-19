package workflowview

import (
	"core/server/workflow"
	"core/shared/serverapi"
)

func apiContextSource(in workflow.ContextSource) serverapi.WorkflowContextSource {
	source := workflow.CanonicalContextSource(in)
	return serverapi.WorkflowContextSource{
		Kind:    string(source.Kind),
		NodeKey: string(source.NodeKey),
	}
}
