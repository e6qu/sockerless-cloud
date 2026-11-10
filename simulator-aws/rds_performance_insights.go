package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// Performance Insights on an Amazon RDS DB instance. The instance's
// EnablePerformanceInsights turns sampling on; every second the simulator
// reads the active sessions of the instance's real engine (pg_stat_activity
// on PostgreSQL, the Performance Schema on MySQL and MariaDB, or the process
// list every five seconds where the Performance Schema is off, as the User
// Guide's "Performance Schema for MariaDB or MySQL" describes), and every
// minute its counters. Data older than PerformanceInsightsRetentionPeriod
// days is dropped.

const (
	piDefaultRetention     = 7
	piLongestRetention     = 731
	piStatementBytes       = 500
	piCounterInterval      = time.Minute
	piProcesslistInterval  = 5 * time.Second
	piPerformanceSchemaOff = "processlist"
)

// piSession is one active session in one sample.
type piSession struct {
	Database     string `json:"db,omitempty"`
	Application  string `json:"app,omitempty"`
	HostID       string `json:"hid,omitempty"`
	HostName     string `json:"hn,omitempty"`
	SessionType  string `json:"st,omitempty"`
	UserID       string `json:"uid,omitempty"`
	UserName     string `json:"un,omitempty"`
	WaitEvent    string `json:"we,omitempty"`
	WaitType     string `json:"wt,omitempty"`
	SQLID        string `json:"sid,omitempty"`
	SQLDBID      string `json:"sdb,omitempty"`
	TokenizedID  string `json:"tid,omitempty"`
	TokenizedDB  string `json:"tdb,omitempty"`
	Statement    string `json:"sql,omitempty"`
	TokenizedSQL string `json:"tsql,omitempty"`
}

// piMinute holds one minute of a resource's samples: each second a sample
// was taken, with the sessions it found active.
type piMinute struct {
	Resource string
	Minute   int64
	Samples  map[int64][]piSession
	// Counters are the raw counter values read in the minute, cumulative
	// counters as the engine reports them.
	Counters map[string]float64
}

// piStatement keeps a statement's full text, which responses cut to 500 bytes.
type piStatement struct {
	Text      string
	Tokenized bool
}

var (
	piMinutes    sim.Store[piMinute]
	piStatements sim.Store[piStatement]

	piSamplerMu sync.Mutex
	piSampling  = map[string]bool{}
)

func registerRDSPerformanceInsights(srv *sim.Server, startBackground bool) {
	piMinutes = sim.MakeStore[piMinute](srv.DB(), "pi_minutes")
	piStatements = sim.MakeStore[piStatement](srv.DB(), "pi_statements")
	if !startBackground {
		return
	}
	srv.StartBackground("Performance Insights", func(ctx context.Context) {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastCounters := map[string]time.Time{}
		lastProcesslist := map[string]time.Time{}
		lastPrune := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				piSampleAll(now, lastCounters, lastProcesslist)
				if now.Sub(lastPrune) >= time.Hour {
					piPrune(now)
					lastPrune = now
				}
			}
		}
	})
}

// piRetention is how many days an instance keeps its Performance Insights
// data.
func piRetention(instance RDSInstance) int {
	if instance.PerformanceInsightsRetentionPeriod > 0 {
		return instance.PerformanceInsightsRetentionPeriod
	}
	return piDefaultRetention
}

func piPrune(now time.Time) {
	retention := map[string]int{}
	for _, instance := range rdsInstances.List() {
		retention[instance.DbiResourceId] = piRetention(instance)
	}
	piMinutes.Prune(func(m piMinute) bool {
		days, ok := retention[m.Resource]
		if !ok {
			days = piLongestRetention
		}
		return now.Sub(time.Unix(m.Minute*60, 0)) > time.Duration(days)*24*time.Hour
	})
}

