package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Metric, composite and log alarms all store and describe it.
type cwActionConfiguration struct {
	AlarmTagsPropagation *cwAlarmTagsPropagation `json:"AlarmTagsPropagation,omitempty" cbor:"AlarmTagsPropagation,omitempty"`
}

type cwAlarmTagsPropagation struct {
	Enabled         *bool    `json:"Enabled,omitempty" cbor:"Enabled,omitempty"`
	IncludedTagKeys []string `json:"IncludedTagKeys,omitempty" cbor:"IncludedTagKeys,omitempty"`
	ExcludedTagKeys []string `json:"ExcludedTagKeys,omitempty" cbor:"ExcludedTagKeys,omitempty"`
}

// nil means the request carried none; a non-empty message means Enabled is
// not a boolean.
func cwQueryActionConfiguration(r *http.Request) (*cwActionConfiguration, string) {
	const prefix = "ActionConfiguration.AlarmTagsPropagation."
	present := false
	for key := range r.Form {
		if strings.HasPrefix(key, prefix) {
			present = true
			break
		}
	}
	if !present {
		return nil, ""
	}
	tp := &cwAlarmTagsPropagation{
		IncludedTagKeys: cwQueryStringList(r, prefix+"IncludedTagKeys"),
		ExcludedTagKeys: cwQueryStringList(r, prefix+"ExcludedTagKeys"),
	}
	if raw := r.FormValue(prefix + "Enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, "The parameter ActionConfiguration.AlarmTagsPropagation.Enabled must be a boolean."
		}
		tp.Enabled = &enabled
	}
	return &cwActionConfiguration{AlarmTagsPropagation: tp}, ""
}

// The model's AlarmTagsPropagation constraints: Enabled is required, the two
// key lists exclude each other, and each holds 1 to 50 keys of 1 to 128
// characters.
func cwValidateActionConfiguration(ac *cwActionConfiguration) (code, msg string, ok bool) {
	if ac == nil || ac.AlarmTagsPropagation == nil {
		return "", "", true
	}
	tp := ac.AlarmTagsPropagation
	if tp.Enabled == nil {
		return "MissingParameter", "The parameter ActionConfiguration.AlarmTagsPropagation.Enabled is required.", false
	}
	if tp.IncludedTagKeys != nil && tp.ExcludedTagKeys != nil {
		return "InvalidParameterCombination", "IncludedTagKeys and ExcludedTagKeys cannot both be specified.", false
	}
	for member, keys := range map[string][]string{"IncludedTagKeys": tp.IncludedTagKeys, "ExcludedTagKeys": tp.ExcludedTagKeys} {
		if keys == nil {
			continue
		}
		if len(keys) < 1 || len(keys) > 50 {
			return "InvalidParameterValue", fmt.Sprintf("The parameter ActionConfiguration.AlarmTagsPropagation.%s must contain between 1 and 50 tag keys.", member), false
		}
		for _, k := range keys {
			if len(k) < 1 || len(k) > 128 {
				return "InvalidParameterValue", fmt.Sprintf("The parameter ActionConfiguration.AlarmTagsPropagation.%s must contain tag keys between 1 and 128 characters long.", member), false
			}
		}
	}
	return "", "", true
}

// Empty when the alarm has none.
func cwActionConfigurationXML(ac *cwActionConfiguration) string {
	if ac == nil || ac.AlarmTagsPropagation == nil {
		return ""
	}
	tp := ac.AlarmTagsPropagation
	var b strings.Builder
	b.WriteString("<ActionConfiguration><AlarmTagsPropagation>")
	if tp.Enabled != nil {
		fmt.Fprintf(&b, "<Enabled>%t</Enabled>", *tp.Enabled)
	}
	for _, list := range []struct {
		name string
		keys []string
	}{{"IncludedTagKeys", tp.IncludedTagKeys}, {"ExcludedTagKeys", tp.ExcludedTagKeys}} {
		if list.keys == nil {
			continue
		}
		fmt.Fprintf(&b, "<%s>", list.name)
		for _, k := range list.keys {
			fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(k))
		}
		fmt.Fprintf(&b, "</%s>", list.name)
	}
	b.WriteString("</AlarmTagsPropagation></ActionConfiguration>")
	return b.String()
}
