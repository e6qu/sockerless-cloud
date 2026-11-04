package main

import "testing"

func TestOrgNewAccountIDDrawsTwelveIndependentDigits(t *testing.T) {
	repeated := 0
	for i := 0; i < 200; i++ {
		id := orgNewAccountID()
		if len(id) != 12 || id[0] == '0' {
			t.Fatalf("account id %q: want 12 digits without a leading zero", id)
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				t.Fatalf("account id %q: non-digit %q", id, c)
			}
		}
		if id[:6] == id[6:] {
			repeated++
		}
	}
	if repeated > 1 {
		t.Fatalf("%d of 200 account ids repeat their first six digits", repeated)
	}
}