// piSampleAll samples every instance with Performance Insights on whose
// engine is running.
func piSampleAll(now time.Time, lastCounters, lastProcesslist map[string]time.Time) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, instance := range rdsInstances.List() {
		if !instance.PerformanceInsightsEnabled || instance.DBInstanceStatus == "stopped" || instance.DBInstanceStatus == "stopping" {
			continue
		}
		engine, ok := rdsInstanceEngine(instance)
		if !ok {
			continue
		}
		piSamplerMu.Lock()
		if piSampling[instance.DbiResourceId] {
			piSamplerMu.Unlock()
			continue
		}
		piSampling[instance.DbiResourceId] = true
		piSamplerMu.Unlock()
		mu.Lock()
		counters := now.Sub(lastCounters[instance.DbiResourceId]) >= piCounterInterval
		if counters {
			lastCounters[instance.DbiResourceId] = now
		}
		processlistDue := now.Sub(lastProcesslist[instance.DbiResourceId]) >= piProcesslistInterval
		mu.Unlock()
		wg.Add(1)
		go func(instance RDSInstance, engine dbengine.Engine) {
			defer wg.Done()
			defer func() {
				piSamplerMu.Lock()
				delete(piSampling, instance.DbiResourceId)
				piSamplerMu.Unlock()
			}()
			sampled, viaProcesslist := piSampleInstance(instance, engine, now, processlistDue)
			if sampled && viaProcesslist {
				mu.Lock()
				lastProcesslist[instance.DbiResourceId] = now
				mu.Unlock()
			}
			if counters {
				piReadCounters(instance, engine, now)
			}
		}(instance, engine)
	}
	wg.Wait()
}

func piMinuteKey(resource string, minute int64) string {
	return resource + "|" + fmt.Sprintf("%012d", minute)
}

func piRecordSample(resource string, at time.Time, sessions []piSession) {
	minute := at.Unix() / 60
	piMinutes.Upsert(piMinuteKey(resource, minute), func(m *piMinute) {
		m.Resource, m.Minute = resource, minute
		if m.Samples == nil {
			m.Samples = map[int64][]piSession{}
		}
		m.Samples[at.Unix()] = sessions
	})
	piDeliverDBLoad(resource, at, sessions)
}

func piRecordCounters(resource string, at time.Time, counters map[string]float64) {
	minute := at.Unix() / 60
	piMinutes.Upsert(piMinuteKey(resource, minute), func(m *piMinute) {
		m.Resource, m.Minute = resource, minute
		if m.Samples == nil {
			m.Samples = map[int64][]piSession{}
		}
		m.Counters = counters
	})
}

// piSampleInstance takes one sample of instance's active sessions. A MySQL
// or MariaDB engine whose Performance Schema is off is sampled from its
// process list once every five seconds only.
func piSampleInstance(instance RDSInstance, engine dbengine.Engine, now time.Time, processlistDue bool) (sampled, viaProcesslist bool) {
	if engine.Family == dbengine.Postgres {
		sessions, err := piPostgresSessions(instance, engine)
		if err != nil {
			return false, false
		}
		piRecordSample(instance.DbiResourceId, now, sessions)
		return true, false
	}
	on, err := piPerformanceSchemaOn(instance, engine)
	if err != nil {
		return false, false
	}
	if !on {
		if !processlistDue {
			return false, true
		}
		sessions, err := piProcesslistSessions(instance, engine)
		if err != nil {
			return false, true
		}
		piRecordSample(instance.DbiResourceId, now, sessions)
		return true, true
	}
	sessions, err := piPerformanceSchemaSessions(instance, engine)
	if err != nil {
		return false, false
	}
	piRecordSample(instance.DbiResourceId, now, sessions)
	return true, false
}

// piEngineQuery runs a query that answers one value in instance's engine.
func piEngineQuery(instance RDSInstance, engine dbengine.Engine, query string) (string, error) {
	labels := rdsEngineLabels(instance.DBInstanceIdentifier)
	name := "DB instance " + instance.DBInstanceIdentifier
	if engine.Family == dbengine.Postgres {
		out, err := rdsContainerOutput(labels, name, []string{engine.Client, "-v", "ON_ERROR_STOP=1", "-At",
			"-U", instance.MasterUsername, "-d", rdsDatabaseName(instance), "-c", query})
		return strings.TrimSpace(out), err
	}
	secret := instance.BackendMasterUserSecret
	if len(secret) == 0 {
		secret = instance.MasterUserSecret
	}
	_, password, ok := kmsDecryptBytes(secret)
	if !ok {
		return "", fmt.Errorf("decrypt the master-user credential")
	}
	out, err := rdsContainerOutput(labels, name, []string{engine.Client, "--user=root", "--password=" + string(password),
		"--batch", "--skip-column-names", "--raw", "--execute=" + query})
	return strings.TrimSpace(out), err
}

