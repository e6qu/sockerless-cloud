package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// The Performance Insights counter metrics the User Guide's "Detailed
// Database Metrics" lists, each read from the engine or the host it runs on
// once a minute. A cumulative counter becomes a rate between two readings.

type piCounterKind uint8

const (
	piGauge piCounterKind = iota
	piPerSecond
	piPerMinute
	piCPUPercent
	piRatio
)

type piCounter struct {
	Metric      string
	Family      string // "os", "postgres" or "mysql"
	Source      string
	Divisor     string // piRatio: Δsource / Δdivisor
	Kind        piCounterKind
	Unit        string
	Description string
}

var piCounters = []piCounter{
	{Metric: "os.cpuUtilization.user", Family: "os", Source: "cpu.user", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU in use by user programs."},
	{Metric: "os.cpuUtilization.system", Family: "os", Source: "cpu.system", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU in use by the kernel."},
	{Metric: "os.cpuUtilization.nice", Family: "os", Source: "cpu.nice", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU in use by programs running at lowest priority."},
	{Metric: "os.cpuUtilization.idle", Family: "os", Source: "cpu.idle", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU that is idle."},
	{Metric: "os.cpuUtilization.wait", Family: "os", Source: "cpu.iowait", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU unused while waiting for I/O access."},
	{Metric: "os.cpuUtilization.irq", Family: "os", Source: "cpu.softirq", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU in use by software interrupts."},
	{Metric: "os.cpuUtilization.steal", Family: "os", Source: "cpu.steal", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU in use by other virtual machines."},
	{Metric: "os.cpuUtilization.guest", Family: "os", Source: "cpu.guest", Kind: piCPUPercent, Unit: "Percentage", Description: "The percentage of CPU in use by guest programs."},
	{Metric: "os.cpuUtilization.total", Family: "os", Source: "cpu.busy", Kind: piCPUPercent, Unit: "Percentage", Description: "The total percentage of the CPU in use. This value includes the nice value."},
	{Metric: "os.memory.active", Family: "os", Source: "mem.Active", Unit: "Kilobytes", Description: "The amount of assigned memory, in kilobytes."},
	{Metric: "os.memory.buffers", Family: "os", Source: "mem.Buffers", Unit: "Kilobytes", Description: "The amount of memory used for buffering I/O requests prior to writing to the storage device, in kilobytes."},
	{Metric: "os.memory.cached", Family: "os", Source: "mem.Cached", Unit: "Kilobytes", Description: "The amount of memory used for caching file system–based I/O, in kilobytes."},
	{Metric: "os.memory.dirty", Family: "os", Source: "mem.Dirty", Unit: "Kilobytes", Description: "The amount of memory pages in RAM that have been modified but not written to their related data block in storage, in kilobytes."},
	{Metric: "os.memory.free", Family: "os", Source: "mem.MemFree", Unit: "Kilobytes", Description: "The amount of unassigned memory, in kilobytes."},
	{Metric: "os.memory.hugePagesFree", Family: "os", Source: "mem.HugePages_Free", Unit: "Pages", Description: "The number of free huge pages. Huge pages are a feature of the Linux kernel."},
	{Metric: "os.memory.hugePagesRsvd", Family: "os", Source: "mem.HugePages_Rsvd", Unit: "Pages", Description: "The number of committed huge pages."},
	{Metric: "os.memory.hugePagesSize", Family: "os", Source: "mem.Hugepagesize", Unit: "Kilobytes", Description: "The size for each huge pages unit, in kilobytes."},
	{Metric: "os.memory.hugePagesSurp", Family: "os", Source: "mem.HugePages_Surp", Unit: "Pages", Description: "The number of available surplus huge pages over the total."},
	{Metric: "os.memory.hugePagesTotal", Family: "os", Source: "mem.HugePages_Total", Unit: "Pages", Description: "The total number of huge pages."},
	{Metric: "os.memory.inactive", Family: "os", Source: "mem.Inactive", Unit: "Kilobytes", Description: "The amount of least-frequently used memory pages, in kilobytes."},
	{Metric: "os.memory.mapped", Family: "os", Source: "mem.Mapped", Unit: "Kilobytes", Description: "The total amount of file-system contents that is memory mapped inside a process address space, in kilobytes."},
	{Metric: "os.memory.pageTables", Family: "os", Source: "mem.PageTables", Unit: "Kilobytes", Description: "The amount of memory used by page tables, in kilobytes."},
	{Metric: "os.memory.slab", Family: "os", Source: "mem.Slab", Unit: "Kilobytes", Description: "The amount of reusable kernel data structures, in kilobytes."},
	{Metric: "os.memory.total", Family: "os", Source: "mem.MemTotal", Unit: "Kilobytes", Description: "The total amount of memory, in kilobytes."},
	{Metric: "os.memory.writeback", Family: "os", Source: "mem.Writeback", Unit: "Kilobytes", Description: "The amount of dirty pages in RAM that are still being written to the backing storage, in kilobytes."},
	{Metric: "os.swap.cached", Family: "os", Source: "mem.SwapCached", Unit: "Kilobytes", Description: "The amount of swap memory, in kilobytes, used as cache memory."},
	{Metric: "os.swap.free", Family: "os", Source: "mem.SwapFree", Unit: "Kilobytes", Description: "The amount of swap memory free, in kilobytes."},
	{Metric: "os.swap.total", Family: "os", Source: "mem.SwapTotal", Unit: "Kilobytes", Description: "The total amount of swap memory available in kilobytes."},
	{Metric: "os.loadAverageMinute.one", Family: "os", Source: "load.one", Unit: "Processes", Description: "The number of processes requesting CPU time over the last minute."},
	{Metric: "os.loadAverageMinute.five", Family: "os", Source: "load.five", Unit: "Processes", Description: "The number of processes requesting CPU time over the last 5 minutes."},
	{Metric: "os.loadAverageMinute.fifteen", Family: "os", Source: "load.fifteen", Unit: "Processes", Description: "The number of processes requesting CPU time over the last 15 minutes."},
	{Metric: "os.tasks.running", Family: "os", Source: "tasks.running", Unit: "Tasks", Description: "The number of tasks that are running."},
	{Metric: "os.tasks.blocked", Family: "os", Source: "tasks.blocked", Unit: "Tasks", Description: "The number of tasks that are blocked."},
	{Metric: "os.tasks.total", Family: "os", Source: "tasks.total", Unit: "Tasks", Description: "The total number of tasks."},

	{Metric: "db.Cache.blks_hit", Family: "postgres", Source: "blks_hit", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.Cache.buffers_alloc", Family: "postgres", Source: "buffers_alloc", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.Checkpoint.buffers_checkpoint", Family: "postgres", Source: "buffers_checkpoint", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.Checkpoint.checkpoint_sync_time", Family: "postgres", Source: "checkpoint_sync_time", Divisor: "checkpoints", Kind: piRatio, Unit: "Milliseconds per checkpoint"},
	{Metric: "db.Checkpoint.checkpoint_write_time", Family: "postgres", Source: "checkpoint_write_time", Divisor: "checkpoints", Kind: piRatio, Unit: "Milliseconds per checkpoint"},
	{Metric: "db.Checkpoint.checkpoints_req", Family: "postgres", Source: "checkpoints_req", Kind: piPerMinute, Unit: "Checkpoints per minute"},
	{Metric: "db.Checkpoint.checkpoints_timed", Family: "postgres", Source: "checkpoints_timed", Kind: piPerMinute, Unit: "Checkpoints per minute"},
	{Metric: "db.Checkpoint.maxwritten_clean", Family: "postgres", Source: "maxwritten_clean", Kind: piPerMinute, Unit: "Bgwriter clean stops per minute"},
	{Metric: "db.Concurrency.deadlocks", Family: "postgres", Source: "deadlocks", Kind: piPerMinute, Unit: "Deadlocks per minute"},
	{Metric: "db.IO.blk_read_time", Family: "postgres", Source: "blk_read_time", Unit: "Milliseconds"},
	{Metric: "db.IO.blks_read", Family: "postgres", Source: "blks_read", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.IO.buffers_backend", Family: "postgres", Source: "buffers_backend", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.IO.buffers_backend_fsync", Family: "postgres", Source: "buffers_backend_fsync", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.IO.buffers_clean", Family: "postgres", Source: "buffers_clean", Kind: piPerSecond, Unit: "Blocks per second"},
	{Metric: "db.SQL.tup_deleted", Family: "postgres", Source: "tup_deleted", Kind: piPerSecond, Unit: "Tuples per second"},
	{Metric: "db.SQL.tup_fetched", Family: "postgres", Source: "tup_fetched", Kind: piPerSecond, Unit: "Tuples per second"},
	{Metric: "db.SQL.tup_inserted", Family: "postgres", Source: "tup_inserted", Kind: piPerSecond, Unit: "Tuples per second"},
	{Metric: "db.SQL.tup_returned", Family: "postgres", Source: "tup_returned", Kind: piPerSecond, Unit: "Tuples per second"},
	{Metric: "db.SQL.tup_updated", Family: "postgres", Source: "tup_updated", Kind: piPerSecond, Unit: "Tuples per second"},
	{Metric: "db.Temp.temp_bytes", Family: "postgres", Source: "temp_bytes", Kind: piPerSecond, Unit: "Bytes per second"},
	{Metric: "db.Temp.temp_files", Family: "postgres", Source: "temp_files", Kind: piPerMinute, Unit: "Files per minute"},
	{Metric: "db.Transactions.xact_commit", Family: "postgres", Source: "xact_commit", Kind: piPerSecond, Unit: "Commits per second"},
	{Metric: "db.Transactions.xact_rollback", Family: "postgres", Source: "xact_rollback", Kind: piPerSecond, Unit: "Rollbacks per second"},
	{Metric: "db.User.numbackends", Family: "postgres", Source: "numbackends", Unit: "Connections"},
	{Metric: "db.WAL.archived_count", Family: "postgres", Source: "archived_count", Kind: piPerMinute, Unit: "Files per minute"},
	{Metric: "db.WAL.archive_failed_count", Family: "postgres", Source: "failed_count", Kind: piPerMinute, Unit: "Files per minute", Description: "The number of failed attempts for archiving WAL files, in files per minute."},
	{Metric: "db.IO.read_latency", Family: "postgres", Source: "blk_read_time", Divisor: "blks_read", Kind: piRatio, Unit: "Milliseconds", Description: "The time spent reading data file blocks by backends in this instance."},
	{Metric: "db.state.idle_in_transaction_aborted_count", Family: "postgres", Source: "idle_in_transaction_aborted_count", Unit: "Sessions", Description: "The number of sessions in the idle in transaction (aborted) state."},
	{Metric: "db.state.idle_in_transaction_count", Family: "postgres", Source: "idle_in_transaction_count", Unit: "Sessions", Description: "The number of sessions in the idle in transaction state."},
	{Metric: "db.state.idle_in_transaction_max_time", Family: "postgres", Source: "idle_in_transaction_max_time", Unit: "Seconds", Description: "The duration of the longest running transaction in the idle in transaction state, in seconds."},
	{Metric: "db.Transactions.active_transactions", Family: "postgres", Source: "active_transactions", Unit: "Transactions", Description: "The number of active transactions."},
	{Metric: "db.Transactions.blocked_transactions", Family: "postgres", Source: "blocked_transactions", Unit: "Transactions", Description: "The number of blocked transactions."},
	{Metric: "db.Transactions.max_used_xact_ids", Family: "postgres", Source: "max_used_xact_ids", Unit: "Transactions", Description: "The number of transactions that haven't been vacuumed."},
	{Metric: "db.User.max_connections", Family: "postgres", Source: "max_connections", Unit: "Connections", Description: "The maximum number of connections allowed for a DB instance as configured in max_connections parameter."},

	{Metric: "db.SQL.Com_analyze", Family: "mysql", Source: "Com_analyze", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Com_optimize", Family: "mysql", Source: "Com_optimize", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Com_select", Family: "mysql", Source: "Com_select", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.Users.Connections", Family: "mysql", Source: "Connections", Kind: piPerMinute, Unit: "The number of connection attempts per minute (successful or not) to the MySQL server"},
	{Metric: "db.SQL.Innodb_rows_deleted", Family: "mysql", Source: "Innodb_rows_deleted", Kind: piPerSecond, Unit: "Rows per second"},
	{Metric: "db.SQL.Innodb_rows_inserted", Family: "mysql", Source: "Innodb_rows_inserted", Kind: piPerSecond, Unit: "Rows per second"},
	{Metric: "db.SQL.Innodb_rows_read", Family: "mysql", Source: "Innodb_rows_read", Kind: piPerSecond, Unit: "Rows per second"},
	{Metric: "db.SQL.Innodb_rows_updated", Family: "mysql", Source: "Innodb_rows_updated", Kind: piPerSecond, Unit: "Rows per second"},
	{Metric: "db.SQL.Select_full_join", Family: "mysql", Source: "Select_full_join", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Select_full_range_join", Family: "mysql", Source: "Select_full_range_join", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Select_range", Family: "mysql", Source: "Select_range", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Select_range_check", Family: "mysql", Source: "Select_range_check", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Select_scan", Family: "mysql", Source: "Select_scan", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Slow_queries", Family: "mysql", Source: "Slow_queries", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Sort_merge_passes", Family: "mysql", Source: "Sort_merge_passes", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Sort_range", Family: "mysql", Source: "Sort_range", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Sort_rows", Family: "mysql", Source: "Sort_rows", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Sort_scan", Family: "mysql", Source: "Sort_scan", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.SQL.Questions", Family: "mysql", Source: "Questions", Kind: piPerSecond, Unit: "Queries per second"},
	{Metric: "db.Locks.Innodb_row_lock_time", Family: "mysql", Source: "Innodb_row_lock_time", Divisor: "Innodb_row_lock_waits", Kind: piRatio, Unit: "Milliseconds (average)"},
	{Metric: "db.Locks.Table_locks_immediate", Family: "mysql", Source: "Table_locks_immediate", Kind: piPerSecond, Unit: "Requests per second"},
	{Metric: "db.Locks.Table_locks_waited", Family: "mysql", Source: "Table_locks_waited", Kind: piPerSecond, Unit: "Requests per second"},
	{Metric: "db.Users.Aborted_clients", Family: "mysql", Source: "Aborted_clients", Unit: "Connections"},
	{Metric: "db.Users.Aborted_connects", Family: "mysql", Source: "Aborted_connects", Unit: "Connections"},
	{Metric: "db.User.max_connections", Family: "mysql", Source: "max_connections", Unit: "Connections"},
	{Metric: "db.Users.Threads_created", Family: "mysql", Source: "Threads_created", Unit: "Connections"},
	{Metric: "db.Users.Threads_running", Family: "mysql", Source: "Threads_running", Unit: "Connections"},
	{Metric: "db.IO.Innodb_data_writes", Family: "mysql", Source: "Innodb_data_writes", Kind: piPerSecond, Unit: "Operations per second"},
	{Metric: "db.IO.Innodb_dblwr_writes", Family: "mysql", Source: "Innodb_dblwr_writes", Kind: piPerSecond, Unit: "Operations per second"},
	{Metric: "db.IO.Innodb_log_write_requests", Family: "mysql", Source: "Innodb_log_write_requests", Kind: piPerSecond, Unit: "Operations per second"},
	{Metric: "db.IO.Innodb_log_writes", Family: "mysql", Source: "Innodb_log_writes", Kind: piPerSecond, Unit: "Operations per second"},
	{Metric: "db.IO.Innodb_pages_written", Family: "mysql", Source: "Innodb_pages_written", Kind: piPerSecond, Unit: "Pages per second"},
	{Metric: "db.Temp.Created_tmp_disk_tables", Family: "mysql", Source: "Created_tmp_disk_tables", Kind: piPerSecond, Unit: "Tables per second"},
	{Metric: "db.Temp.Created_tmp_tables", Family: "mysql", Source: "Created_tmp_tables", Kind: piPerSecond, Unit: "Tables per second"},
	{Metric: "db.Cache.Innodb_buffer_pool_pages_data", Family: "mysql", Source: "Innodb_buffer_pool_pages_data", Unit: "Pages"},
	{Metric: "db.Cache.Innodb_buffer_pool_pages_total", Family: "mysql", Source: "Innodb_buffer_pool_pages_total", Unit: "Pages"},
	{Metric: "db.Cache.Innodb_buffer_pool_read_requests", Family: "mysql", Source: "Innodb_buffer_pool_read_requests", Kind: piPerSecond, Unit: "Pages per second"},
	{Metric: "db.Cache.Innodb_buffer_pool_reads", Family: "mysql", Source: "Innodb_buffer_pool_reads", Kind: piPerSecond, Unit: "Pages per second"},
	{Metric: "db.Cache.Opened_tables", Family: "mysql", Source: "Opened_tables", Unit: "Tables"},
	{Metric: "db.Cache.Opened_table_definitions", Family: "mysql", Source: "Opened_table_definitions", Unit: "Tables"},
	{Metric: "db.Cache.innoDB_buffer_pool_hits", Family: "mysql", Source: "innodb_buffer_pool_hits", Unit: "Reads", Description: "The number of reads that InnoDB could satisfy from the buffer pool."},
	{Metric: "db.Cache.innoDB_buffer_pool_hit_rate", Family: "mysql", Source: "innodb_buffer_pool_hit_rate", Unit: "Percentage", Description: "The percentage of reads that InnoDB could satisfy from the buffer pool."},
	{Metric: "db.Cache.innoDB_buffer_pool_usage", Family: "mysql", Source: "innodb_buffer_pool_usage", Unit: "Percentage", Description: "The percentage of the InnoDB buffer pool that contains data (pages)."},
	{Metric: "db.SQL.innodb_rows_changed", Family: "mysql", Source: "innodb_rows_changed", Unit: "Rows", Description: "The total InnoDB row operations."},
	{Metric: "db.Transactions.active_transactions", Family: "mysql", Source: "active_transactions", Unit: "Transactions", Description: "The total active transactions."},
	{Metric: "db.Locks.innodb_deadlocks", Family: "mysql", Source: "lock_deadlocks", Unit: "Locks", Description: "The total number of deadlocks."},
	{Metric: "db.Locks.innodb_lock_timeouts", Family: "mysql", Source: "lock_timeouts", Unit: "Locks", Description: "The total number of locks that timed out."},
	{Metric: "db.Locks.innodb_row_lock_waits", Family: "mysql", Source: "lock_row_lock_waits", Unit: "Locks", Description: "The total number of row locks that resulted in a wait."},
}

func piCountersFor(family dbengine.Family) []piCounter {
	var out []piCounter
	for _, c := range piCounters {
		if c.Family == "os" || c.Family == string(family) {
			out = append(out, c)
		}
	}
	return out
}

func piFindCounter(family dbengine.Family, metric string) (piCounter, bool) {
	for _, c := range piCountersFor(family) {
		if c.Metric == metric {
			return c, true
		}
	}
	return piCounter{}, false
}

const piPostgresCountersQuery = `SELECT json_build_object(
 'blks_hit', (SELECT sum(blks_hit) FROM pg_stat_database), 'blks_read', (SELECT sum(blks_read) FROM pg_stat_database),
 'tup_deleted', (SELECT sum(tup_deleted) FROM pg_stat_database), 'tup_fetched', (SELECT sum(tup_fetched) FROM pg_stat_database),
 'tup_inserted', (SELECT sum(tup_inserted) FROM pg_stat_database), 'tup_returned', (SELECT sum(tup_returned) FROM pg_stat_database),
 'tup_updated', (SELECT sum(tup_updated) FROM pg_stat_database), 'temp_bytes', (SELECT sum(temp_bytes) FROM pg_stat_database),
 'temp_files', (SELECT sum(temp_files) FROM pg_stat_database), 'xact_commit', (SELECT sum(xact_commit) FROM pg_stat_database),
 'xact_rollback', (SELECT sum(xact_rollback) FROM pg_stat_database), 'deadlocks', (SELECT sum(deadlocks) FROM pg_stat_database),
 'blk_read_time', (SELECT sum(blk_read_time) FROM pg_stat_database), 'numbackends', (SELECT sum(numbackends) FROM pg_stat_database),
 'bgwriter', (SELECT to_jsonb(b) FROM pg_stat_bgwriter b),
 'archived_count', (SELECT archived_count FROM pg_stat_archiver), 'failed_count', (SELECT failed_count FROM pg_stat_archiver),
 'idle_in_transaction_count', (SELECT count(*) FROM pg_stat_activity WHERE state = 'idle in transaction'),
 'idle_in_transaction_aborted_count', (SELECT count(*) FROM pg_stat_activity WHERE state = 'idle in transaction (aborted)'),
 'idle_in_transaction_max_time', (SELECT coalesce(max(extract(epoch FROM now() - state_change)), 0) FROM pg_stat_activity WHERE state = 'idle in transaction'),
 'active_transactions', (SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND xact_start IS NOT NULL AND pid <> pg_backend_pid()),
 'blocked_transactions', (SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'),
 'max_used_xact_ids', (SELECT max(age(datfrozenxid)) FROM pg_database),
 'max_connections', current_setting('max_connections')::int,
 'version', current_setting('server_version_num')::int)`

// piPostgresCheckpointerQuery reads what PostgreSQL 17 moved out of
// pg_stat_bgwriter.
const piPostgresCheckpointerQuery = `SELECT to_jsonb(c) FROM pg_stat_checkpointer c`

func piReadCounters(instance RDSInstance, engine dbengine.Engine, now time.Time) {
	counters := map[string]float64{"_at": float64(now.Unix())}
	if host, err := piHostCounters(instance); err == nil {
		for k, v := range host {
			counters[k] = v
		}
	}
	var err error
	if engine.Family == dbengine.Postgres {
		err = piPostgresCounters(instance, engine, counters)
	} else {
		err = piMySQLCounters(instance, engine, counters)
	}
	if err != nil && len(counters) == 1 {
		return
	}
	piRecordCounters(instance.DbiResourceId, now, counters)
}

func piNumbers(raw map[string]any, into map[string]float64, prefix string) {
	for k, v := range raw {
		switch n := v.(type) {
		case float64:
			into[prefix+k] = n
		case string:
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				into[prefix+k] = f
			}
		case map[string]any:
			piNumbers(n, into, prefix)
		}
	}
}

func piPostgresCounters(instance RDSInstance, engine dbengine.Engine, counters map[string]float64) error {
	out, err := piEngineQuery(instance, engine, piPostgresCountersQuery)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return fmt.Errorf("read the PostgreSQL statistics: %w", err)
	}
	piNumbers(raw, counters, "")
	if counters["version"] >= 170000 {
		out, err := piEngineQuery(instance, engine, piPostgresCheckpointerQuery)
		if err != nil {
			return err
		}
		var checkpointer map[string]any
		if err := json.Unmarshal([]byte(out), &checkpointer); err != nil {
			return fmt.Errorf("read pg_stat_checkpointer: %w", err)
		}
		piNumbers(checkpointer, counters, "checkpointer.")
		counters["checkpoints_timed"] = counters["checkpointer.num_timed"]
		counters["checkpoints_req"] = counters["checkpointer.num_requested"]
		counters["checkpoint_write_time"] = counters["checkpointer.write_time"]
		counters["checkpoint_sync_time"] = counters["checkpointer.sync_time"]
		counters["buffers_checkpoint"] = counters["checkpointer.buffers_written"]
	}
	counters["checkpoints"] = counters["checkpoints_timed"] + counters["checkpoints_req"]
	return nil
}

func piMySQLCounters(instance RDSInstance, engine dbengine.Engine, counters map[string]float64) error {
	statusTable := "performance_schema.global_status"
	if engine.Client == dbengine.MariaDB114.Client {
		statusTable = "information_schema.GLOBAL_STATUS"
	}
	out, err := piEngineQuery(instance, engine, "SELECT JSON_OBJECT('status', (SELECT JSON_OBJECTAGG(VARIABLE_NAME, VARIABLE_VALUE) FROM "+statusTable+
		"), 'max_connections', @@max_connections, 'active_transactions', (SELECT COUNT(1) FROM information_schema.INNODB_TRX)"+
		", 'metrics', (SELECT JSON_OBJECTAGG(NAME, `COUNT`) FROM information_schema.INNODB_METRICS WHERE NAME IN ('lock_deadlocks', 'lock_timeouts', 'lock_row_lock_waits')))")
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return fmt.Errorf("read the MySQL status: %w", err)
	}
	if status, ok := raw["status"].(map[string]any); ok {
		normalized := map[string]any{}
		for k, v := range status {
			normalized[piMySQLStatusName(k)] = v
		}
		raw["status"] = normalized
	}
	piNumbers(raw, counters, "")
	requests, reads := counters["Innodb_buffer_pool_read_requests"], counters["Innodb_buffer_pool_reads"]
	counters["innodb_buffer_pool_hits"] = requests - reads
	if requests > 0 {
		counters["innodb_buffer_pool_hit_rate"] = 100 * (requests - reads) / requests
	}
	if total := counters["Innodb_buffer_pool_pages_total"]; total > 0 {
		counters["innodb_buffer_pool_usage"] = 100 * counters["Innodb_buffer_pool_pages_data"] / total
	}
	counters["innodb_rows_changed"] = counters["Innodb_rows_inserted"] + counters["Innodb_rows_deleted"] + counters["Innodb_rows_updated"]
	return nil
}

// piMySQLStatusName spells a status variable as SHOW GLOBAL STATUS does:
// MariaDB's information_schema reports the names in capitals.
func piMySQLStatusName(name string) string {
	for _, c := range piCounters {
		if c.Family == "mysql" && strings.EqualFold(c.Source, name) {
			return c.Source
		}
		if c.Family == "mysql" && strings.EqualFold(c.Divisor, name) {
			return c.Divisor
		}
	}
	return name
}

// piHostCounters reads the kernel figures of the host the engine runs on.
func piHostCounters(instance RDSInstance) (map[string]float64, error) {
	out, err := rdsContainerOutput(rdsEngineLabels(instance.DBInstanceIdentifier), "DB instance "+instance.DBInstanceIdentifier,
		[]string{"cat", "/proc/stat", "/proc/meminfo", "/proc/loadavg"})
	if err != nil {
		return nil, err
	}
	counters := map[string]float64{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "cpu" && len(fields) >= 9:
			names := []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal", "guest"}
			total, idle := 0.0, 0.0
			for i, name := range names {
				if i+1 >= len(fields) {
					break
				}
				v, _ := strconv.ParseFloat(fields[i+1], 64)
				counters["cpu."+name] = v
				if name != "guest" {
					total += v
				}
				if name == "idle" || name == "iowait" {
					idle += v
				}
			}
			counters["cpu.total"] = total
			counters["cpu.busy"] = total - idle
		case fields[0] == "procs_running" && len(fields) == 2:
			counters["tasks.running"], _ = strconv.ParseFloat(fields[1], 64)
		case fields[0] == "procs_blocked" && len(fields) == 2:
			counters["tasks.blocked"], _ = strconv.ParseFloat(fields[1], 64)
		case strings.HasSuffix(fields[0], ":") && len(fields) >= 2:
			v, err := strconv.ParseFloat(fields[1], 64)
			if err == nil {
				counters["mem."+strings.TrimSuffix(fields[0], ":")] = v
			}
		case len(fields) == 5 && strings.Contains(fields[3], "/"):
			counters["load.one"], _ = strconv.ParseFloat(fields[0], 64)
			counters["load.five"], _ = strconv.ParseFloat(fields[1], 64)
			counters["load.fifteen"], _ = strconv.ParseFloat(fields[2], 64)
			if _, total, ok := strings.Cut(fields[3], "/"); ok {
				counters["tasks.total"], _ = strconv.ParseFloat(total, 64)
			}
		}
	}
	return counters, nil
}

// piCounterValue answers a counter's value for the minute cur, given the
// reading before it; ok is false where the minute carries no value.
func piCounterValue(c piCounter, prev, cur map[string]float64) (float64, bool) {
	value, has := cur[c.Source]
	if !has {
		return 0, false
	}
	switch c.Kind {
	case piGauge:
		return value, true
	case piPerSecond, piPerMinute:
		before, had := prev[c.Source]
		elapsed := cur["_at"] - prev["_at"]
		if !had || elapsed <= 0 || value < before {
			return 0, false
		}
		rate := (value - before) / elapsed
		if c.Kind == piPerMinute {
			rate *= 60
		}
		return rate, true
	case piCPUPercent:
		total := cur["cpu.total"] - prev["cpu.total"]
		before, had := prev[c.Source]
		if !had || total <= 0 {
			return 0, false
		}
		return 100 * (value - before) / total, true
	case piRatio:
		before, had := prev[c.Source]
		divisor := cur[c.Divisor] - prev[c.Divisor]
		if !had || divisor <= 0 {
			return 0, had
		}
		return (value - before) / divisor, true
	}
	return 0, false
}
