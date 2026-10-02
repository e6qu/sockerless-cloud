package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// Memorystore persistence: RDB snapshots taken every rdbSnapshotPeriod,
// aligned to rdbSnapshotStartTime, and — for a cluster — an append-only file
// fsynced as appendFsync says. The engine writes both into its node's
// directory, which a restarted node loads its dataset from.

const (
	msRedisPersistenceDisabled = "DISABLED"
	msRedisPersistenceRDB      = "RDB"
	msRedisPersistenceAOF      = "AOF"
)

const (
	msRedisDefaultSnapshotPeriod = "TWENTY_FOUR_HOURS"
	msRedisDefaultAppendFsync    = "EVERYSEC"
)

var msRedisSnapshotPeriods = map[string]time.Duration{
	"ONE_HOUR":          time.Hour,
	"SIX_HOURS":         6 * time.Hour,
	"TWELVE_HOURS":      12 * time.Hour,
	"TWENTY_FOUR_HOURS": 24 * time.Hour,
}

var msRedisAppendFsyncs = map[string]string{
	"NO":       "no",
	"EVERYSEC": "everysec",
	"ALWAYS":   "always",
}

// msRedisPersistence is how an engine persists its dataset.
type msRedisPersistence struct {
	Mode        string
	Period      string
	Start       time.Time
	AppendFsync string
}

// MSRedisPersistenceConfig mirrors google.cloud.redis.v1.PersistenceConfig.
type MSRedisPersistenceConfig struct {
	PersistenceMode      string `json:"persistenceMode,omitempty"`
	RdbSnapshotPeriod    string `json:"rdbSnapshotPeriod,omitempty"`
	RdbSnapshotStartTime string `json:"rdbSnapshotStartTime,omitempty"`
	RdbNextSnapshotTime  string `json:"rdbNextSnapshotTime,omitempty"`
}

// MSRedisClusterPersistenceConfig mirrors
// google.cloud.redis.cluster.v1.ClusterPersistenceConfig.
type MSRedisClusterPersistenceConfig struct {
	Mode      string            `json:"mode,omitempty"`
	RdbConfig *MSRedisRDBConfig `json:"rdbConfig,omitempty"`
	AofConfig *MSRedisAOFConfig `json:"aofConfig,omitempty"`
}

type MSRedisRDBConfig struct {
	RdbSnapshotPeriod    string `json:"rdbSnapshotPeriod,omitempty"`
	RdbSnapshotStartTime string `json:"rdbSnapshotStartTime,omitempty"`
}

type MSRedisAOFConfig struct {
	AppendFsync string `json:"appendFsync,omitempty"`
}

func msRedisSpecified(value string) bool {
	return value != "" && !strings.HasSuffix(value, "_UNSPECIFIED")
}

// rdb fills in the snapshot schedule from period and start, keeping what
// the persistence already had for either left out.
func (p msRedisPersistence) rdb(period, start string, now time.Time) (msRedisPersistence, error) {
	if msRedisSpecified(period) {
		if _, ok := msRedisSnapshotPeriods[period]; !ok {
			return p, fmt.Errorf("rdbSnapshotPeriod %q is not one of ONE_HOUR, SIX_HOURS, TWELVE_HOURS or TWENTY_FOUR_HOURS", period)
		}
		p.Period = period
	}
	if p.Period == "" {
		p.Period = msRedisDefaultSnapshotPeriod
	}
	if start != "" {
		parsed, err := time.Parse(time.RFC3339Nano, start)
		if err != nil {
			return p, fmt.Errorf("rdbSnapshotStartTime %q is not an RFC 3339 timestamp", start)
		}
		p.Start = parsed.UTC()
	}
	if p.Start.IsZero() {
		p.Start = now.UTC()
	}
	return p, nil
}

// msRedisInstancePersistence applies an instance's requested persistence
// config to current. A mode left out keeps the existing one.
func msRedisInstancePersistence(current msRedisPersistence, req *MSRedisPersistenceConfig, now time.Time) (msRedisPersistence, error) {
	next := current
	if next.Mode == "" {
		next.Mode = msRedisPersistenceDisabled
	}
	if req == nil {
		return next, nil
	}
	if msRedisSpecified(req.PersistenceMode) {
		switch req.PersistenceMode {
		case msRedisPersistenceDisabled, msRedisPersistenceRDB:
			next.Mode = req.PersistenceMode
		default:
			return current, fmt.Errorf("persistenceMode %q is not one of DISABLED or RDB", req.PersistenceMode)
		}
	}
	if next.Mode != msRedisPersistenceRDB {
		return next, nil
	}
	return next.rdb(req.RdbSnapshotPeriod, req.RdbSnapshotStartTime, now)
}

// msRedisClusterPersistence applies a cluster's requested persistence config
// to current.
func msRedisClusterPersistence(current msRedisPersistence, req *MSRedisClusterPersistenceConfig, now time.Time) (msRedisPersistence, error) {
	next := current
	if next.Mode == "" {
		next.Mode = msRedisPersistenceDisabled
	}
	if req == nil {
		return next, nil
	}
	if msRedisSpecified(req.Mode) {
		switch req.Mode {
		case msRedisPersistenceDisabled, msRedisPersistenceRDB, msRedisPersistenceAOF:
			next.Mode = req.Mode
		default:
			return current, fmt.Errorf("persistenceConfig.mode %q is not one of DISABLED, RDB or AOF", req.Mode)
		}
	}
	switch next.Mode {
	case msRedisPersistenceRDB:
		var period, start string
		if req.RdbConfig != nil {
			period, start = req.RdbConfig.RdbSnapshotPeriod, req.RdbConfig.RdbSnapshotStartTime
		}
		return next.rdb(period, start, now)
	case msRedisPersistenceAOF:
		if req.AofConfig != nil && msRedisSpecified(req.AofConfig.AppendFsync) {
			if _, ok := msRedisAppendFsyncs[req.AofConfig.AppendFsync]; !ok {
				return current, fmt.Errorf("appendFsync %q is not one of NO, EVERYSEC or ALWAYS", req.AofConfig.AppendFsync)
			}
			next.AppendFsync = req.AofConfig.AppendFsync
		}
		if next.AppendFsync == "" {
			next.AppendFsync = msRedisDefaultAppendFsync
		}
	}
	return next, nil
}

