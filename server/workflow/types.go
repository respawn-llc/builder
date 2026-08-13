package workflow

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/workflowcontract"
)

type NodeID string
type TransitionGroupID string
type EdgeID string
type TaskID = runtimeids.TaskID
type TransitionID string
type ModelKey string

type NodeKind string

const (
	NodeKindStart    NodeKind = "start"
	NodeKindAgent    NodeKind = "agent"
	NodeKindScript   NodeKind = "script"
	NodeKindJoin     NodeKind = "join"
	NodeKindTerminal NodeKind = "terminal"
)

type ContextMode string

const (
	ContextModeNewSession                ContextMode = "new_session"
	ContextModeContinueSession           ContextMode = "continue_session"
	ContextModeCompactAndContinueSession ContextMode = "compact_and_continue_session"
)

type AssigneeSelection string

const (
	AssigneeSelectionConfigured   AssigneeSelection = "configured"
	AssigneeSelectionPreviousNode AssigneeSelection = "previous_node"
)

func CanonicalAssigneeSelection(selection AssigneeSelection) AssigneeSelection {
	return AssigneeSelection(strings.TrimSpace(string(selection)))
}

func DefaultAssigneeSelection(selection AssigneeSelection) AssigneeSelection {
	canonical := CanonicalAssigneeSelection(selection)
	if canonical == "" {
		return AssigneeSelectionConfigured
	}
	return canonical
}

type ThinkingSelection string

const (
	ThinkingSelectionConfigured   ThinkingSelection = "configured"
	ThinkingSelectionPreviousNode ThinkingSelection = "previous_node"
)

func CanonicalThinkingSelection(selection ThinkingSelection) ThinkingSelection {
	return ThinkingSelection(strings.TrimSpace(string(selection)))
}

func DefaultThinkingSelection(selection ThinkingSelection) ThinkingSelection {
	canonical := CanonicalThinkingSelection(selection)
	if canonical == "" {
		return ThinkingSelectionConfigured
	}
	return canonical
}

type ContextSourceKind string

const (
	ContextSourceImmediateSource     ContextSourceKind = "immediate_source"
	ContextSourceSelectedNode        ContextSourceKind = "selected_node"
	ContextSourcePreviousTarget      ContextSourceKind = "previous_target"
	ContextSourcePreviousTargetOrNew ContextSourceKind = "previous_target_or_new"
)

type ContextSource struct {
	Kind    ContextSourceKind `json:"kind"`
	NodeKey ModelKey          `json:"node_key,omitempty"`
}

func CanonicalContextSource(source ContextSource) ContextSource {
	kind := ContextSourceKind(strings.TrimSpace(string(source.Kind)))
	nodeKey := ModelKey(strings.TrimSpace(string(source.NodeKey)))
	if kind == "" || kind == ContextSourceImmediateSource {
		return ContextSource{Kind: ContextSourceImmediateSource}
	}
	if kind == ContextSourcePreviousTarget || kind == ContextSourcePreviousTargetOrNew {
		return ContextSource{Kind: kind}
	}
	return ContextSource{Kind: kind, NodeKey: nodeKey}
}

type BindingSource string

const (
	BindingSourceTask             BindingSource = "task"
	BindingSourceTransitionOutput BindingSource = "transition_output"
	BindingSourceJoin             BindingSource = "join"
)

const (
	MaxModelKeyChars               = workflowcontract.MaxModelKeyChars
	MaxDisplayNameChars            = workflowcontract.MaxDisplayNameChars
	MaxOutputFieldNameChars        = workflowcontract.MaxOutputFieldNameChars
	MaxOutputFieldDescriptionChars = workflowcontract.MaxOutputFieldDescriptionChars
	MaxParameterKeyChars           = workflowcontract.MaxParameterKeyChars
	MaxParameterDescriptionChars   = workflowcontract.MaxParameterDescriptionChars
	MaxOutputValueBytes            = workflowcontract.MaxOutputValueBytes
	MaxCommentaryBytes             = workflowcontract.MaxCommentaryBytes
	MaxTaskCommentBytes            = workflowcontract.MaxTaskCommentBytes
)

const RuntimePromptParameterCommentary = workflowcontract.RuntimePromptParameterCommentary

