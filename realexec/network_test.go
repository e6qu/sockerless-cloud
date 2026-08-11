package realexec

import "testing"

func TestDeriveLinuxNamePreservesLimit(t *testing.T) {
	got := deriveLinuxName("abcdefghijklmn", "ns")
	if got != "abcdefghijklmns" {
		t.Fatalf("deriveLinuxName = %q, want abcdefghijklmns", got)
	}
	if len(got) > 15 {
		t.Fatalf("derived Linux name length = %d, want <= 15", len(got))
	}
}
