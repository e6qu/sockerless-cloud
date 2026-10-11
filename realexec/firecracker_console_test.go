package realexec

import (
	"testing"
)

// The console watch sees its line however the console's output is split into
// writes, and only once.
func TestConsoleLineWatchSeesALineSplitAcrossWrites(t *testing.T) {
	w := newConsoleLineWatch(guestNetworkUpLine)
	for _, chunk := range []string{"[    0.912] IP-Con", "fig: Comp", "lete:\n     device=eth0"} {
		select {
		case <-w.seen:
			t.Fatalf("the watch fired before %q was complete", guestNetworkUpLine)
		default:
		}
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-w.seen:
	default:
		t.Fatalf("the watch missed %q split across writes", guestNetworkUpLine)
	}
	if _, err := w.Write([]byte("IP-Config: Complete again")); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleLineWatchIgnoresOtherOutput(t *testing.T) {
	w := newConsoleLineWatch(guestNetworkUpLine)
	for _, chunk := range []string{"IP-Config: Got", " DHCP answer\n", "Complete"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-w.seen:
		t.Fatal("the watch fired on output that does not hold the line")
	default:
	}
}