const piPostgresSessionsQuery = `SELECT coalesce(json_agg(json_build_object(
 'db', coalesce(a.datname, ''), 'app', coalesce(a.application_name, ''),
 'host', coalesce(host(a.client_addr), ''), 'hostname', coalesce(a.client_hostname, ''),
 'type', coalesce(a.backend_type, ''), 'uid', coalesce(a.usesysid::text, ''), 'user', coalesce(a.usename, ''),
 'wait_type', coalesce(a.wait_event_type, ''), 'wait', coalesce(a.wait_event, ''),
 'query_id', coalesce(to_jsonb(a)->>'query_id', ''), 'sql', coalesce(a.query, ''))), '[]')
 FROM pg_stat_activity a WHERE a.state = 'active' AND a.pid <> pg_backend_pid()`

func piPostgresSessions(instance RDSInstance, engine dbengine.Engine) ([]piSession, error) {
	out, err := piEngineQuery(instance, engine, piPostgresSessionsQuery)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		DB       string `json:"db"`
		App      string `json:"app"`
		Host     string `json:"host"`
		Hostname string `json:"hostname"`
		Type     string `json:"type"`
		UID      string `json:"uid"`
		User     string `json:"user"`
		WaitType string `json:"wait_type"`
		Wait     string `json:"wait"`
		QueryID  string `json:"query_id"`
		SQL      string `json:"sql"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("read pg_stat_activity: %w", err)
	}
	sessions := make([]piSession, 0, len(rows))
	for _, row := range rows {
		waitType, wait := row.WaitType, row.Wait
		if waitType == "" {
			waitType, wait = "CPU", "CPU"
		} else {
			wait = waitType + ":" + wait
		}
		host := row.Host
		if host == "" {
			host = "local"
		}
		hostname := row.Hostname
		if hostname == "" {
			hostname = host
		}
		s := piSession{Database: row.DB, Application: row.App, HostID: host, HostName: hostname, SessionType: row.Type,
			UserID: row.UID, UserName: row.User, WaitEvent: wait, WaitType: waitType}
		piSetStatement(&s, row.SQL, row.QueryID, "")
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// piSchemaChecks remembers, per resource, whether its engine runs the
// Performance Schema with the consumers Performance Insights reads; the
// engine changes that only at a restart, so a check holds for a minute.
var (
	piSchemaChecksMu sync.Mutex
	piSchemaChecks   = map[string]piSchemaCheck{}
)

type piSchemaCheck struct {
	on bool
	at time.Time
}

func piPerformanceSchemaOn(instance RDSInstance, engine dbengine.Engine) (bool, error) {
	piSchemaChecksMu.Lock()
	check, ok := piSchemaChecks[instance.DbiResourceId]
	piSchemaChecksMu.Unlock()
	if ok && time.Since(check.at) < time.Minute {
		return check.on, nil
	}
	on, err := piCheckPerformanceSchema(instance, engine)
	if err == nil {
		piSchemaChecksMu.Lock()
		piSchemaChecks[instance.DbiResourceId] = piSchemaCheck{on: on, at: time.Now()}
		piSchemaChecksMu.Unlock()
	}
	return on, err
}

func piCheckPerformanceSchema(instance RDSInstance, engine dbengine.Engine) (bool, error) {
	out, err := piEngineQuery(instance, engine, "SELECT @@performance_schema")
	if err != nil {
		return false, err
	}
	if out != "1" {
		return false, nil
	}
	// Performance Insights manages the Performance Schema it samples: the
	// consumers of the current statements and waits.
	_, err = piEngineQuery(instance, engine, "UPDATE performance_schema.setup_consumers SET ENABLED = 'YES' "+
		"WHERE NAME IN ('events_statements_current', 'events_waits_current') AND ENABLED = 'NO'")
	return err == nil, err
}

const piPerformanceSchemaQuery = `SELECT JSON_ARRAYAGG(JSON_OBJECT(
 'db', COALESCE(t.PROCESSLIST_DB, ''), 'user', COALESCE(t.PROCESSLIST_USER, ''), 'host', COALESCE(t.PROCESSLIST_HOST, ''),
 'sql', COALESCE(s.SQL_TEXT, ''), 'digest', COALESCE(s.DIGEST, ''), 'digest_text', COALESCE(s.DIGEST_TEXT, ''),
 'wait', COALESCE(w.EVENT_NAME, '')))
 FROM performance_schema.threads t
 LEFT JOIN performance_schema.events_statements_current s ON s.THREAD_ID = t.THREAD_ID AND s.END_EVENT_ID IS NULL
 LEFT JOIN performance_schema.events_waits_current w ON w.THREAD_ID = t.THREAD_ID AND w.END_EVENT_ID IS NULL
 WHERE t.TYPE = 'FOREGROUND' AND t.PROCESSLIST_COMMAND NOT IN ('Sleep', 'Daemon', 'Binlog Dump', 'Binlog Dump GTID')
 AND t.PROCESSLIST_ID <> CONNECTION_ID()`

func piPerformanceSchemaSessions(instance RDSInstance, engine dbengine.Engine) ([]piSession, error) {
	out, err := piEngineQuery(instance, engine, piPerformanceSchemaQuery)
	if err != nil {
		return nil, err
	}
	if out == "" || out == "NULL" {
		return []piSession{}, nil
	}
	var rows []struct {
		DB         string `json:"db"`
		User       string `json:"user"`
		Host       string `json:"host"`
		SQL        string `json:"sql"`
		Digest     string `json:"digest"`
		DigestText string `json:"digest_text"`
		Wait       string `json:"wait"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("read the Performance Schema: %w", err)
	}
	sessions := make([]piSession, 0, len(rows))
	for _, row := range rows {
		waitType, wait := "CPU", "CPU"
		if row.Wait != "" && !strings.HasPrefix(row.Wait, "idle") {
			wait = row.Wait
			parts := strings.Split(row.Wait, "/")
			if len(parts) >= 2 {
				waitType = parts[0] + "/" + parts[1]
			}
		}
		s := piSession{Database: row.DB, HostID: piMySQLHost(row.Host), HostName: piMySQLHost(row.Host),
			UserID: row.User, UserName: row.User, WaitEvent: wait, WaitType: waitType}
		piSetStatement(&s, row.SQL, row.Digest, row.DigestText)
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// piProcesslistSessions reads the process list, whose STATE stands in for
// the wait event, and which carries no per-SQL detail.
func piProcesslistSessions(instance RDSInstance, engine dbengine.Engine) ([]piSession, error) {
	out, err := piEngineQuery(instance, engine, `SELECT JSON_ARRAYAGG(JSON_OBJECT('db', COALESCE(DB, ''), 'user', COALESCE(USER, ''),
 'host', COALESCE(HOST, ''), 'state', COALESCE(STATE, ''))) FROM information_schema.PROCESSLIST
 WHERE COMMAND NOT IN ('Sleep', 'Daemon', 'Binlog Dump', 'Binlog Dump GTID') AND ID <> CONNECTION_ID()`)
	if err != nil {
		return nil, err
	}
	if out == "" || out == "NULL" {
		return []piSession{}, nil
	}
	var rows []struct{ DB, User, Host, State string }
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("read the process list: %w", err)
	}
	sessions := make([]piSession, 0, len(rows))
	for _, row := range rows {
		state := row.State
		if state == "" {
			state = "CPU"
		}
		sessions = append(sessions, piSession{Database: row.DB, HostID: piMySQLHost(row.Host), HostName: piMySQLHost(row.Host),
			UserID: row.User, UserName: row.User, WaitEvent: state, WaitType: state})
	}
	return sessions, nil
}

