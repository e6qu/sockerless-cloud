package main

import (
	"slices"
	"testing"
)

// A container starts after every container its dependsOn names, the declared
// order holding among containers free to start; a name outside the template,
// a self-reference and a cycle are refused.
func TestCloudRunContainerStartOrder(t *testing.T) {
	order, err := cloudRunContainerStartOrder([]Container{
		{Name: "main", DependsOn: []string{"proxy", "cache"}},
		{Name: "cache"},
		{Name: "proxy", DependsOn: []string{"cache"}},
		{Name: "logger"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 0, 3}; !slices.Equal(order, want) {
		t.Fatalf("start order = %v, want %v", order, want)
	}
	for _, refused := range [][]Container{
		{{Name: "main", DependsOn: []string{"absent"}}},
		{{Name: "main", DependsOn: []string{"main"}}},
		{{Name: "a", DependsOn: []string{"b"}}, {Name: "b", DependsOn: []string{"a"}}},
	} {
		if _, err := cloudRunContainerStartOrder(refused); err == nil {
			t.Fatalf("containers %+v were accepted", refused)
		}
	}
}
