package main

import (
	"encoding/json"
	"testing"
)

func eventGridFilterTestSubscription(t *testing.T, filter string) EventGridEventSubscription {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal([]byte(filter), &f); err != nil {
		t.Fatalf("filter %s: %v", filter, err)
	}
	return EventGridEventSubscription{Properties: map[string]any{"filter": f}}
}

// Each advanced filter operator the Event Grid specification declares, graded
// against the documented semantics.
func TestEventGridAdvancedFilterOperators(t *testing.T) {
	const event = `{"id":"1","eventType":"Contoso.Items.ItemReceived","subject":"/A/B/c.jpg","data":{
		"counter":5,"price":3.5,"enabled":true,"name":"Azure Data Factory","nothing":null,
		"tags":["red","Blue"],"sizes":[1,20]}}`
	cases := []struct {
		filter string
		want   bool
	}{
		{`{"operatorType":"NumberIn","key":"data.counter","values":[5,1]}`, true},
		{`{"operatorType":"NumberIn","key":"data.counter","values":[4]}`, false},
		{`{"operatorType":"NumberNotIn","key":"data.counter","values":[41,0]}`, true},
		{`{"operatorType":"NumberNotIn","key":"data.counter","values":[5]}`, false},
		{`{"operatorType":"NumberNotIn","key":"data.absent","values":[5]}`, true},
		{`{"operatorType":"NumberLessThan","key":"data.counter","value":100}`, true},
		{`{"operatorType":"NumberLessThan","key":"data.counter","value":5}`, false},
		{`{"operatorType":"NumberGreaterThan","key":"data.counter","value":4}`, true},
		{`{"operatorType":"NumberLessThanOrEquals","key":"data.counter","value":5}`, true},
		{`{"operatorType":"NumberGreaterThanOrEquals","key":"data.counter","value":6}`, false},
		{`{"operatorType":"NumberInRange","key":"data.price","values":[[3.14159,999.95],[3000,4000]]}`, true},
		{`{"operatorType":"NumberInRange","key":"data.price","values":[[4,5]]}`, false},
		{`{"operatorType":"NumberNotInRange","key":"data.price","values":[[4,5]]}`, true},
		{`{"operatorType":"NumberNotInRange","key":"data.price","values":[[3,4]]}`, false},
		{`{"operatorType":"BoolEquals","key":"data.enabled","value":true}`, true},
		{`{"operatorType":"BoolEquals","key":"data.enabled","value":false}`, false},
		{`{"operatorType":"StringIn","key":"data.name","values":["azure data factory"]}`, true},
		{`{"operatorType":"StringIn","key":"data.name","values":["azure"]}`, false},
		{`{"operatorType":"StringNotIn","key":"data.name","values":["aws","bridge"]}`, true},
		{`{"operatorType":"StringNotIn","key":"data.absent","values":["aws"]}`, true},
		{`{"operatorType":"StringBeginsWith","key":"data.name","values":["event","AZURE"]}`, true},
		{`{"operatorType":"StringNotBeginsWith","key":"data.name","values":["azure"]}`, false},
		{`{"operatorType":"StringEndsWith","key":"Subject","values":["png",".JPG"]}`, true},
		{`{"operatorType":"StringNotEndsWith","key":"Subject","values":["png"]}`, true},
		{`{"operatorType":"StringContains","key":"data.name","values":["microsoft","data"]}`, true},
		{`{"operatorType":"StringNotContains","key":"data.name","values":["contoso","fabrikam"]}`, true},
		{`{"operatorType":"StringNotContains","key":"data.name","values":["factory"]}`, false},
		{`{"operatorType":"StringContains","key":"data.absent","values":["x"]}`, false},
		{`{"operatorType":"StringNotContains","key":"data.absent","values":["x"]}`, false},
		{`{"operatorType":"StringIn","key":"data.counter","values":["5"]}`, false},
		{`{"operatorType":"IsNullOrUndefined","key":"data.absent"}`, true},
		{`{"operatorType":"IsNullOrUndefined","key":"data.nothing"}`, true},
		{`{"operatorType":"IsNullOrUndefined","key":"data.counter"}`, false},
		{`{"operatorType":"IsNotNull","key":"data.counter"}`, true},
		{`{"operatorType":"IsNotNull","key":"data.nothing"}`, false},
		{`{"operatorType":"StringIn","key":"data.tags","values":["blue"]}`, false},
	}
	for _, c := range cases {
		es := eventGridFilterTestSubscription(t, `{"advancedFilters":[`+c.filter+`]}`)
		if got := eventGridFilterAdmits(es, json.RawMessage(event)); got != c.want {
			t.Errorf("%s: admits %v, want %v", c.filter, got, c.want)
		}
	}

	arrays := []struct {
		filter string
		want   bool
	}{
		{`{"operatorType":"StringIn","key":"data.tags","values":["blue"]}`, true},
		{`{"operatorType":"StringNotIn","key":"data.tags","values":["red"]}`, false},
		{`{"operatorType":"NumberGreaterThan","key":"data.sizes","value":10}`, true},
		{`{"operatorType":"NumberNotInRange","key":"data.sizes","values":[[15,25]]}`, false},
	}
	for _, c := range arrays {
		es := eventGridFilterTestSubscription(t, `{"enableAdvancedFilteringOnArrays":true,"advancedFilters":[`+c.filter+`]}`)
		if got := eventGridFilterAdmits(es, json.RawMessage(event)); got != c.want {
			t.Errorf("on arrays %s: admits %v, want %v", c.filter, got, c.want)
		}
	}

	both := eventGridFilterTestSubscription(t, `{"advancedFilters":[
		{"operatorType":"StringContains","key":"Subject","values":["/a/"]},
		{"operatorType":"NumberIn","key":"data.counter","values":[6]}]}`)
	if eventGridFilterAdmits(both, json.RawMessage(event)) {
		t.Error("two advanced filters must both hold")
	}
	cloudEvent := eventGridFilterTestSubscription(t, `{"includedEventTypes":["com.example.someevent"],
		"advancedFilters":[{"operatorType":"NumberIn","key":"comexampleothervalue","values":[5]}]}`)
	if !eventGridFilterAdmits(cloudEvent, json.RawMessage(`{"specversion":"1.0","type":"com.example.someevent","source":"/c","id":"1","comexampleothervalue":5}`)) {
		t.Error("a CloudEvents event must be filtered by its type and extension attributes")
	}
}
