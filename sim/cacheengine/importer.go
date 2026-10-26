package cacheengine

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// ImportSource is one RDB file a cluster loads.
type ImportSource struct {
	Name string
	Data []byte
}

// ImportError is an import whose source the engine could not load.
type ImportError struct {
	Source string
	Err    error
}

func (e *ImportError) Error() string { return fmt.Sprintf("import %s: %v", e.Source, e.Err) }

func (e *ImportError) Unwrap() error { return e.Err }

// importDirectory holds the RDB file the import source serves.
const importDirectory = "/import"

// ImportRDB loads an RDB file's keys into a running cluster: a standalone
// redis-server placed beside the nodes loads the file, and redis-cli's
// cluster import, run from one node, migrates each key to the shard that owns
// its slot.
func (p *Plane) ImportRDB(source ImportSource) error {
	via := p.Nodes()[0]
	ip, err := p.NodeIP(via)
	if err != nil {
		return err
	}
	password := p.Password()
	from, stop, err := p.placeImportSource(source)
	if err != nil {
		return err
	}
	defer stop()
	importArgs := []string{"redis-cli", "--cluster", "import", net.JoinHostPort(ip, strconv.Itoa(Port)),
		"--cluster-from", net.JoinHostPort(from, strconv.Itoa(Port)), "--cluster-copy", "--cluster-replace"}
	var env []string
	if password != "" {
		importArgs = append(importArgs, "--cluster-from-pass", password)
		env = append(env, "REDISCLI_AUTH="+password)
	}
	if _, err := p.Exec(via, importArgs, env); err != nil {
		return &ImportError{Source: source.Name, Err: err}
	}
	return nil
}

// placeImportSource places a redis-server that serves source's RDB file on
// the engines' network, and returns its address once it has loaded the file
// and accepts clients, with the function that stops it.
func (p *Plane) placeImportSource(source ImportSource) (string, func(), error) {
	backend, err := p.cfg.Backend()
	if err != nil {
		return "", nil, err
	}
	if err := p.prepareNetwork(); err != nil {
		return "", nil, err
	}
	p.mu.RLock()
	image, network, dns, password := p.image, p.network, p.dns, p.password
	p.mu.RUnlock()
	command := []string{"redis-server",
		"--port", strconv.Itoa(Port), "--bind", "0.0.0.0", "--protected-mode", "no",
		"--dir", importDirectory, "--dbfilename", "source.rdb",
		"--save", "", "--appendonly", "no",
	}
	if password != "" {
		command = append(command, "--requirepass", password)
	}
	labels := map[string]string{p.cfg.NodeLabel + "-import": "true"}
	for key, value := range p.cfg.Labels {
		labels[key] = value
	}
	ready := make(chan struct{})
	var once sync.Once
	output := sim.FuncSink(func(line sim.LogLine) {
		if strings.Contains(strings.ToLower(line.Text), "ready to accept connections") {
			once.Do(func() { close(ready) })
		}
	})
	handle, err := backend.Place(context.Background(), placement.Workload{
		CancelGracePeriod: stopGrace,
		Image:             image,
		Architecture:      p.cfg.Platform,
		Command:           command,
		Labels:            labels,
		Network:           network,
		DNS:               dns,
		Seed:              []sim.SeedFile{{Path: importDirectory + "/source.rdb", Data: source.Data}},
		Sandbox:           p.cfg.Sandbox,
	}, output)
	if err != nil {
		return "", nil, fmt.Errorf("place the import source: %w", err)
	}
	stop := func() {
		handle.Cancel()
		_ = handle.Wait()
	}
	timer := time.NewTimer(readyBudget)
	defer timer.Stop()
	select {
	case <-ready:
	case <-handle.Exited():
		result := handle.Wait()
		return "", nil, &ImportError{Source: source.Name, Err: fmt.Errorf("the RDB file did not load: redis-server exited with status %d", result.ExitCode)}
	case <-timer.C:
		stop()
		return "", nil, fmt.Errorf("import %s: the source did not load the RDB file within %s", source.Name, readyBudget)
	}
	address, err := handle.Address(context.Background())
	if err != nil {
		stop()
		return "", nil, err
	}
	return address, stop, nil
}

// keysPerScan is the COUNT a copy scans the source's keyspace in.
const keysPerScan = 1000

// Flush deletes every key of every primary.
func (p *Plane) Flush() error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primaries, err := p.primaries()
	if err != nil {
		return err
	}
	for _, index := range primaries {
		if _, err := p.Command(index, "FLUSHALL"); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plane) primaries() ([]int, error) {
	if !p.cfg.Cluster {
		return []int{p.Primary()}, nil
	}
	primaries, _, err := p.ShardMap()
	return primaries, err
}

// CopyFrom copies every key of the Redis engine source reaches into the
// plane, each with its remaining time to live, the way a migration's data
// sync does: SCAN walks the source, DUMP reads each key and RESTORE REPLACE
// writes it into the primary whose shard owns its slot. It returns how many
// keys it copied.
func (p *Plane) CopyFrom(source *Conn) (int, error) {
	if err := p.Ensure(); err != nil {
		return 0, err
	}
	owners, err := p.slotOwners()
	if err != nil {
		return 0, err
	}
	targets := map[int]*Conn{}
	defer func() {
		for _, conn := range targets {
			_ = conn.Close()
		}
	}()
	if err := source.SetDeadline(time.Now().Add(reshardDeadline)); err != nil {
		return 0, err
	}
	copied := 0
	cursor := "0"
	for {
		reply, err := source.Do("SCAN", cursor, "COUNT", strconv.Itoa(keysPerScan))
		if err != nil {
			return copied, fmt.Errorf("scan the source: %w", err)
		}
		page, _ := reply.([]any)
		if len(page) != 2 {
			return copied, fmt.Errorf("the source answered SCAN with %v", reply)
		}
		cursor = fmt.Sprint(page[0])
		keys, _ := page[1].([]any)
		for _, item := range keys {
			key := fmt.Sprint(item)
			dumped, err := source.Do("DUMP", key)
			if err != nil {
				return copied, fmt.Errorf("dump %q from the source: %w", key, err)
			}
			if dumped == nil {
				continue
			}
			ttl, err := source.Do("PTTL", key)
			if err != nil {
				return copied, fmt.Errorf("read the time to live of %q: %w", key, err)
			}
			remaining, _ := ttl.(int64)
			if remaining < 0 {
				remaining = 0
			}
			owner := owners[KeySlot(key)]
			target, ok := targets[owner]
			if !ok {
				if target, err = p.dialNode(owner); err != nil {
					return copied, err
				}
				targets[owner] = target
			}
			if _, err := target.Do("RESTORE", key, strconv.FormatInt(remaining, 10), fmt.Sprint(dumped), "REPLACE"); err != nil {
				return copied, fmt.Errorf("restore %q: %w", key, err)
			}
			copied++
		}
		if cursor == "0" {
			return copied, nil
		}
	}
}

// slotOwners maps every hash slot to the primary that owns it; an instance's
// primary owns them all.
func (p *Plane) slotOwners() ([ClusterSlots]int, error) {
	var owners [ClusterSlots]int
	if !p.cfg.Cluster {
		primary := p.Primary()
		for slot := range owners {
			owners[slot] = primary
		}
		return owners, nil
	}
	slots, err := p.ShardSlots()
	if err != nil {
		return owners, err
	}
	for index, fields := range slots {
		list, err := SlotList(fields)
		if err != nil {
			return owners, err
		}
		for _, slot := range list {
			owners[slot] = index
		}
	}
	return owners, nil
}
