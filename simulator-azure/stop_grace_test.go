package main

import (
	"testing"
	"time"
)

// A replica's stop grace is the template's terminationGracePeriodSeconds, and
// thirty seconds when the template sets none; zero is a real setting.
func TestACAAppStopGraceReadsTheTemplate(t *testing.T) {
	var ten, zero int64 = 10, 0
	cases := map[string]struct {
		app  ContainerApp
		want time.Duration
	}{
		"no template": {ContainerApp{}, 30 * time.Second},
		"unset":       {ContainerApp{Properties: ContainerAppProps{Template: &ContainerAppTemplate{}}}, 30 * time.Second},
		"set":         {ContainerApp{Properties: ContainerAppProps{Template: &ContainerAppTemplate{TerminationGracePeriodSeconds: &ten}}}, 10 * time.Second},
		"zero":        {ContainerApp{Properties: ContainerAppProps{Template: &ContainerAppTemplate{TerminationGracePeriodSeconds: &zero}}}, 0},
	}
	for name, c := range cases {
		if got := acaAppStopGrace(c.app); got != c.want {
			t.Errorf("%s: stop grace = %s, want %s", name, got, c.want)
		}
	}
}

// A site's stop grace is WEBSITES_CONTAINER_STOP_TIME_LIMIT in seconds, five
// when unset or unparseable, and capped at the 120 the platform accepts.
func TestSiteStopGraceReadsTheAppSetting(t *testing.T) {
	site := func(value string) *Site {
		s := &Site{Properties: SiteProperties{SiteConfig: &SiteConfig{}}}
		if value != "" {
			s.Properties.SiteConfig.AppSettings = []NameValuePair{{Name: "WEBSITES_CONTAINER_STOP_TIME_LIMIT", Value: value}}
		}
		return s
	}
	cases := map[string]struct {
		site *Site
		want time.Duration
	}{
		"nil site":  {nil, 5 * time.Second},
		"unset":     {site(""), 5 * time.Second},
		"set":       {site("30"), 30 * time.Second},
		"capped":    {site("600"), 120 * time.Second},
		"garbage":   {site("soon"), 5 * time.Second},
		"negative":  {site("-3"), 5 * time.Second},
		"immediate": {site("0"), 0},
	}
	for name, c := range cases {
		if got := siteStopGrace(c.site); got != c.want {
			t.Errorf("%s: stop grace = %s, want %s", name, got, c.want)
		}
	}
}
