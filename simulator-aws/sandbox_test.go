package main

import (
	"slices"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/moby/moby/api/types/container"
)

// Every AWS profile drops all capabilities, forbids new privileges and never
// runs privileged; Lambda alone pins the sandbox user and a read-only rootfs,
// while Fargate lets the image's USER win and adds SYS_CHROOT.
func TestAWSSandboxProfiles(t *testing.T) {
	cases := []struct {
		name         string
		profile      sim.SandboxProfile
		wantReadonly bool
		wantUser     string
		wantCapAdd   []string
	}{
		{"lambda", SandboxLambda, true, "1051:1051", nil},
		{"fargate", SandboxFargate, false, "", []string{"SYS_CHROOT"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hc := &container.HostConfig{}
			cc := &container.Config{}
			if err := c.profile.Apply(hc, cc); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if hc.Privileged {
				t.Error("Privileged must stay off")
			}
			if hc.ReadonlyRootfs != c.wantReadonly {
				t.Errorf("ReadonlyRootfs = %v, want %v", hc.ReadonlyRootfs, c.wantReadonly)
			}
			if cc.User != c.wantUser {
				t.Errorf("User = %q, want %q", cc.User, c.wantUser)
			}
			if !slices.Contains(hc.CapDrop, "ALL") {
				t.Errorf("CapDrop = %v, want ALL", hc.CapDrop)
			}
			if !slices.Contains(hc.SecurityOpt, "no-new-privileges") {
				t.Errorf("SecurityOpt = %v, want no-new-privileges", hc.SecurityOpt)
			}
			for _, want := range c.wantCapAdd {
				if !slices.Contains(hc.CapAdd, want) {
					t.Errorf("CapAdd = %v, want %s", hc.CapAdd, want)
				}
			}
		})
	}
}
