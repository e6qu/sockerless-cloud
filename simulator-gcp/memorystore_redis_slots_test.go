package main

import "testing"

func TestMSRedisPlanSlotMovesGivesEveryPrimaryAnEqualShare(t *testing.T) {
	all := func(low, high int) []int {
		var slots []int
		for s := low; s <= high; s++ {
			slots = append(slots, s)
		}
		return slots
	}
	cases := []struct {
		name      string
		primaries []int
		donors    []int
		owned     map[int][]int
	}{
		{"grow 1 to 2", []int{0, 1}, nil, map[int][]int{0: all(0, 16383)}},
		{"grow 2 to 3", []int{0, 1, 2}, nil, map[int][]int{0: all(0, 8191), 1: all(8192, 16383)}},
		{"shrink 3 to 2", []int{0, 1}, []int{2}, map[int][]int{0: all(0, 5461), 1: all(5462, 10922), 2: all(10923, 16383)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			owner := map[int]int{}
			for node, slots := range c.owned {
				for _, s := range slots {
					owner[s] = node
				}
			}
			for _, move := range msRedisPlanSlotMoves(c.primaries, c.donors, c.owned) {
				for _, s := range move.slots {
					if owner[s] != move.from {
						t.Fatalf("slot %d moves from node %d, which does not own it", s, move.from)
					}
					owner[s] = move.to
				}
			}
			count := map[int]int{}
			for s := 0; s < msRedisClusterSlots; s++ {
				count[owner[s]]++
			}
			for _, d := range c.donors {
				if count[d] != 0 {
					t.Fatalf("donor %d still owns %d slots", d, count[d])
				}
			}
			for i, node := range c.primaries {
				want := msRedisClusterSlots / len(c.primaries)
				if i < msRedisClusterSlots%len(c.primaries) {
					want++
				}
				if count[node] != want {
					t.Fatalf("node %d owns %d slots, want %d", node, count[node], want)
				}
			}
		})
	}
}