// piMySQLHost drops the client port MySQL reports with the host.
func piMySQLHost(host string) string {
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

var (
	piStringLiteral  = regexp.MustCompile(`'(?:[^']|'')*'`)
	piNumericLiteral = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	piPlaceholder    = regexp.MustCompile(`\$\d+`)
	piWhitespace     = regexp.MustCompile(`\s+`)
)

// piTokenize writes a statement's digest: its literals become ?.
func piTokenize(statement string) string {
	out := piStringLiteral.ReplaceAllString(statement, "?")
	out = piPlaceholder.ReplaceAllString(out, "?")
	out = piNumericLiteral.ReplaceAllString(out, "?")
	return strings.TrimSpace(piWhitespace.ReplaceAllString(out, " "))
}

func piHash(text string) string {
	sum := sha1.Sum([]byte(text))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// piSyntheticID is the "pi-" ID Performance Insights generates where the
// engine has none of its own.
func piSyntheticID(text string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	return "pi-" + strconv.FormatUint(uint64(h.Sum32()), 10)
}

func piSetStatement(s *piSession, statement, engineID, digest string) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return
	}
	tokenized := digest
	if tokenized == "" {
		tokenized = piTokenize(statement)
	}
	s.SQLID, s.TokenizedID = piHash(statement), piHash(tokenized)
	s.SQLDBID, s.TokenizedDB = piSyntheticID(statement), piSyntheticID(tokenized)
	if engineID != "" && engineID != "0" {
		s.TokenizedDB = engineID
	}
	s.Statement, s.TokenizedSQL = piTruncate(statement), piTruncate(tokenized)
	if _, ok := piStatements.Get(s.SQLID); !ok {
		piStatements.Put(s.SQLID, piStatement{Text: statement})
	}
	if _, ok := piStatements.Get(s.TokenizedID); !ok {
		piStatements.Put(s.TokenizedID, piStatement{Text: tokenized, Tokenized: true})
	}
}

