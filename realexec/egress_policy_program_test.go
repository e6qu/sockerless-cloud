// SPDX-License-Identifier: AGPL-3.0-or-later
package realexec

import (
	"strings"
	"testing"
)

// The egress policy is one nft program: the host address stays reachable, each
// allowed source may leave, and everything else is dropped. It used to be one
// process per rule, which cost seconds of every task start.
func TestRenderEgressPolicyProgram(t *testing.T) {
	program := renderEgressPolicyProgram("egtest", "veth0", "10.0.0.1",
		[]string{"10.42.0.0/20", "10.42.16.0/20"})

	for _, want := range []string{
		"table inet egtest {}\n",
		"delete table inet egtest\n",
		"type filter hook forward priority filter;",
		`oifname "veth0" ip daddr 10.0.0.1 accept`,
		`oifname "veth0" ip saddr 10.42.0.0/20 accept`,
		`oifname "veth0" ip saddr 10.42.16.0/20 accept`,
		`oifname "veth0" drop`,
	} {
		if !strings.Contains(program, want) {
			t.Errorf("program is missing %q:\n%s", want, program)
		}
	}

	// The drop is last: a source accepted above it must not be dropped.
	if strings.Index(program, `ip saddr 10.42.16.0/20 accept`) > strings.Index(program, `oifname "veth0" drop`) {
		t.Errorf("an accept follows the drop:\n%s", program)
	}
}

// A policy with no allowed source still reaches the host and drops the rest,
// which is what an isolated VPC looks like.
func TestRenderEgressPolicyProgramWithNoAllowedSources(t *testing.T) {
	program := renderEgressPolicyProgram("egtest", "veth0", "10.0.0.1", nil)
	if !strings.Contains(program, `oifname "veth0" ip daddr 10.0.0.1 accept`) {
		t.Errorf("the host address must stay reachable:\n%s", program)
	}
	if strings.Contains(program, "ip saddr") {
		t.Errorf("no source was allowed, so no saddr rule belongs here:\n%s", program)
	}
	if !strings.Contains(program, `oifname "veth0" drop`) {
		t.Errorf("the policy must end in a drop:\n%s", program)
	}
}
