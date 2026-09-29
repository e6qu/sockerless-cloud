package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// logicRecurrence is a Recurrence trigger's schedule: every Interval
// Frequency units from StartTime (the last save when absent), read in
// TimeZone, and for a Day, Week or Month frequency optionally at the
// schedule's hours, minutes, weekDays and monthDays.
type logicRecurrence struct {
	frequency string
	interval  int
	anchor    time.Time
	loc       *time.Location
	hours     []int
	minutes   []int
	weekDays  []time.Weekday
	monthDays []int
	scheduled bool
}

type logicRecurrenceSpec struct {
	Frequency string `json:"frequency"`
	Interval  int    `json:"interval"`
	StartTime string `json:"startTime"`
	TimeZone  string `json:"timeZone"`
	Schedule  *struct {
		Hours     []int    `json:"hours"`
		Minutes   []int    `json:"minutes"`
		WeekDays  []string `json:"weekDays"`
		MonthDays []int    `json:"monthDays"`
	} `json:"schedule"`
}

var logicWeekDays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// parseLogicRecurrence reads a trigger's recurrence object; saved is when
// the workflow was last saved, the anchor of a recurrence with no startTime.
func parseLogicRecurrence(raw any, saved time.Time) (logicRecurrence, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return logicRecurrence{}, err
	}
	var spec logicRecurrenceSpec
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return logicRecurrence{}, fmt.Errorf("recurrence: %w", err)
	}
	r := logicRecurrence{frequency: strings.ToLower(spec.Frequency), interval: spec.Interval, loc: time.UTC}
	switch r.frequency {
	case "second", "minute", "hour", "day", "week", "month":
	default:
		return logicRecurrence{}, fmt.Errorf("recurrence frequency %q is not Second, Minute, Hour, Day, Week or Month", spec.Frequency)
	}
	if r.interval < 1 {
		return logicRecurrence{}, fmt.Errorf("recurrence interval must be at least 1")
	}
	if spec.TimeZone != "" {
		name, ok := azureWindowsTimeZones[spec.TimeZone]
		if !ok {
			return logicRecurrence{}, fmt.Errorf("recurrence timeZone %q is not a Windows time zone", spec.TimeZone)
		}
		if r.loc, err = time.LoadLocation(name); err != nil {
			return logicRecurrence{}, err
		}
	}
	r.anchor = saved.In(r.loc)
	if spec.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, spec.StartTime); err == nil {
			r.anchor = t.In(r.loc)
		} else if t, err := time.ParseInLocation("2006-01-02T15:04:05", spec.StartTime, r.loc); err == nil {
			r.anchor = t
		} else {
			return logicRecurrence{}, fmt.Errorf("recurrence startTime %q is not an ISO 8601 date-time", spec.StartTime)
		}
	}
	if s := spec.Schedule; s != nil && (len(s.Hours)+len(s.Minutes)+len(s.WeekDays)+len(s.MonthDays)) > 0 {
		if r.frequency != "day" && r.frequency != "week" && r.frequency != "month" {
			return logicRecurrence{}, fmt.Errorf("a recurrence schedule applies only to the Day, Week and Month frequencies")
		}
		r.scheduled = true
		r.hours, r.minutes, r.monthDays = slices.Clone(s.Hours), slices.Clone(s.Minutes), slices.Clone(s.MonthDays)
		for _, day := range s.WeekDays {
			weekday, ok := logicWeekDays[strings.ToLower(day)]
			if !ok {
				return logicRecurrence{}, fmt.Errorf("recurrence weekDays holds %q", day)
			}
			r.weekDays = append(r.weekDays, weekday)
		}
		for _, h := range r.hours {
			if h < 0 || h > 23 {
				return logicRecurrence{}, fmt.Errorf("recurrence hours holds %d", h)
			}
		}
		for _, m := range r.minutes {
			if m < 0 || m > 59 {
				return logicRecurrence{}, fmt.Errorf("recurrence minutes holds %d", m)
			}
		}
		for _, d := range r.monthDays {
			if d < 1 || d > 31 {
				return logicRecurrence{}, fmt.Errorf("recurrence monthDays holds %d", d)
			}
		}
	}
	if len(r.hours) == 0 {
		r.hours = []int{r.anchor.Hour()}
	}
	if len(r.minutes) == 0 {
		r.minutes = []int{r.anchor.Minute()}
	}
	if len(r.weekDays) == 0 {
		r.weekDays = []time.Weekday{r.anchor.Weekday()}
	}
	if len(r.monthDays) == 0 {
		r.monthDays = []int{r.anchor.Day()}
	}
	slices.Sort(r.hours)
	slices.Sort(r.minutes)
	return r, nil
}

// first is the anchor itself when it has not passed, else the next
// occurrence after now.
func (r logicRecurrence) first(now time.Time) (time.Time, bool) {
	if !r.scheduled && !r.anchor.Before(now.Add(-time.Second)) {
		return r.anchor, true
	}
	if r.anchor.After(now) {
		return r.next(r.anchor.Add(-time.Nanosecond))
	}
	return r.next(now)
}

