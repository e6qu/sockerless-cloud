package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// acaJobCron parses a Schedule-triggered job's cronExpression: five fields,
// read in UTC.
func acaJobCron(config *JobConfiguration) (cron.Schedule, error) {
	if config == nil || config.ScheduleTrigger == nil || strings.TrimSpace(config.ScheduleTrigger.CronExpression) == "" {
		return cron.Schedule{}, fmt.Errorf("a job with triggerType Schedule requires scheduleTriggerConfig.cronExpression")
	}
	return cron.Parse(config.ScheduleTrigger.CronExpression, cron.Vixie, time.UTC)
}

// startACAJobSchedules starts an execution of every Schedule-triggered job at
// each occurrence of its cronExpression.
func startACAJobSchedules(srv *sim.Server, jobs sim.Store[ContainerAppJob], executions sim.Store[JobExecution]) {
	records := sim.MakeStore[cron.Record](srv.DB(), "aca_job_schedules")
	cron.NewTicker(records, func() []cron.Entry { return acaJobScheduleEntries(jobs, executions) }).
		Start(srv, "Container Apps job schedules", time.Second)
}

func acaJobScheduleEntries(jobs sim.Store[ContainerAppJob], executions sim.Store[JobExecution]) []cron.Entry {
	var entries []cron.Entry
	for _, job := range jobs.List() {
		config := job.Properties.Configuration
		if config == nil || !strings.EqualFold(config.TriggerType, "Schedule") || job.Properties.ProvisioningState != "Succeeded" {
			continue
		}
		schedule, err := acaJobCron(config)
		if err != nil {
			continue
		}
		entries = append(entries, cron.Entry{
			Key:  job.ID,
			Spec: config.ScheduleTrigger.CronExpression,
			Next: schedule.Next,
			Fire: func(time.Time) {
				if current, ok := jobs.Get(job.ID); ok {
					acaStartJobExecution(executions, current, current.Properties.Template)
				}
			},
		})
	}
	return entries
}
