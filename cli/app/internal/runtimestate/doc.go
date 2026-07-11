// Package runtimestate owns CLI runtime-event state transitions.
//
// It consumes wire-safe DTOs from shared/clientui and emits commands for the
// Bubble Tea frontend: pending input mutations, prompt history writes,
// reasoning and assistant streaming commands, activity transitions, background
// process refreshes, and status notices.
package runtimestate