func (r logicRecurrence) next(after time.Time) (time.Time, bool) {
	if !r.scheduled {
		return r.nextPlain(after)
	}
	day := time.Date(after.In(r.loc).Year(), after.In(r.loc).Month(), after.In(r.loc).Day(), 0, 0, 0, 0, time.UTC)
	anchorDay := time.Date(r.anchor.Year(), r.anchor.Month(), r.anchor.Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(anchorDay) {
		day = anchorDay
	}
	for range 366 * 5 * r.interval {
		if r.dayQualifies(day, anchorDay) {
			for _, h := range r.hours {
				for _, m := range r.minutes {
					t := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, r.loc)
					if t.Hour() == h && t.Minute() == m && t.After(after) && !t.Before(r.anchor) {
						return t, true
					}
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, false
}

func (r logicRecurrence) dayQualifies(day, anchorDay time.Time) bool {
	switch r.frequency {
	case "day":
		return int(day.Sub(anchorDay).Hours()/24)%r.interval == 0
	case "week":
		weekStart := func(t time.Time) time.Time { return t.AddDate(0, 0, -int(t.Weekday())) }
		weeks := int(weekStart(day).Sub(weekStart(anchorDay)).Hours() / (24 * 7))
		return weeks%r.interval == 0 && slices.Contains(r.weekDays, day.Weekday())
	}
	months := (day.Year()-anchorDay.Year())*12 + int(day.Month()) - int(anchorDay.Month())
	return months%r.interval == 0 && slices.Contains(r.monthDays, day.Day())
}

// nextPlain steps from the anchor by whole intervals: elapsed time for the
// second, minute and hour frequencies, calendar days and months for the rest.
func (r logicRecurrence) nextPlain(after time.Time) (time.Time, bool) {
	var step time.Duration
	switch r.frequency {
	case "second":
		step = time.Second
	case "minute":
		step = time.Minute
	case "hour":
		step = time.Hour
	}
	if step > 0 {
		step *= time.Duration(r.interval)
		if after.Before(r.anchor) {
			return r.anchor, true
		}
		k := after.Sub(r.anchor)/step + 1
		return r.anchor.Add(k * step), true
	}
	at := func(k int) time.Time {
		switch r.frequency {
		case "day":
			return r.anchor.AddDate(0, 0, k*r.interval)
		case "week":
			return r.anchor.AddDate(0, 0, 7*k*r.interval)
		}
		return r.anchor.AddDate(0, k*r.interval, 0)
	}
	for k := 0; k < 100000; k++ {
		if t := at(k); t.After(after) {
			return t, true
		}
	}
	return time.Time{}, false
}

// startLogicRecurrences runs every enabled workflow's Recurrence triggers on
// their schedules.
func startLogicRecurrences(srv *sim.Server) {
	workflows := logicWorkflows
	records := sim.MakeStore[cron.Record](srv.DB(), "logic_recurrence_triggers")
	cron.NewTicker(records, func() []cron.Entry { return logicRecurrenceEntries(workflows) }).
		Start(srv, "Logic Apps recurrence triggers", time.Second)
}

func logicRecurrenceEntries(workflows sim.Store[LogicWorkflow]) []cron.Entry {
	var entries []cron.Entry
	for _, wf := range workflows.List() {
		if state, _ := wf.Properties["state"].(string); strings.EqualFold(state, "Disabled") {
			continue
		}
		saved, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(wf.Properties["changedTime"]))
		def, _ := wf.Properties["definition"].(map[string]any)
		triggers, _ := def["triggers"].(map[string]any)
		for name, raw := range triggers {
			trigger, _ := raw.(map[string]any)
			if !strings.EqualFold(fmt.Sprint(trigger["type"]), "Recurrence") {
				continue
			}
			recurrence, err := parseLogicRecurrence(trigger["recurrence"], saved)
			if err != nil {
				continue
			}
			spec, _ := json.Marshal(trigger["recurrence"])
			workflowID, triggerName := wf.ID, name
			entries = append(entries, cron.Entry{
				Key:   workflowID + "/triggers/" + triggerName,
				Spec:  string(spec) + "|" + saved.String(),
				First: recurrence.first,
				Next:  recurrence.next,
				Fire: func(time.Time) {
					if current, ok := workflows.Get(workflowID); ok {
						logicRecordTriggerRun(current, triggerName)
					}
				},
			})
		}
	}
	return entries
}

// logicValidateRecurrenceTriggers rejects a definition whose Recurrence
// triggers the service could not schedule.
func logicValidateRecurrenceTriggers(props map[string]any) error {
	def, _ := props["definition"].(map[string]any)
	triggers, _ := def["triggers"].(map[string]any)
	for name, raw := range triggers {
		trigger, _ := raw.(map[string]any)
		if !strings.EqualFold(fmt.Sprint(trigger["type"]), "Recurrence") {
			continue
		}
		if _, err := parseLogicRecurrence(trigger["recurrence"], time.Now()); err != nil {
			return fmt.Errorf("trigger %q: %w", name, err)
		}
	}
	return nil
}