type Definition struct {
	ID                    runtimeids.WorkflowID
	DisplayName           string
	ExecutionTargetPolicy ExecutionTargetPolicy
	NodeGroups            []NodeGroup
	Nodes                 []Node
	TransitionGroups      []TransitionGroup
	Edges                 []Edge
}

type NodeGroup struct {
	WorkflowID    runtimeids.WorkflowID
	ID            string
	Key           ModelKey
	DisplayName   string
	SortOrder     int64
	MemberNodeIDs []NodeID
}

type Node interface {
	sealedWorkflowNode()
	Identity() NodeIdentity
	Kind() NodeKind
}

type NodeIdentity struct {
	WorkflowID  runtimeids.WorkflowID
	ID          NodeID
	Key         ModelKey
	DisplayName string
	GroupID     *string
}

type OptionalScriptPath struct {
	value string
	set   bool
}

func AbsentScriptPath() OptionalScriptPath {
	return OptionalScriptPath{}
}

func PresentScriptPath(path string) (OptionalScriptPath, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return OptionalScriptPath{}, false
	}
	return OptionalScriptPath{value: trimmed, set: true}, true
}

func MustPresentScriptPath(path string) OptionalScriptPath {
	value, ok := PresentScriptPath(path)
	if !ok {
		panic("workflow: script path must be non-empty when present")
	}
	return value
}

func (p OptionalScriptPath) IsPresent() bool {
	return p.set
}

func (p OptionalScriptPath) Value() (string, bool) {
	return p.value, p.set
}

func (p OptionalScriptPath) String() string {
	if !p.set {
		return ""
	}
	return p.value
}

type StartNode struct {
	NodeIdentity
}

func (StartNode) sealedWorkflowNode() {}
func (n StartNode) Identity() NodeIdentity {
	return n.NodeIdentity
}
func (StartNode) Kind() NodeKind {
	return NodeKindStart
}

type AgentNode struct {
	NodeIdentity
	SubagentRole   string
	CompletionMode string
}

func (AgentNode) sealedWorkflowNode() {}
func (n AgentNode) Identity() NodeIdentity {
	return n.NodeIdentity
}
func (AgentNode) Kind() NodeKind {
	return NodeKindAgent
}

type ScriptNode struct {
	NodeIdentity
	ScriptPath OptionalScriptPath
}

func (ScriptNode) sealedWorkflowNode() {}
func (n ScriptNode) Identity() NodeIdentity {
	return n.NodeIdentity
}
func (ScriptNode) Kind() NodeKind {
	return NodeKindScript
}

type JoinNode struct {
	NodeIdentity
	JoinInputProviders []JoinInputProvider
}

func (JoinNode) sealedWorkflowNode() {}
func (n JoinNode) Identity() NodeIdentity {
	return n.NodeIdentity
}
func (JoinNode) Kind() NodeKind {
	return NodeKindJoin
}

type TerminalNode struct {
	NodeIdentity
}

func (TerminalNode) sealedWorkflowNode() {}
func (n TerminalNode) Identity() NodeIdentity {
	return n.NodeIdentity
}
func (TerminalNode) Kind() NodeKind {
	return NodeKindTerminal
}

func WorkflowIDPointer(workflowID runtimeids.WorkflowID) *runtimeids.WorkflowID {
	return &workflowID
}

func NodeWorkflowID(node Node) *runtimeids.WorkflowID {
	if node == nil {
		return nil
	}
	return WorkflowIDPointer(node.Identity().WorkflowID)
}

func NodeIDOf(node Node) NodeID {
	if node == nil {
		return ""
	}
	return node.Identity().ID
}

func NodeKey(node Node) ModelKey {
	if node == nil {
		return ""
	}
	return node.Identity().Key
}

func NodeDisplayName(node Node) string {
	if node == nil {
		return ""
	}
	return node.Identity().DisplayName
}

func NodeGroupID(node Node) (string, bool) {
	if node == nil {
		return "", false
	}
	groupID := node.Identity().GroupID
	if groupID == nil {
		return "", false
	}
	return *groupID, true
}

func IsExecutableNode(node Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind() {
	case NodeKindAgent, NodeKindScript:
		return true
	default:
		return false
	}
}

func NodeSubagentRole(node Node) string {
	if agent, ok := node.(AgentNode); ok {
		return agent.SubagentRole
	}
	return ""
}