func piTruncate(text string) string {
	if len(text) <= piStatementBytes {
		return text
	}
	cut := piStatementBytes
	for cut > 0 && (text[cut]&0xC0) == 0x80 {
		cut--
	}
	return text[:cut]
}

// piDeliverDBLoad publishes the AWS/RDS DBLoad metrics the User Guide's
// "CloudWatch metrics for Performance Insights" lists, for a sample that
// found load.
func piDeliverDBLoad(resource string, at time.Time, sessions []piSession) {
	if len(sessions) == 0 || cwMetrics == nil {
		return
	}
	instance, ok := piInstanceByResource(resource)
	if !ok {
		return
	}
	cpu := 0
	for _, s := range sessions {
		if s.WaitType == "CPU" {
			cpu++
		}
	}
	dims := []CWDimension{{Name: "DBInstanceIdentifier", Value: instance.DBInstanceIdentifier}}
	ts := float64(at.Unix())
	cwStoreDatum(CWMetricDatum{Namespace: "AWS/RDS", MetricName: "DBLoad", Dimensions: dims, Value: float64(len(sessions)), Timestamp: ts, Unit: "None"})
	cwStoreDatum(CWMetricDatum{Namespace: "AWS/RDS", MetricName: "DBLoadCPU", Dimensions: dims, Value: float64(cpu), Timestamp: ts, Unit: "None"})
	cwStoreDatum(CWMetricDatum{Namespace: "AWS/RDS", MetricName: "DBLoadNonCPU", Dimensions: dims, Value: float64(len(sessions) - cpu), Timestamp: ts, Unit: "None"})
}

func piInstanceByResource(resource string) (RDSInstance, bool) {
	for _, instance := range rdsInstances.List() {
		if instance.DbiResourceId == resource {
			return instance, true
		}
	}
	return RDSInstance{}, false
}