// msRedisPersistenceOf reads back a persistence a resource recorded.
func msRedisPersistenceOfInstance(config *MSRedisPersistenceConfig) msRedisPersistence {
	persistence, _ := msRedisInstancePersistence(msRedisPersistence{}, config, time.Now())
	return persistence
}

func msRedisPersistenceOfCluster(config *MSRedisClusterPersistenceConfig) msRedisPersistence {
	persistence, _ := msRedisClusterPersistence(msRedisPersistence{}, config, time.Now())
	return persistence
}

// instanceConfig is the persistence an instance reports.
func (p msRedisPersistence) instanceConfig(now time.Time) *MSRedisPersistenceConfig {
	if p.Mode != msRedisPersistenceRDB {
		return &MSRedisPersistenceConfig{PersistenceMode: msRedisPersistenceDisabled}
	}
	return &MSRedisPersistenceConfig{
		PersistenceMode:      msRedisPersistenceRDB,
		RdbSnapshotPeriod:    p.Period,
		RdbSnapshotStartTime: p.Start.Format(time.RFC3339),
		RdbNextSnapshotTime:  p.nextSnapshot(now).Format(time.RFC3339),
	}
}

// clusterConfig is the persistence a cluster reports.
func (p msRedisPersistence) clusterConfig() *MSRedisClusterPersistenceConfig {
	switch p.Mode {
	case msRedisPersistenceRDB:
		return &MSRedisClusterPersistenceConfig{Mode: p.Mode, RdbConfig: &MSRedisRDBConfig{
			RdbSnapshotPeriod: p.Period, RdbSnapshotStartTime: p.Start.Format(time.RFC3339),
		}}
	case msRedisPersistenceAOF:
		return &MSRedisClusterPersistenceConfig{Mode: p.Mode, AofConfig: &MSRedisAOFConfig{AppendFsync: p.AppendFsync}}
	}
	return &MSRedisClusterPersistenceConfig{Mode: msRedisPersistenceDisabled}
}

// nextSnapshot is the first snapshot time after now: the start time itself
// while it is still ahead, and otherwise the next whole period after it.
func (p msRedisPersistence) nextSnapshot(now time.Time) time.Time {
	if now.Before(p.Start) {
		return p.Start
	}
	period := msRedisSnapshotPeriods[p.Period]
	elapsed := now.Sub(p.Start)
	return p.Start.Add((elapsed/period + 1) * period)
}

// engineArgs are the redis-server directives the persistence runs with. RDB
// snapshots are the service's own schedule, not the engine's save points.
func (p msRedisPersistence) engineArgs() []string {
	if p.Mode != msRedisPersistenceAOF {
		return []string{"--appendonly", "no"}
	}
	return []string{"--appendonly", "yes", "--appendfsync", msRedisAppendFsyncs[p.AppendFsync]}
}

// scheduleSnapshots arms the timer for the next RDB snapshot.
func (p *msRedisPlane) scheduleSnapshots() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshots != nil {
		p.snapshots.Stop()
		p.snapshots = nil
	}
	if p.closed || p.persistence.Mode != msRedisPersistenceRDB {
		return
	}
	next := p.persistence.nextSnapshot(time.Now())
	p.snapshots = time.AfterFunc(time.Until(next), p.takeScheduledSnapshot)
}

func (p *msRedisPlane) takeScheduledSnapshot() {
	if p.running() {
		p.opMu.Lock()
		err := p.backgroundSave()
		p.opMu.Unlock()
		if err != nil {
			log.Printf("Memorystore %s: RDB snapshot: %v", p.name, err)
		}
	}
	p.scheduleSnapshots()
}

// backgroundSave has every primary write its RDB snapshot.
func (p *msRedisPlane) backgroundSave() error {
	primaries := []int{p.Primary()}
	if p.cluster {
		var err error
		if primaries, _, err = p.shardMap(); err != nil {
			return err
		}
	}
	for _, node := range primaries {
		if _, err := p.command(node, "BGSAVE"); err != nil {
			return err
		}
	}
	return nil
}

// SetPersistence changes how the engine persists, live: the append-only file
// starts or stops at once and the snapshot schedule moves.
func (p *msRedisPlane) SetPersistence(next msRedisPersistence) error {
	p.mu.Lock()
	previous := p.persistence
	p.persistence = next
	p.mu.Unlock()
	if p.running() {
		switch {
		case next.Mode == msRedisPersistenceAOF:
			if err := p.everyNode("CONFIG", "SET", "appendfsync", msRedisAppendFsyncs[next.AppendFsync]); err != nil {
				return err
			}
			if err := p.everyNode("CONFIG", "SET", "appendonly", "yes"); err != nil {
				return err
			}
		case previous.Mode == msRedisPersistenceAOF:
			if err := p.everyNode("CONFIG", "SET", "appendonly", "no"); err != nil {
				return err
			}
		}
	}
	p.scheduleSnapshots()
	return nil
}
