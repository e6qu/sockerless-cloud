package main

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// msRedisSlotBatch is how many slots one pipelined round of a migration
// moves. Each slot costs a few short commands on each side, so a batch keeps
// both directions of the connection well inside the socket buffers.
const msRedisSlotBatch = 512

// msRedisKeysPerMigrate is the COUNT a slot's keys are listed and migrated in.
const msRedisKeysPerMigrate = 1000

// msRedisSlotMove moves a set of slots from one primary to another.
type msRedisSlotMove struct {
	from, to int
	slots    []int
}

// balanceSlots gives every primary in primaries an equal share of the slots,
// taking them from the donors, which give up every slot, and from the
// primaries that hold more than their share. It moves each slot the way Redis
// Cluster's own resharding does: the receiver imports it, the owner migrates
// its keys with MIGRATE, and every primary learns the new owner from CLUSTER
// SETSLOT NODE. The commands of a batch of slots travel pipelined.
func (p *msRedisPlane) balanceSlots(primaries, donors []int, ids map[int]string) error {
	view, err := p.clusterView(primaries[0])
	if err != nil {
		return err
	}
	owned := map[int][]int{}
	for _, node := range append(append([]int(nil), primaries...), donors...) {
		entry, ok := view.byID(ids[node])
		if !ok {
			return fmt.Errorf("node %d (%s) is not in the cluster's view", node, ids[node])
		}
		if owned[node], err = msRedisSlotList(entry.Slots); err != nil {
			return err
		}
	}
	moves := msRedisPlanSlotMoves(primaries, donors, owned)
	everyPrimary := append(append([]int(nil), primaries...), donors...)
	for _, move := range moves {
		for start := 0; start < len(move.slots); start += msRedisSlotBatch {
			batch := move.slots[start:min(start+msRedisSlotBatch, len(move.slots))]
			if err := p.migrateSlots(move.from, move.to, batch, everyPrimary, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

// msRedisPlanSlotMoves decides which slots move where: the first
// msRedisClusterSlots%len(primaries) primaries hold one slot more than the
// rest, a donor gives up all of its slots, and a primary over its share gives
// up its highest slots.
func msRedisPlanSlotMoves(primaries, donors []int, owned map[int][]int) []msRedisSlotMove {
	share := map[int]int{}
	for i, node := range primaries {
		share[node] = msRedisClusterSlots / len(primaries)
		if i < msRedisClusterSlots%len(primaries) {
			share[node]++
		}
	}
	type freed struct{ from, slot int }
	var pool []freed
	for _, node := range donors {
		for _, slot := range owned[node] {
			pool = append(pool, freed{node, slot})
		}
	}
	for _, node := range primaries {
		if excess := len(owned[node]) - share[node]; excess > 0 {
			for _, slot := range owned[node][len(owned[node])-excess:] {
				pool = append(pool, freed{node, slot})
			}
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].slot < pool[j].slot })

	index := map[[2]int]int{}
	var moves []msRedisSlotMove
	next := 0
	for _, node := range primaries {
		for deficit := share[node] - len(owned[node]); deficit > 0 && next < len(pool); deficit-- {
			key := [2]int{pool[next].from, node}
			i, ok := index[key]
			if !ok {
				i = len(moves)
				index[key] = i
				moves = append(moves, msRedisSlotMove{from: pool[next].from, to: node})
			}
			moves[i].slots = append(moves[i].slots, pool[next].slot)
			next++
		}
	}
	return moves
}

// migrateSlots moves slots from one primary to another and tells every
// primary in everyPrimary the new owner.
func (p *msRedisPlane) migrateSlots(from, to int, slots, everyPrimary []int, ids map[int]string) error {
	source, err := p.dialNode(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := p.dialNode(to)
	if err != nil {
		return err
	}
	defer target.Close()
	targetIP, err := p.nodeIP(to)
	if err != nil {
		return err
	}

	importing := make([][]string, len(slots))
	migrating := make([][]string, len(slots))
	counting := make([][]string, len(slots))
	for i, slot := range slots {
		s := strconv.Itoa(slot)
		importing[i] = []string{"CLUSTER", "SETSLOT", s, "IMPORTING", ids[from]}
		migrating[i] = []string{"CLUSTER", "SETSLOT", s, "MIGRATING", ids[to]}
		counting[i] = []string{"CLUSTER", "COUNTKEYSINSLOT", s}
	}
	if _, err := target.Pipeline(importing); err != nil {
		return fmt.Errorf("import slots on node %d: %w", to, err)
	}
	if _, err := source.Pipeline(migrating); err != nil {
		return fmt.Errorf("migrate slots from node %d: %w", from, err)
	}
	counts, err := source.Pipeline(counting)
	if err != nil {
		return fmt.Errorf("count keys on node %d: %w", from, err)
	}
	for i, slot := range slots {
		if count, _ := counts[i].(int64); count > 0 {
			if err := p.migrateSlotKeys(source, slot, targetIP); err != nil {
				return err
			}
		}
	}

	owner := func(slot int) []string {
		return []string{"CLUSTER", "SETSLOT", strconv.Itoa(slot), "NODE", ids[to]}
	}
	assign := make([][]string, len(slots))
	for i, slot := range slots {
		assign[i] = owner(slot)
	}
	if _, err := target.Pipeline(assign); err != nil {
		return fmt.Errorf("assign slots to node %d: %w", to, err)
	}
	if _, err := source.Pipeline(assign, msRedisSetslotOnReplica); err != nil {
		return fmt.Errorf("release slots on node %d: %w", from, err)
	}
	for _, node := range everyPrimary {
		if node == from || node == to {
			continue
		}
		conn, err := p.dialNode(node)
		if err != nil {
			return err
		}
		_, err = conn.Pipeline(assign, msRedisSetslotOnReplica)
		_ = conn.Close()
		if err != nil {
			return fmt.Errorf("announce slot owner to node %d: %w", node, err)
		}
	}
	return nil
}

// migrateSlotKeys moves every key of slot from the source primary to the
// primary at targetIP.
func (p *msRedisPlane) migrateSlotKeys(source *msRedisConn, slot int, targetIP string) error {
	for {
		reply, err := source.Do("CLUSTER", "GETKEYSINSLOT", strconv.Itoa(slot), strconv.Itoa(msRedisKeysPerMigrate))
		if err != nil {
			return fmt.Errorf("list keys of slot %d: %w", slot, err)
		}
		keys, _ := reply.([]any)
		if len(keys) == 0 {
			return nil
		}
		args := []string{"MIGRATE", targetIP, strconv.Itoa(msRedisPort), "", "0", strconv.Itoa(int(msRedisCommandDeadline / time.Millisecond))}
		if password := p.currentPassword(); password != "" {
			args = append(args, "AUTH", password)
		}
		args = append(args, "KEYS")
		for _, key := range keys {
			args = append(args, fmt.Sprint(key))
		}
		if _, err := source.Do(args...); err != nil {
			return fmt.Errorf("migrate keys of slot %d: %w", slot, err)
		}
	}
}

func (p *msRedisPlane) dialNode(node int) (*msRedisConn, error) {
	address, err := p.nodeAddress(node)
	if err != nil {
		return nil, err
	}
	conn, err := msRedisDial(address, p.currentPassword(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to node %d: %w", node, err)
	}
	if err := conn.SetDeadline(time.Now().Add(msRedisReshardDeadline)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// msRedisSlotList expands the slot fields of a CLUSTER NODES line ("0-5460",
// "5461") into slot numbers, skipping the bracketed importing and migrating
// markers.
func msRedisSlotList(fields []string) ([]int, error) {
	var slots []int
	for _, field := range fields {
		if strings.HasPrefix(field, "[") {
			continue
		}
		first, last, ranged := strings.Cut(field, "-")
		low, err := strconv.Atoi(first)
		if err != nil {
			return nil, fmt.Errorf("slot range %q: %w", field, err)
		}
		high := low
		if ranged {
			if high, err = strconv.Atoi(last); err != nil {
				return nil, fmt.Errorf("slot range %q: %w", field, err)
			}
		}
		for slot := low; slot <= high; slot++ {
			slots = append(slots, slot)
		}
	}
	sort.Ints(slots)
	return slots, nil
}

// msRedisSetslotOnReplica is the refusal of CLUSTER SETSLOT by a node that is
// no longer a primary. A primary that gives up its last slot becomes a replica
// of the slot's new owner (cluster-allow-replica-migration), so telling it the
// owner afterwards has nothing left to do; redis-cli's resharding ignores the
// refusal for the same reason.
const msRedisSetslotOnReplica = "ERR Please use SETSLOT only with masters"

// Pipeline sends every command before reading any reply and returns the
// replies in order. An error reply fails the call once every reply is read,
// so the connection stays in step, unless it starts with one of tolerated.
func (c *msRedisConn) Pipeline(commands [][]string, tolerated ...string) ([]any, error) {
	var b bytes.Buffer
	for _, args := range commands {
		b.Write(msRedisEncodeCommand(args))
	}
	if _, err := c.Write(b.Bytes()); err != nil {
		return nil, err
	}
	replies := make([]any, len(commands))
	var failed error
	for i, args := range commands {
		reply, err := msRedisReadReply(c.reader)
		var refused msRedisError
		switch {
		case errors.As(err, &refused):
			if failed == nil && !msRedisToleratedRefusal(refused, tolerated) {
				failed = fmt.Errorf("%s: %w", strings.Join(args, " "), err)
			}
		case err != nil:
			return nil, err
		default:
			replies[i] = reply
		}
	}
	return replies, failed
}

func msRedisToleratedRefusal(refused msRedisError, tolerated []string) bool {
	for _, prefix := range tolerated {
		if strings.HasPrefix(string(refused), prefix) {
			return true
		}
	}
	return false
}
