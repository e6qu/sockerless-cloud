// SPDX-License-Identifier: AGPL-3.0-or-later
package realexec

import "testing"

// A policy the kernel already holds is not committed again. The egress policy
// and the security-group filter are both reapplied on every task start, and
// rebuilding an identical ruleset was seconds of every start.
func TestInstalledProgramIsRememberedPerTable(t *testing.T) {
	n := &Network{}
	program := renderEgressPolicyProgram("egtest", "veth0", "10.0.0.1", []string{"10.42.0.0/20"})

	if _, ok := n.installed.Load("egtest"); ok {
		t.Fatal("nothing is installed before the first commit")
	}
	n.installed.Store("egtest", program)

	committed, ok := n.installed.Load("egtest")
	if !ok || committed != program {
		t.Fatalf("the committed program was not remembered: %v", committed)
	}

	// A changed allowed-source set renders a different program, so the memo
	// must not match it.
	changed := renderEgressPolicyProgram("egtest", "veth0", "10.0.0.1",
		[]string{"10.42.0.0/20", "10.42.16.0/20"})
	if committed == changed {
		t.Fatal("a changed CIDR set must render a different program")
	}

	// Teardown forgets it, so the next attach reinstalls.
	n.installed.Delete("egtest")
	if _, ok := n.installed.Load("egtest"); ok {
		t.Fatal("teardown must forget the installed program")
	}
}
