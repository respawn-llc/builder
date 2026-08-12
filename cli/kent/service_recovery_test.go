package main

import (
	"slices"
	"strings"
	"testing"
)

type managedChildFake struct {
	observation       serviceTerminationObservation
	releases, retains int
	label, retained   string
}

func (f *managedChildFake) target() managedChildSettlementTarget {
	return managedChildSettlementTarget{terminate: func() serviceTerminationObservation { return f.observation }, release: func() { f.releases++ }, retain: func() { f.retains++; f.retained = f.label }}
}
func TestServiceWakeRoutesManagedChildSettlement(t *testing.T) {
	names := []string{"same session", "differing session", "no session"}
	sessions := [][2]uint32{{7, 7}, {7, 8}, {7, 0}}
	for i, name := range names {
		t.Run(name, func(t *testing.T) {
			fake := &managedChildFake{observation: observedStatus(2)}
			calls := 0
			got := routeManagedChildWake(sessions[i][0], sessions[i][1], func() managedChildSettlementDecision {
				calls++
				return settleManagedChild(fake.target(), fake.observation)
			})
			if calls != []int{0, 1, 1}[i] {
				t.Fatalf("settlement calls = %d, want %d", calls, []int{0, 1, 1}[i])
			}
			if i == 0 {
				assertOperations(t, fake, 0, 0)
				return
			}
			assertDecision(t, got, false, true)
			assertOperations(t, fake, 1, 0)
			assertCleanStop(t, got)
		})
	}
}

func TestServiceManagedChildExitBeforeHandoff(t *testing.T) {
	assertDecision(t, settleManagedChild((&managedChildFake{}).target(), observedStatus(2)), false, true)
}

func TestServiceManagedChildTerminationCases(t *testing.T) {
	statusOne := uint32(1)
	names := []string{"status 2", "non-2", "confirmed without status", "unconfirmed"}
	observations := []serviceTerminationObservation{observedStatus(2), {TerminationConfirmed: true, ExitStatus: &statusOne}, {TerminationConfirmed: true}, {}}
	releases, retains := []int{1, 1, 1, 0}, []int{0, 0, 0, 1}
	for i, name := range names {
		t.Run(name, func(t *testing.T) {
			fake := &managedChildFake{observation: observations[i]}
			got := settleManagedChild(fake.target(), observations[i])
			assertDecision(t, got, i == 1, i == 0 || i == 2)
			assertOperations(t, fake, releases[i], retains[i])
		})
	}
}

func TestServiceStaleLaunchedCandidateSettlement(t *testing.T) {
	for i, name := range []string{"status 2", "unconfirmed"} {
		t.Run(name, func(t *testing.T) {
			observation := observedStatus(2)
			if i == 1 {
				observation = serviceTerminationObservation{}
			}
			fake := &managedChildFake{label: "candidate", observation: observation}
			got := settleStaleLaunchedCandidate(fake.target())
			assertDecision(t, got, false, i == 0)
			if fake.retained != map[bool]string{true: "", false: "candidate"}[i == 0] || fake.releases != i^1 {
				t.Fatalf("stale settlement operations = %#v", fake)
			}
			if i == 0 {
				assertCleanStop(t, got)
			}
		})
	}
}

func TestRenderedSystemdUnitRestartPolicy(t *testing.T) {
	lines := strings.Split(renderSystemdUnitText("Kent server", "/bin/kent serve", "server.log", "server.err.log"), "\n")
	for _, want := range []string{"Restart=always", "RestartPreventExitStatus=2", "RestartSec=2"} {
		if !slices.Contains(lines, want) {
			t.Fatalf("rendered unit missing %q", want)
		}
	}
}

func TestWindowsServiceNoResurrectCompletionIsCleanStop(t *testing.T) {
	assertCleanStop(t, managedChildSettlementDecision{Complete: true})
}

func assertDecision(t *testing.T, got managedChildSettlementDecision, replace, complete bool) {
	if got.Replace != replace || got.Complete != complete {
		t.Fatalf("decision = %#v, want replace=%t complete=%t", got, replace, complete)
	}
}

func assertCleanStop(t *testing.T, decision managedChildSettlementDecision) {
	if specific, code := serviceCompletionStatus(); specific || code != 0 {
		t.Fatalf("SCM completion = (%t, %d), want (false, 0)", specific, code)
	}
}

func assertOperations(t *testing.T, fake *managedChildFake, releases, retains int) {
	if fake.releases != releases || fake.retains != retains {
		t.Fatalf("settlement operations = %#v", fake)
	}
}

func observedStatus(status uint32) serviceTerminationObservation {
	return serviceTerminationObservation{TerminationConfirmed: true, ExitStatus: &status}
}
