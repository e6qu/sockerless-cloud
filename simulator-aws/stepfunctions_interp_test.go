package main

import "testing"

func TestSFN_Interpreter(t *testing.T) {
	cancel := make(chan struct{})

	// Choice: numeric comparison + Default.
	choice := `{"StartAt":"C","States":{
		"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericGreaterThan":5,"Next":"Big"}],"Default":"Small"},
		"Big":{"Type":"Pass","Result":"big","End":true},
		"Small":{"Type":"Pass","Result":"small","End":true}}}`
	if out, status, err := sfnExecute(choice, `{"x":10}`, cancel); err != nil || status != "SUCCEEDED" || out != `"big"` {
		t.Fatalf("choice >5 → %q %q %v", out, status, err)
	}
	if out, _, _ := sfnExecute(choice, `{"x":1}`, cancel); out != `"small"` {
		t.Fatalf("choice <=5 → %q", out)
	}

	// Choice And + StringEquals.
	andDef := `{"StartAt":"C","States":{
		"C":{"Type":"Choice","Choices":[{"And":[{"Variable":"$.a","StringEquals":"yes"},{"Variable":"$.n","NumericEquals":2}],"Next":"Hit"}],"Default":"Miss"},
		"Hit":{"Type":"Pass","Result":"hit","End":true},
		"Miss":{"Type":"Pass","Result":"miss","End":true}}}`
	if out, _, _ := sfnExecute(andDef, `{"a":"yes","n":2}`, cancel); out != `"hit"` {
		t.Fatalf("choice And → %q", out)
	}
	if out, _, _ := sfnExecute(andDef, `{"a":"yes","n":3}`, cancel); out != `"miss"` {
		t.Fatalf("choice And miss → %q", out)
	}

	// Parallel: two branches → array of outputs.
	par := `{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[
		{"StartAt":"A","States":{"A":{"Type":"Pass","Result":"a","End":true}}},
		{"StartAt":"B","States":{"B":{"Type":"Pass","Result":"b","End":true}}}]}}}`
	if out, status, err := sfnExecute(par, `{}`, cancel); err != nil || status != "SUCCEEDED" || out != `["a","b"]` {
		t.Fatalf("parallel → %q %q %v", out, status, err)
	}

	// Map: iterate ItemsPath → array of per-item outputs.
	mp := `{"StartAt":"M","States":{"M":{"Type":"Map","End":true,"ItemsPath":"$.items",
		"ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","Result":"x","End":true}}}}}}`
	if out, status, err := sfnExecute(mp, `{"items":[1,2,3]}`, cancel); err != nil || status != "SUCCEEDED" || out != `["x","x","x"]` {
		t.Fatalf("map → %q %q %v", out, status, err)
	}

	// Pathologically nested Parallel must fail gracefully via the depth guard,
	// never overflow the goroutine stack (a fatal crash).
	deep := sfnNestedParallel(sfnMaxNestingDepth + 50)
	if _, status, err := sfnExecute(deep, `{}`, cancel); status != "FAILED" || err == nil {
		t.Fatalf("deep nesting should FAIL via depth guard, got status=%q err=%v", status, err)
	}
}
