package cacheengine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ClusterSlots is the number of hash slots a Redis Cluster splits its
// keyspace into.
const ClusterSlots = 16384

// slotBatch is how many slots one pipelined round of a migration moves. Each
// slot costs a few short commands on each side, so a batch keeps both
// directions of the connection well inside the socket buffers.
const slotBatch = 512

// keysPerMigrate is the COUNT a migration lists and moves a slot's keys in.
const keysPerMigrate = 1000

// SlotRange is the hash-slot range shard s of n owns, the slots split evenly.
func SlotRange(shard, shards int) (first, last int) {
	first = shard * ClusterSlots / shards
	last = (shard+1)*ClusterSlots/shards - 1
	return first, last
}

// KeySlot is the hash slot Redis Cluster stores key in: the CRC16 of the key,
// or of its hash tag when the key has one.
func KeySlot(key string) int {
	if open := strings.IndexByte(key, '{'); open >= 0 {
		if end := strings.IndexByte(key[open+1:], '}'); end > 0 {
			key = key[open+1 : open+1+end]
		}
	}
	return int(crc16([]byte(key)) % ClusterSlots)
}

// crc16 is CRC-16/XMODEM, the checksum Redis Cluster hashes keys with.
func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

type SlotMove struct {
	From, To int
	Slots    []int
}

// balanceSlots gives every primary in primaries an equal share of the slots,
// taking them from the donors, which give up every slot, and from the
// primaries that hold more than their share. It moves each slot the way Redis
// Cluster's own resharding does: the receiver imports it, the owner migrates
// its keys with MIGRATE, and every primary learns the new owner from CLUSTER
// SETSLOT NODE. The commands of a batch of slots travel pipelined.
func (p *Plane) balanceSlots(primaries, donors []int, ids map[int]string) error {
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
		if owned[node], err = SlotList(entry.Slots); err != nil {
			return err
		}
	}
	moves := PlanSlotMoves(primaries, donors, owned)
	everyPrimary := append(append([]int(nil), primaries...), donors...)
	for _, move := range moves {
		for start := 0; start < len(move.Slots); start += slotBatch {
			batch := move.Slots[start:min(start+slotBatch, len(move.Slots))]
			if err := p.migrateSlots(move.From, move.To, batch, everyPrimary, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

// PlanSlotMoves decides which slots move where: the first
// ClusterSlots%len(primaries) primaries hold one slot more than the rest, a
// donor gives up all of its slots, and a primary over its share gives up its
// highest slots.
func PlanSlotMoves(primaries, donors []int, owned map[int][]int) []SlotMove {
	share := map[int]int{}
	for i, node := range primaries {
		share[node] = ClusterSlots / len(primaries)
		if i < ClusterSlots%len(primaries) {
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
	var moves []SlotMove
	next := 0
	for _, node := range primaries {
		for deficit := share[node] - len(owned[node]); deficit > 0 && next < len(pool); deficit-- {
			key := [2]int{pool[next].from, node}
			i, ok := index[key]
			if !ok {
				i = len(moves)
				index[key] = i
				moves = append(moves, SlotMove{From: pool[next].from, To: node})
			}
			moves[i].Slots = append(moves[i].Slots, pool[next].slot)
			next++
		}
	}
	return moves
}

// migrateSlots moves slots from one primary to another and tells every
// primary in everyPrimary the new owner.
func (p *Plane) migrateSlots(from, to int, slots, everyPrimary []int, ids map[int]string) error {
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
	targetIP, err := p.NodeIP(to)
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

	assign := make([][]string, len(slots))
	for i, slot := range slots {
		assign[i] = []string{"CLUSTER", "SETSLOT", strconv.Itoa(slot), "NODE", ids[to]}
	}
	if _, err := target.Pipeline(assign); err != nil {
		return fmt.Errorf("assign slots to node %d: %w", to, err)
	}
	if _, err := source.Pipeline(assign, setslotOnReplica); err != nil {
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
		_, err = conn.Pipeline(assign, setslotOnReplica)
		_ = conn.Close()
		if err != nil {
			return fmt.Errorf("announce slot owner to node %d: %w", node, err)
		}
	}
	return nil
}

// migrateSlotKeys moves every key of slot from the source primary to the
// primary at targetIP.
func (p *Plane) migrateSlotKeys(source *Conn, slot int, targetIP string) error {
	for {
		reply, err := source.Do("CLUSTER", "GETKEYSINSLOT", strconv.Itoa(slot), strconv.Itoa(keysPerMigrate))
		if err != nil {
			return fmt.Errorf("list keys of slot %d: %w", slot, err)
		}
		keys, _ := reply.([]any)
		if len(keys) == 0 {
			return nil
		}
		args := []string{"MIGRATE", targetIP, strconv.Itoa(Port), "", "0", strconv.Itoa(int(commandDeadline / time.Millisecond))}
		if password := p.Password(); password != "" {
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

func (p *Plane) dialNode(node int) (*Conn, error) {
	address, err := p.nodeAddress(node)
	if err != nil {
		return nil, err
	}
	conn, err := Dial(address, p.Password(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to node %d: %w", node, err)
	}
	if err := conn.SetDeadline(time.Now().Add(reshardDeadline)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// SlotList expands the slot fields of a CLUSTER NODES line ("0-5460",
// "5461") into slot numbers, skipping the bracketed importing and migrating
// markers.
func SlotList(fields []string) ([]int, error) {
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

// setslotOnReplica is the refusal of CLUSTER SETSLOT by a node that is no
// longer a primary. A primary that gives up its last slot becomes a replica
// of the slot's new owner (cluster-allow-replica-migration), so telling it the
// owner afterwards has nothing left to do; redis-cli's resharding ignores the
// refusal for the same reason.
const setslotOnReplica = "ERR Please use SETSLOT only with masters"
