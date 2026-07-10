package pty

import (
	"core/internal/testharness/pty/analyzer"
)

type Dimensions = analyzer.Dimensions

func MustDimensions(rows, cols int) Dimensions {
	return analyzer.MustDimensions(rows, cols)
}

var NewDimensions = analyzer.NewDimensions

type Region = analyzer.Region

type ByteRange = analyzer.ByteRange

type Chunk = analyzer.Chunk

var NewChunk = analyzer.NewChunk

type Capture = analyzer.Capture

type ProcessExit = analyzer.ProcessExit

var NewCapture = analyzer.NewCapture

var NewCaptureWithEvents = analyzer.NewCaptureWithEvents

type ResizeEvent = analyzer.ResizeEvent

type CaptureAssembler = analyzer.CaptureAssembler

var NewCaptureAssembler = analyzer.NewCaptureAssembler

type EvidenceLimitExceeded = analyzer.EvidenceLimitExceeded

type EvidenceSource = analyzer.EvidenceSource

const (
	EvidenceSourcePTY           = analyzer.EvidenceSourcePTY
	EvidenceSourceOperationText = analyzer.EvidenceSourceOperationText
)

type ResizePlacement = analyzer.ResizePlacement

type ResizePlacementKind = analyzer.ResizePlacementKind

const (
	ResizeBeforeFirstChunk = analyzer.ResizeBeforeFirstChunk
	ResizeAfterChunk       = analyzer.ResizeAfterChunk
)

var BeforeFirstChunk = analyzer.BeforeFirstChunk

var AfterChunk = analyzer.AfterChunk

type OperationKind = analyzer.OperationKind

const (
	OperationWrite              = analyzer.OperationWrite
	OperationErase              = analyzer.OperationErase
	OperationCursorMove         = analyzer.OperationCursorMove
	OperationScrollRegionChange = analyzer.OperationScrollRegionChange
	OperationResize             = analyzer.OperationResize
	OperationModeChange         = analyzer.OperationModeChange
)

type Position = analyzer.Position

type Operation = analyzer.Operation

type WritePayload = analyzer.WritePayload

var NewWritePayload = analyzer.NewWritePayload

var MustWritePayload = analyzer.MustWritePayload

type Analysis = analyzer.Analysis

type PrivateModeChange = analyzer.PrivateModeChange

type PhaseKind = analyzer.PhaseKind

const (
	PhaseScenarioStart    = analyzer.PhaseScenarioStart
	PhaseWindowStart      = analyzer.PhaseWindowStart
	PhaseWindowEnd        = analyzer.PhaseWindowEnd
	PhaseReadyForQuit     = analyzer.PhaseReadyForQuit
	PhaseScenarioComplete = analyzer.PhaseScenarioComplete
)

type WindowID = analyzer.WindowID

var NewWindowID = analyzer.NewWindowID

type PhaseEvent = analyzer.PhaseEvent

type PhaseMarker = analyzer.PhaseMarker

var EncodePhaseMarker = analyzer.EncodePhaseMarker

type OperationWindow = analyzer.OperationWindow

type AppendOperation = analyzer.AppendOperation

var ResolveOperationWindows = analyzer.ResolveOperationWindows

var ClassifyAppends = analyzer.ClassifyAppends

type Cell = analyzer.Cell

type ScreenSnapshot = analyzer.ScreenSnapshot

type BlankFrameDiagnostic = analyzer.BlankFrameDiagnostic

var NewScreenSnapshot = analyzer.NewScreenSnapshot