// rdsRequestedPerformanceInsights applies a CreateDBInstance's or a
// ModifyDBInstance's Performance Insights settings to instance; they take
// effect at once, without a reboot.
func rdsRequestedPerformanceInsights(r *http.Request, instance *RDSInstance) (code, message string) {
	enable := rdsRequestedBool(r, "EnablePerformanceInsights")
	mode := r.FormValue("DatabaseInsightsMode")
	switch mode {
	case "", "standard", "advanced":
	default:
		return "InvalidParameterValue", fmt.Sprintf("Invalid DatabaseInsightsMode %s. Valid values are standard and advanced.", mode)
	}
	if enable != nil {
		instance.PerformanceInsightsEnabled = *enable
	}
	if mode != "" {
		instance.DatabaseInsightsMode = mode
	}
	if instance.DatabaseInsightsMode == "advanced" && !instance.PerformanceInsightsEnabled {
		return "InvalidParameterCombination", "Database Insights advanced mode requires Performance Insights to be enabled."
	}
	if v := r.FormValue("PerformanceInsightsRetentionPeriod"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || !piValidRetention(days) {
			return "InvalidParameterValue", fmt.Sprintf("Invalid PerformanceInsightsRetentionPeriod %s. Specify 7, month * 31 (where month is a number from 1-23), or 731.", v)
		}
		if !instance.PerformanceInsightsEnabled {
			return "InvalidParameterCombination", "To set PerformanceInsightsRetentionPeriod, enable Performance Insights."
		}
		instance.PerformanceInsightsRetentionPeriod = days
	}
	if ref := r.FormValue("PerformanceInsightsKMSKeyId"); ref != "" {
		if !instance.PerformanceInsightsEnabled {
			return "InvalidParameterCombination", "To set PerformanceInsightsKMSKeyId, enable Performance Insights."
		}
		keyID, ok := resolveKMSKey(ref)
		if !ok {
			return "KMSKeyNotAccessibleFault", fmt.Sprintf("The specified KMS key %s does not exist or is not accessible.", ref)
		}
		key, _ := kmsKeys.Get(keyID)
		if instance.PerformanceInsightsKMSKeyId != "" && instance.PerformanceInsightsKMSKeyId != key.Arn {
			return "InvalidParameterCombination", "You can't change the KMS key of Performance Insights once it is set."
		}
		instance.PerformanceInsightsKMSKeyId = key.Arn
	}
	if instance.PerformanceInsightsEnabled {
		if instance.PerformanceInsightsRetentionPeriod == 0 {
			instance.PerformanceInsightsRetentionPeriod = piDefaultRetention
		}
		if instance.PerformanceInsightsKMSKeyId == "" {
			key, err := kmsAWSManagedKey("rds")
			if err != nil {
				return "InternalFailure", err.Error()
			}
			instance.PerformanceInsightsKMSKeyId = key.Arn
		}
	}
	if instance.DatabaseInsightsMode == "" {
		instance.DatabaseInsightsMode = "standard"
	}
	return "", ""
}

func piValidRetention(days int) bool {
	return days == piDefaultRetention || days == piLongestRetention || days%31 == 0 && days/31 >= 1 && days/31 <= 23
}

func renderRDSPerformanceInsights(i RDSInstance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<PerformanceInsightsEnabled>%t</PerformanceInsightsEnabled>", i.PerformanceInsightsEnabled)
	if i.PerformanceInsightsEnabled {
		fmt.Fprintf(&b, "<PerformanceInsightsKMSKeyId>%s</PerformanceInsightsKMSKeyId>", xmlEscape(i.PerformanceInsightsKMSKeyId))
		fmt.Fprintf(&b, "<PerformanceInsightsRetentionPeriod>%d</PerformanceInsightsRetentionPeriod>", piRetention(i))
	}
	if i.DatabaseInsightsMode != "" {
		fmt.Fprintf(&b, "<DatabaseInsightsMode>%s</DatabaseInsightsMode>", xmlEscape(i.DatabaseInsightsMode))
	}
	return b.String()
}

// piSampledMinutes reads resource's minutes in [start, end).
func piSampledMinutes(resource string, start, end time.Time) []piMinute {
	var out []piMinute
	for _, entry := range piMinutes.ListPrefix(resource + "|") {
		m := entry.Item
		if (m.Minute+1)*60 <= start.Unix() || m.Minute*60 >= end.Unix() {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minute < out[j].Minute })
	return out
}
