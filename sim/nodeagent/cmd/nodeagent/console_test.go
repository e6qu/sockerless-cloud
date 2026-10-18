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
	whole, _, _, _, stopWhole := c.follow(selection{})
	defer stopWhole()
	if len(whole) != 1 || whole[0].Text != "first boot" {
		t.Fatalf("the whole console before the second boot wrote is %+v", whole)
	}
	current, ch, drained, _, stop := c.follow(selection{currentBoot: true})
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
	all, _, _, _, stopAll := c.follow(selection{})
	defer stopAll()
	if len(all) != 2 || all[0].Text != "first boot" || all[1].Text != "second boot" {
		t.Fatalf("the whole console is %+v", all)
	}
}

// A reader of one process sees that process's lines alone, and learns when
// the process has exited and every line it wrote is out.
func TestConsoleSelectsOneProcessAndKnowsItsEnd(t *testing.T) {
	c := newConsole()
	boot, _ := c.beginBoot()
	c.add(boot, logLine{Stream: "stdout", Text: "serving"})
	c.add(boot, logLine{Stream: "stdout", Text: "proxy up", Member: "proxy"})
	c.add(boot, logLine{Stream: "stdout", Text: "request", Member: "proxy"})
	first, _, _, firstEnd, stopFirst := c.follow(selection{process: initProcess})
	defer stopFirst()
	if len(first) != 1 || first[0].Text != "serving" {
		t.Fatalf("the first process's lines are %+v", first)
	}
	proxy, _, _, proxyEnd, stopProxy := c.follow(selection{process: "proxy"})
	defer stopProxy()
	if len(proxy) != 2 || proxy[1].Text != "request" {
		t.Fatalf("the member's lines are %+v", proxy)
	}
	select {
	case <-proxyEnd.done:
		t.Fatal("the member ended before its exit report")
	default:
	}
	c.processEnded(boot, "proxy", 2)
	select {
	case <-proxyEnd.done:
	default:
		t.Fatal("the member's end did not close")
	}
	if proxyEnd.lines != 2 {
		t.Fatalf("the member wrote %d lines by its report, want 2", proxyEnd.lines)
	}
	select {
	case <-firstEnd.done:
		t.Fatal("the first process ended with the member")
	default:
	}
}
