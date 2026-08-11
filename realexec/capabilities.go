package realexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const (
	capNetAdmin = 12
	capSysAdmin = 21
)

// CapabilityReport records the host capabilities required by the VM/network
// substrate. It is intentionally about the current process, not about what a
// privileged wrapper might be able to do.
type CapabilityReport struct {
	GOOS        string
	Commands    map[string]string
	KVMPath     string
	KVMUsable   bool
	CapNetAdmin bool
	CapSysAdmin bool
	Missing     []string
}

// ErrMissingCapability is returned when a migrated real-execution API path
// cannot create its required host-side objects.
type ErrMissingCapability struct {
	Missing []string
}

func (e *ErrMissingCapability) Error() string {
	return "missing real-execution host capabilities: " + strings.Join(e.Missing, ", ")
}

func (e *ErrMissingCapability) Is(target error) bool {
	_, ok := target.(*ErrMissingCapability)
	return ok
}

// DetectCapabilities inspects host capabilities without creating persistent
// resources. Network namespace, link, and nftables mutations are exercised by
// the real host tests, not by this discovery pass.
func DetectCapabilities(requiredCommands ...string) CapabilityReport {
	if len(requiredCommands) == 0 {
		requiredCommands = []string{"firecracker", "jailer", "ip", "nft"}
	}
	requiresKVM := commandsRequireKVM(requiredCommands)

	report := CapabilityReport{
		GOOS:     runtime.GOOS,
		Commands: map[string]string{},
		KVMPath:  "/dev/kvm",
	}

	if report.GOOS != "linux" {
		report.Missing = append(report.Missing, "linux host")
	}

	for _, name := range requiredCommands {
		path, err := exec.LookPath(name)
		if err != nil {
			report.Missing = append(report.Missing, "command:"+name)
			continue
		}
		report.Commands[name] = path
	}

	if report.GOOS == "linux" && requiresKVM {
		if f, err := os.OpenFile(report.KVMPath, os.O_RDWR, 0); err == nil {
			report.KVMUsable = true
			_ = f.Close()
		} else {
			report.Missing = append(report.Missing, "kvm:"+report.KVMPath)
		}

	}

	if report.GOOS == "linux" {
		effectiveCaps, err := linuxEffectiveCapabilities("/proc/self/status")
		if err != nil {
			report.Missing = append(report.Missing, "linux-capabilities")
		} else {
			report.CapNetAdmin = hasCapability(effectiveCaps, capNetAdmin)
			report.CapSysAdmin = hasCapability(effectiveCaps, capSysAdmin)
			if !report.CapNetAdmin {
				report.Missing = append(report.Missing, "cap:CAP_NET_ADMIN")
			}
			if !report.CapSysAdmin {
				report.Missing = append(report.Missing, "cap:CAP_SYS_ADMIN")
			}
		}
	}

	return report
}

func DetectNetworkCapabilities() CapabilityReport {
	return DetectCapabilities("ip", "nft", "sysctl")
}

func DetectExternalNamespaceCapabilities() CapabilityReport {
	return DetectCapabilities("ip", "nft", "sysctl", "nsenter")
}

// DetectCaptureCapabilities reports what traffic capture needs, which is less
// than the network substrate needs. Capturing opens a packet socket on an
// interface inside a network namespace: that wants `ip` to reach the namespace
// and the Linux capabilities to enter it and open a raw socket. It does NOT
// want nftables or sysctl, which the fabric uses to program filtering and
// forwarding — demanding those would make a host that can capture perfectly
// well report that it cannot.
func DetectCaptureCapabilities() CapabilityReport {
	return DetectCapabilities("ip")
}

func commandsRequireKVM(requiredCommands []string) bool {
	for _, name := range requiredCommands {
		if name == "firecracker" || name == "jailer" {
			return true
		}
	}
	return false
}

func (r CapabilityReport) Require() error {
	if len(r.Missing) == 0 {
		return nil
	}
	return &ErrMissingCapability{Missing: append([]string(nil), r.Missing...)}
}

func linuxEffectiveCapabilities(path string) (uint64, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return 0, fmt.Errorf("malformed CapEff line: %q", line)
		}
		return strconv.ParseUint(fields[1], 16, 64)
	}
	return 0, errors.New("CapEff not found")
}

func hasCapability(mask uint64, capNumber uint) bool {
	return mask&(uint64(1)<<capNumber) != 0
}
