package main

import "testing"

func TestCWAlarmRuleEvaluatesTheUserGuideExamples(t *testing.T) {
	states := map[string]string{
		"CPUUtilizationTooHigh": "ALARM",
		"DiskReadOpsTooHigh":    "OK",
		"DeploymentInProgress":  "OK",
		"NetworkOutTooHigh":     "OK",
		"WebServer1CPU":         "ALARM",
		"WebServer2CPU":         "ALARM",
		"WebServer3CPU":         "OK",
		"WebServer4CPU":         "INSUFFICIENT_DATA",
		"DatabaseConnection1":   "OK",
		"DatabaseConnection2":   "ALARM",
		"DatabaseConnection3":   "OK",
		"DatabaseConnection4":   "ALARM",
		"AZ1Health":             "ALARM",
		"AZ2Health":             "OK",
		"AZ3Health":             "ALARM",
		"MaintenanceWindow":     "ALARM",
		`quoted "name"`:         "ALARM",
	}
	stateOf := func(ref string) string { return states[cwAlarmRefName(ref)] }
	cases := map[string]bool{
		"ALARM(CPUUtilizationTooHigh) AND ALARM(DiskReadOpsTooHigh)":                                              false,
		"ALARM(CPUUtilizationTooHigh) AND NOT ALARM(DeploymentInProgress)":                                        true,
		"(ALARM(CPUUtilizationTooHigh) OR ALARM(DiskReadOpsTooHigh)) AND OK(NetworkOutTooHigh)":                   true,
		"AT_LEAST(2, ALARM, (WebServer1CPU, WebServer2CPU, WebServer3CPU, WebServer4CPU))":                        true,
		"AT_LEAST(3, ALARM, (WebServer1CPU, WebServer2CPU, WebServer3CPU, WebServer4CPU))":                        false,
		"AT_LEAST(50%, OK, (DatabaseConnection1, DatabaseConnection2, DatabaseConnection3, DatabaseConnection4))": true,
		"AT_LEAST(75%, OK, (DatabaseConnection1, DatabaseConnection2, DatabaseConnection3, DatabaseConnection4))": false,
		"AT_LEAST(2, NOT ALARM, (WebServer1CPU, WebServer3CPU, WebServer4CPU))":                                   true,
		"AT_LEAST(2, ALARM, (AZ1Health, AZ2Health, AZ3Health)) AND NOT ALARM(MaintenanceWindow)":                  false,
		`ALARM("CPUUtilizationTooHigh")`: true,
		`ALARM("quoted \"name\"")`:       true,
		"ALARM(arn:aws:cloudwatch:us-east-1:123456789012:alarm:CPUUtilizationTooHigh)": true,
		"INSUFFICIENT_DATA(WebServer4CPU)":                                             true,
		"TRUE":                                                                         true,
		"FALSE OR NOT TRUE":                                                            false,
		"ALARM(Missing)":                                                               false,
	}
	for rule, want := range cases {
		n, err := cwParseAlarmRule(rule)
		if err != nil {
			t.Errorf("%s: %v", rule, err)
			continue
		}
		if got := n.eval(stateOf); got != want {
			t.Errorf("%s evaluated to %v, want %v", rule, got, want)
		}
	}
}

func TestCWAlarmRuleRefusesMalformedRules(t *testing.T) {
	for _, rule := range []string{
		"",
		"ALARM(",
		"ALARM(a) AND",
		"ALARM(a) ALARM(b)",
		"AT_LEAST(x, ALARM, (a))",
		"AT_LEAST(1, BROKEN, (a))",
		`ALARM("unterminated)`,
		"OKAY(a)",
	} {
		if _, err := cwParseAlarmRule(rule); err == nil {
			t.Errorf("%q parsed, want an error", rule)
		}
	}
}
