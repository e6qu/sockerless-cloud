// SPDX-License-Identifier: AGPL-3.0-or-later

package realexec

import (
	"strings"
	"testing"
)

// The filter is one nft program applied in one process. The program declares
// the table, deletes it and rebuilds it, so a first install and a reinstall
// read the same and a reader never sees a half-built filter. Verified against
// nftables v1.1.6: applies twice in a row and lists back rule for rule.
func TestRenderIngressFilterProgram(t *testing.T) {
	program, err := renderIngressFilterProgram("fw-eh-abc", "eh-abc", []PacketRule{
		{Protocol: "tcp", SourceCIDR: "10.42.0.7/32", FromPort: 3000, ToPort: 3000},
		{Protocol: "udp", SourceCIDR: "10.42.0.0/16", FromPort: 1024, ToPort: 2048},
		{Protocol: "icmp", SourceCIDR: "10.42.0.0/16"},
		{Protocol: "-1", SourceCIDR: "0.0.0.0/0", Action: "drop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"table bridge fw-eh-abc {}",
		"delete table bridge fw-eh-abc",
		"table bridge fw-eh-abc {",
		"\tchain forward {",
		"\t\ttype filter hook forward priority filter; policy accept;",
		"\t\tct state established,related accept",
		`\t\toifname "eh-abc" ether type arp accept`,
		`\t\toifname "eh-abc" ip saddr 10.42.0.7/32 ip protocol tcp tcp dport 3000 accept`,
		`\t\toifname "eh-abc" ip saddr 10.42.0.0/16 ip protocol udp udp dport 1024-2048 accept`,
		`\t\toifname "eh-abc" ip saddr 10.42.0.0/16 ip protocol icmp accept`,
		`\t\toifname "eh-abc" ip saddr 0.0.0.0/0 drop`,
		`\t\toifname "eh-abc" drop`,
		"\t}",
		"}",
		"",
	}, "\n")
	want = strings.ReplaceAll(want, `\t`, "\t")
	if program != want {
		t.Fatalf("program:\n%s\nwant:\n%s", program, want)
	}
}

func TestRenderIngressFilterProgramRejectsWhatNftWouldNot(t *testing.T) {
	if _, err := renderIngressFilterProgram("fw", "eh", []PacketRule{{Protocol: "sctp"}}); err == nil {
		t.Fatal("an unsupported protocol rendered")
	}
	if _, err := renderIngressFilterProgram("fw", "eh", []PacketRule{{Protocol: "tcp", Action: "reject"}}); err == nil {
		t.Fatal("an unsupported action rendered")
	}
}
