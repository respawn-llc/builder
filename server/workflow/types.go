package workflow

import (
	"fmt"
	"strings"
)

type WorkflowID string
type NodeID string
type TransitionGroupID string
type EdgeID string
type TaskID string
type PlacementID string
type RunID string
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
	MaxModelKeyChars               = 64
	MaxDisplayNameChars            = 120
	MaxOutputFieldNameChars        = 64
	MaxOutputFieldDescriptionChars = 1000
	MaxInputFieldNameChars         = MaxOutputFieldNameChars
	MaxInputFieldDescriptionChars  = MaxOutputFieldDescriptionChars
	MaxParameterKeyChars           = MaxOutputFieldNameChars
	MaxParameterDescriptionChars   = MaxOutputFieldDescriptionChars
	MaxOutputValueBytes            = 64 * 1024
	MaxCommentaryBytes             = 64 * 1024
	MaxTaskCommentBytes            = 256 * 1024
)

const RuntimePromptParameterCommentary = "commentary"

type Definition struct {
	ID                    WorkflowID
	DisplayName           string
	ExecutionTargetPolicy ExecutionTargetPolicy
	NodeGroups            []NodeGroup
	Nodes                 []Node
	TransitionGroups      []TransitionGroup
	Edges                 []Edge
}

type NodeGroup struct {
	WorkflowID    WorkflowID
	ID            string
	Key           ModelKey
	DisplayName   string
	MemberNodeIDs []NodeID
}

type Node interface {
	sealedWorkflowNode()
	Identity() NodeIdentity
	Kind() NodeKind
}

type NodeIdentity struct {
	WorkflowID  WorkflowID
	ID          NodeID
	Key         ModelKey
	DisplayName string
	GroupID     string
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
	PromptTemplate string
	CompletionMode string
	InputFields    []InputField
	OutputFields   []OutputField
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
	ScriptPath   OptionalScriptPath
	OutputFields []OutputField
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

func NodeWorkflowID(node Node) WorkflowID {
	if node == nil {
		return ""
	}
	return node.Identity().WorkflowID
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

func NodeGroupID(node Node) string {
	if node == nil {
		return ""
	}
	return node.Identity().GroupID
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

func IsSessionNode(node Node) bool {
	if node == nil {
		return false
	}
	return node.Kind() == NodeKindAgent
}

func IsBoardVisibleNode(node Node) bool {
	return IsExecutableNode(node)
}

func CanSourceParameters(node Node) bool {
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

func CanOwnCompletionContract(node Node) bool {
	return CanSourceParameters(node)
}

func NodeSubagentRole(node Node) string {
	if agent, ok := node.(AgentNode); ok {
		return agent.SubagentRole
	}
	return ""
}

func NodePromptTemplate(node Node) string {
	if agent, ok := node.(AgentNode); ok {
		return agent.PromptTemplate
	}
	return ""
}

func NodeCompletionMode(node Node) string {
	if agent, ok := node.(AgentNode); ok {
		return agent.CompletionMode
	}
	return ""
}

func NodeInputFields(node Node) []InputField {
	if agent, ok := node.(AgentNode); ok {
		return append([]InputField(nil), agent.InputFields...)
	}
	return nil
}

func NodeJoinInputProviders(node Node) []JoinInputProvider {
	if join, ok := node.(JoinNode); ok {
		return append([]JoinInputProvider(nil), join.JoinInputProviders...)
	}
	return nil
}

func NodeOutputFields(node Node) []OutputField {
	switch typed := node.(type) {
	case AgentNode:
		return append([]OutputField(nil), typed.OutputFields...)
	case ScriptNode:
		return append([]OutputField(nil), typed.OutputFields...)
	default:
		return nil
	}
}

func NodeScriptPath(node Node) OptionalScriptPath {
	if script, ok := node.(ScriptNode); ok {
		return script.ScriptPath
	}
	return AbsentScriptPath()
}

type NodeFields struct {
	SubagentRole       string
	PromptTemplate     string
	CompletionMode     string
	InputFields        []InputField
	JoinInputProviders []JoinInputProvider
	OutputFields       []OutputField
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
			PromptTemplate: fields.PromptTemplate,
			CompletionMode: fields.CompletionMode,
			InputFields:    append([]InputField(nil), fields.InputFields...),
			OutputFields:   append([]OutputField(nil), fields.OutputFields...),
		}, nil
	case NodeKindScript:
		return ScriptNode{
			NodeIdentity: identity,
			ScriptPath:   fields.ScriptPath,
			OutputFields: append([]OutputField(nil), fields.OutputFields...),
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
	WorkflowID   WorkflowID
	ID           TransitionGroupID
	SourceNodeID NodeID
	TransitionID TransitionID
	DisplayName  string
	Description  string
}

type Edge struct {
	WorkflowID         WorkflowID
	ID                 EdgeID
	Key                ModelKey
	TransitionGroupID  TransitionGroupID
	TargetNodeID       NodeID
	ContextMode        ContextMode
	ContextSource      ContextSource
	RequiresApproval   bool
	PromptTemplate     string
	Parameters         []Parameter
	InputBindings      []InputBinding
	OutputRequirements []OutputRequirement
}

type Parameter struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

type OutputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InputField struct {
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
