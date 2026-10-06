package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// web_webjobs_schedule.go runs a triggered webjob on the schedule its
// settings.job names: a six-field NCRONTAB expression ({second} {minute}
// {hour} {day} {month} {day-of-week}) read in the site's WEBSITE_TIME_ZONE,
// UTC when it sets none. Each occurrence starts a run whose trigger is
// "Schedule - <expression>", unless the job is still running or the site's
// WEBJOBS_STOPPED setting keeps its jobs from running. The scheduler keeps no
// occurrence across a simulator restart: like Kudu's, it starts from the next
// occurrence after it comes up and runs none it missed.

// webJobSchedule is a parsed NCRONTAB expression.
type webJobSchedule struct {
	seconds []int
	minutes cron.Schedule
}

// parseWebJobSchedule parses a six-field NCRONTAB expression.
func parseWebJobSchedule(expr string, loc *time.Location) (webJobSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 6 {
		return webJobSchedule{}, fmt.Errorf("the schedule %q has %d fields; a webjob schedule has six: {second} {minute} {hour} {day} {month} {day-of-week}", expr, len(fields))
	}
	seconds, err := parseCronSeconds(fields[0])
	if err != nil {
		return webJobSchedule{}, fmt.Errorf("the schedule %q: seconds: %w", expr, err)
	}
	minutes, err := cron.Parse(strings.Join(fields[1:], " "), cron.Vixie, loc)
	if err != nil {
		return webJobSchedule{}, fmt.Errorf("the schedule %q: %w", expr, err)
	}
	return webJobSchedule{seconds: seconds, minutes: minutes}, nil
}

// parseCronSeconds expands a seconds field: `*`, values, ranges, lists and
// `/` steps over 0–59, in ascending order.
func parseCronSeconds(spec string) ([]int, error) {
	var set [60]bool
	for _, part := range strings.Split(spec, ",") {
		rng, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 || n > 59 {
				return nil, fmt.Errorf("invalid step in %q", part)
			}
			rng, step = part[:i], n
		}
		from, to := 0, 59
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, errA := strconv.Atoi(rng[:strings.IndexByte(rng, '-')])
			b, errB := strconv.Atoi(rng[strings.IndexByte(rng, '-')+1:])
			if errA != nil || errB != nil {
				return nil, fmt.Errorf("invalid range %q", rng)
			}
			from, to = a, b
		default:
			v, err := strconv.Atoi(rng)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", rng)
			}
			from, to = v, v
			if step > 1 {
				to = 59
			}
		}
		if from < 0 || to > 59 || from > to {
			return nil, fmt.Errorf("%q is outside 0-59", part)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	var out []int
	for v, ok := range set {
		if ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// Next returns the first occurrence strictly after after.
func (s webJobSchedule) Next(after time.Time) (time.Time, bool) {
	minute := after.Truncate(time.Minute)
	// A minute the expression's last five fields hold is its own next
	// occurrence after the second before it.
	if next, ok := s.minutes.Next(minute.Add(-time.Second)); ok && next.Equal(minute) {
		for _, sec := range s.seconds {
			if t := minute.Add(time.Duration(sec) * time.Second); t.After(after) {
				return t, true
			}
		}
	}
	next, ok := s.minutes.Next(minute)
	if !ok {
		return time.Time{}, false
	}
	return next.Add(time.Duration(s.seconds[0]) * time.Second), true
}

// webSiteTimeZone is the zone WEBSITE_TIME_ZONE names, as a Windows time zone
// name or an IANA one; UTC when the site names none.
func webSiteTimeZone(site *Site) (*time.Location, error) {
	name := strings.TrimSpace(siteAppSettings(site)["WEBSITE_TIME_ZONE"])
	if name == "" {
		return time.UTC, nil
	}
	if iana, ok := azureWindowsTimeZones[name]; ok {
		name = iana
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("WEBSITE_TIME_ZONE %q names no time zone", name)
	}
	return loc, nil
}

// webJobScheduleExpr is the schedule a triggered job's settings.job names.
func webJobScheduleExpr(rec WebJobRecord) string {
	if s, ok := kuduWebJobSettings(rec)["schedule"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// startWebJobSchedules evaluates every scheduled triggered webjob each second.
func startWebJobSchedules(srv *sim.Server) {
	cron.NewTicker(sim.NewStateStore[cron.Record](), webJobScheduleEntries).
		Start(srv, "App Service webjob schedules", time.Second)
}

func webJobScheduleEntries() []cron.Entry {
	var entries []cron.Entry
	for _, rec := range webWebJobs.List() {
		if rec.JobKind != "triggered" {
			continue
		}
		site, ok := webJobSite(rec.SiteID)
		if !ok {
			continue
		}
		expr := webJobScheduleExpr(rec)
		if expr == "" {
			webJobScheduleState(&site, rec, "", nil)
			continue
		}
		loc, err := webSiteTimeZone(&site)
		if err == nil {
			var schedule webJobSchedule
			if schedule, err = parseWebJobSchedule(expr, loc); err == nil {
				id := rec.ID
				entries = append(entries, cron.Entry{
					Key:  id,
					Spec: expr + "|" + loc.String(),
					Next: schedule.Next,
					Fire: func(scheduled time.Time) { bg.Go(func() { webFireScheduledWebJob(id, expr, scheduled) }) },
				})
			}
		}
		webJobScheduleState(&site, rec, expr, err)
	}
	return entries
}

// webJobScheduleState records a schedule's state on the job: the error of
// one that does not parse, and the scheduler log line of one it arms.
func webJobScheduleState(site *Site, rec WebJobRecord, expr string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if rec.ScheduleError == msg && rec.Schedule == expr {
		return
	}
	webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
		row.Schedule = expr
		row.ScheduleError = msg
	})
	switch {
	case err != nil:
		webJobAppendLog(site, webJobSchedulerLogRel(rec.Name), "SYS ERR ", "Invalid schedule: "+msg)
	case expr != "":
		webJobAppendLog(site, webJobSchedulerLogRel(rec.Name), "SYS INFO", "Job schedule: "+expr)
	}
}

// webFireScheduledWebJob starts the run an occurrence of a job's schedule
// calls for.
func webFireScheduledWebJob(jobID, expr string, scheduled time.Time) {
	rec, ok := webWebJobs.Get(jobID)
	if !ok {
		return
	}
	site, ok := webJobSite(rec.SiteID)
	if !ok {
		return
	}
	logRel := webJobSchedulerLogRel(rec.Name)
	if webJobsStopped(rec.SiteID) {
		webJobAppendLog(&site, logRel, "SYS INFO", "Skipped the run scheduled for "+scheduled.UTC().Format(time.RFC3339)+": WEBJOBS_STOPPED is 1.")
		return
	}
	if latest, ok := webLatestRun(rec.ID); ok && latest.Status == "Running" {
		webJobAppendLog(&site, logRel, "SYS INFO", "Skipped the run scheduled for "+scheduled.UTC().Format(time.RFC3339)+": the job is already running.")
		return
	}
	runID := webRunTriggeredWebJob(&site, rec, "Schedule - "+expr, "")
	webJobAppendLog(&site, logRel, "SYS INFO", "Started run "+runID+" scheduled for "+scheduled.UTC().Format(time.RFC3339)+".")
}
