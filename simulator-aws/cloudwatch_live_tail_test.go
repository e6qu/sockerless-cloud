package main

import (
	"testing"
)

func TestLiveTailSessionEvaluatesFilterPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"error 404", []string{"error 404 on /a"}},
		{"?timeout ?refused", []string{"connection refused", "read timeout"}},
		{"error -404", []string{"error 500 on /b"}},
		{`{ $.status = 500 }`, []string{`{"status":500}`}},
		{`%error [45]0[04]%`, []string{"error 404 on /a", "error 500 on /b"}},
		{`%on /[a]% -500`, []string{"error 404 on /a"}},
		{`%read time%`, []string{"read timeout"}},
	} {
		pattern, err := cwCompileLogPattern(tc.pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		s := &cwLiveTailSession{groupARNs: map[string]string{"g": "arn:g"}, pattern: pattern}
		s.offer("g", "s", []CWLogEvent{
			{Message: "error 404 on /a"},
			{Message: "error 500 on /b"},
			{Message: "connection refused"},
			{Message: "read timeout"},
			{Message: `{"status":500}`},
		})
		got, _ := s.drain()
		var messages []string
		for _, ev := range got {
			messages = append(messages, ev.Message)
		}
		if len(messages) != len(tc.want) {
			t.Fatalf("pattern %q streamed %q, want %q", tc.pattern, messages, tc.want)
		}
		for i := range messages {
			if messages[i] != tc.want[i] {
				t.Fatalf("pattern %q streamed %q, want %q", tc.pattern, messages, tc.want)
			}
		}
	}
	if _, err := cwCompileLogPattern(`{ $.level = }`); err == nil {
		t.Fatal("a structured pattern missing its value must not compile")
	}
	if _, err := cwCompileLogPattern(`%error [%`); err == nil {
		t.Fatal("an invalid regular expression must not compile")
	}
}