func NodeCompletionMode(node Node) string {
	if agent, ok := node.(AgentNode); ok {
		return agent.CompletionMode
	}
	return ""
}

func NodeJoinInputProviders(node Node) []JoinInputProvider {
	if join, ok := node.(JoinNode); ok {
		return append([]JoinInputProvider(nil), join.JoinInputProviders...)
	}
	return nil
}

func NodeScriptPath(node Node) OptionalScriptPath {
	if script, ok := node.(ScriptNode); ok {
		return script.ScriptPath
	}
	return AbsentScriptPath()
}

type NodeFields struct {
	SubagentRole       string
	CompletionMode     string
	JoinInputProviders []JoinInputProvider
	ScriptPath         OptionalScriptPath
}

func NewNode(identity NodeIdentity, kind NodeKind, fields NodeFields) (Node, error) {
	switch kind {
	case NodeKindStart:
		return StartNode{NodeIdentity: identity}, nil
	case NodeKindAgent:
		return AgentNode{
			NodeIdentity:   identity,
			SubagentRole:   fields.SubagentRole,
			CompletionMode: fields.CompletionMode,
		}, nil
	case NodeKindScript:
		return ScriptNode{
			NodeIdentity: identity,
			ScriptPath:   fields.ScriptPath,
		}, nil
	case NodeKindJoin:
		return JoinNode{
			NodeIdentity:       identity,
			JoinInputProviders: append([]JoinInputProvider(nil), fields.JoinInputProviders...),
		}, nil
	case NodeKindTerminal:
		return TerminalNode{NodeIdentity: identity}, nil
	default:
		return nil, fmt.Errorf("workflow node kind %q is invalid", kind)
	}
}

type TransitionGroup struct {
	WorkflowID   runtimeids.WorkflowID
	ID           TransitionGroupID
	SourceNodeID NodeID
	TransitionID TransitionID
	DisplayName  string
	Description  string
}

type Edge struct {
	WorkflowID         runtimeids.WorkflowID
	ID                 EdgeID
	Key                ModelKey
	TransitionGroupID  TransitionGroupID
	TargetNodeID       NodeID
	AssigneeSelection  AssigneeSelection
	ThinkingSelection  ThinkingSelection
	ContextMode        ContextMode
	ContextSource      ContextSource
	RequiresApproval   bool
	PromptTemplate     string
	Parameters         []Parameter
	InputBindings      []InputBinding
	OutputRequirements []OutputRequirement
}

func (edge Edge) Canonical() Edge {
	edge.AssigneeSelection = CanonicalAssigneeSelection(edge.AssigneeSelection)
	edge.ThinkingSelection = CanonicalThinkingSelection(edge.ThinkingSelection)
	edge.ContextSource = CanonicalContextSource(edge.ContextSource)
	edge.Parameters = append([]Parameter(nil), edge.Parameters...)
	for index := range edge.Parameters {
		edge.Parameters[index] = edge.Parameters[index].Canonical()
	}
	edge.InputBindings = append([]InputBinding(nil), edge.InputBindings...)
	edge.OutputRequirements = append([]OutputRequirement(nil), edge.OutputRequirements...)
	return edge
}

type Parameter struct {
	Key         string           `json:"key"`
	Description string           `json:"description"`
	Purpose     ParameterPurpose `json:"purpose"`
}

type ParameterPurpose string

const (
	ParameterPurposeOrdinary       ParameterPurpose = "ordinary"
	ParameterPurposeTargetAssignee ParameterPurpose = "target_assignee"
	ParameterPurposeTargetThinking ParameterPurpose = "target_thinking"
)

func CanonicalParameterPurpose(purpose ParameterPurpose) ParameterPurpose {
	return ParameterPurpose(strings.TrimSpace(string(purpose)))
}

func (parameter Parameter) Canonical() Parameter {
	parameter.Purpose = CanonicalParameterPurpose(parameter.Purpose)
	return parameter
}

type OutputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type JoinInputProvider struct {
	InputName      string `json:"input_name"`
	ProviderEdgeID EdgeID `json:"provider_edge_id"`
}

type OutputRequirement struct {
	FieldName string `json:"field_name"`
}

type TemplatePlaceholder string

type InputBinding struct {
	Name   string        `json:"name"`
	Source BindingSource `json:"source"`
	Field  string        `json:"field"`
}
