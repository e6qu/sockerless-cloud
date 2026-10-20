package revisions

import "testing"

func TestSignatureFollowsContent(t *testing.T) {
	type template struct {
		Image string
		Args  []string
	}
	a, err := Signature(template{Image: "app", Args: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	same, _ := Signature(template{Image: "app", Args: []string{"serve"}})
	other, _ := Signature(template{Image: "app", Args: []string{"migrate"}})
	if a != same || a == other {
		t.Fatalf("signatures %s %s %s", a, same, other)
	}
}

func TestPickFollowsTheWeights(t *testing.T) {
	counts := map[string]int{}
	candidates := []Weighted[string]{{"blue", 25}, {"green", 75}, {"idle", 0}}
	for range 4000 {
		rev, ok := Pick(candidates)
		if !ok {
			t.Fatal("no revision picked")
		}
		counts[rev]++
	}
	if counts["idle"] != 0 {
		t.Fatalf("a revision without traffic was picked: %v", counts)
	}
	if counts["blue"] < 800 || counts["blue"] > 1200 {
		t.Fatalf("blue took %d of 4000 requests, want about 1000", counts["blue"])
	}
	if _, ok := Pick([]Weighted[string]{{"idle", 0}}); ok {
		t.Fatal("a pick among revisions without traffic succeeded")
	}
}
