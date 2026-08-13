package workflow

import "fmt"

type LegacyContinuationSourceScope string

const (
	LegacyContinuationSourceCurrentNode  LegacyContinuationSourceScope = "current_node"
	LegacyContinuationSourceFanoutBranch LegacyContinuationSourceScope = "fanout_branch"
)

type LegacyContinuationSourceUnresolvedError struct {
	Source       CurrentNodeReference
	TargetNodeID NodeID
	EdgeID       EdgeID
	Scope        LegacyContinuationSourceScope
}

func (e LegacyContinuationSourceUnresolvedError) Error() string {
	return fmt.Sprintf(
		"legacy Workflow continuation source is unresolved for Current Node %v to Node %q on Edge %q",
		e.Source,
		e.TargetNodeID,
		e.EdgeID,
	)
}
