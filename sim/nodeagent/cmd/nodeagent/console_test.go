package main

import (
	"testing"
	"time"
)

func TestConsoleKeepsEveryBootAndFollowsTheCurrentOne(t *testing.T) {
	c := newConsole()
	boot1, drained1 := c.beginBoot()
	c.add(boot1, logLine{Time: time.Now(), Stream: "stdout", Text: "first boot"})
	close(drained1)
	if n := c.received(boot1); n != 1 {
		t.Fatalf("boot 1 received %d lines, want 1", n)
	}
	boot2, drained2 := c.beginBoot()
	whole, _, _, stopWhole := c.follow(false)
	defer stopWhole()
	if len(whole) != 1 || whole[0].Text != "first boot" {
		t.Fatalf("the whole console before the second boot wrote is %+v", whole)
	}
	current, ch, drained, stop := c.follow(true)
	defer stop()
	if len(current) != 0 {
		t.Fatalf("the current boot has lines before it wrote: %+v", current)
	}
	if drained != drained2 {
		t.Fatal("follow does not hand out the boot in progress's drained channel")
	}
	c.add(boot2, logLine{Time: time.Now(), Stream: "stdout", Text: "second boot"})
	select {
	case line := <-ch:
		if line.Text != "second boot" {
			t.Fatalf("followed %q", line.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("the follower did not receive the new line")
	}
	if n := c.received(boot2); n != 1 {
		t.Fatalf("boot 2 received %d lines, want 1", n)
	}
	all, _, _, stopAll := c.follow(false)
	defer stopAll()
	if len(all) != 2 || all[0].Text != "first boot" || all[1].Text != "second boot" {
		t.Fatalf("the whole console is %+v", all)
	}
}
