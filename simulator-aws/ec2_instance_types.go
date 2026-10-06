package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ec2_instance_types_vendored.json is the AWS Price List offer file's Compute
// Instance products for us-east-1, written by
// scripts/fetch-aws-ec2-instance-types.go with the offer version and checksum
// it read.
//
//go:embed ec2_instance_types_vendored.json
var ec2InstanceTypesJSON []byte

// ec2InstanceTypeFacts is what DescribeInstanceTypes answers for one instance
// type: the facts the Price List states, converted to the API's units.
type ec2InstanceTypeFacts struct {
	Name                     string
	VCpus                    int
	MemoryMiB                int
	Architectures            []string
	NetworkPerformance       string
	CurrentGeneration        bool
	InstanceStorageSupported bool
}

var ec2InstanceTypeCatalog = sync.OnceValues(func() ([]ec2InstanceTypeFacts, map[string]ec2InstanceTypeFacts) {
	var vendored struct {
		InstanceTypes []struct {
			InstanceType          string `json:"instanceType"`
			VCPU                  string `json:"vcpu"`
			Memory                string `json:"memory"`
			ProcessorArchitecture string `json:"processorArchitecture"`
			PhysicalProcessor     string `json:"physicalProcessor"`
			NetworkPerformance    string `json:"networkPerformance"`
			CurrentGeneration     string `json:"currentGeneration"`
			Storage               string `json:"storage"`
		} `json:"instanceTypes"`
	}
	if err := json.Unmarshal(ec2InstanceTypesJSON, &vendored); err != nil {
		panic("vendored Amazon EC2 instance-type catalog is not valid JSON: " + err.Error())
	}
	list := make([]ec2InstanceTypeFacts, 0, len(vendored.InstanceTypes))
	byName := make(map[string]ec2InstanceTypeFacts, len(vendored.InstanceTypes))
	for _, t := range vendored.InstanceTypes {
		vcpus, err := strconv.Atoi(t.VCPU)
		if err != nil {
			panic(fmt.Sprintf("vendored instance type %s states vcpu %q", t.InstanceType, t.VCPU))
		}
		memory, err := ec2PriceListMemoryMiB(t.Memory)
		if err != nil {
			panic(fmt.Sprintf("vendored instance type %s: %v", t.InstanceType, err))
		}
		facts := ec2InstanceTypeFacts{
			Name:                     t.InstanceType,
			VCpus:                    vcpus,
			MemoryMiB:                memory,
			Architectures:            ec2PriceListArchitectures(t.InstanceType, t.ProcessorArchitecture, t.PhysicalProcessor),
			CurrentGeneration:        t.CurrentGeneration == "Yes",
			InstanceStorageSupported: t.Storage != "EBS only",
		}
		if t.NetworkPerformance != "NA" {
			facts.NetworkPerformance = t.NetworkPerformance
		}
		list = append(list, facts)
		byName[facts.Name] = facts
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, byName
})

func ec2LookupInstanceType(name string) (ec2InstanceTypeFacts, bool) {
	_, byName := ec2InstanceTypeCatalog()
	facts, ok := byName[name]
	return facts, ok
}

// ec2PriceListMemoryMiB converts the Price List's "<n> GiB" to MiB. The Price
// List rounds a few legacy sizes to three decimals of a GiB; truncating gives
// the MiB figure DescribeInstanceTypes reports for them (t1.micro's 0.613 GiB is
// 627 MiB).
func ec2PriceListMemoryMiB(memory string) (int, error) {
	gib, ok := strings.CutSuffix(memory, " GiB")
	if !ok {
		return 0, fmt.Errorf("memory %q is not in GiB", memory)
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(gib, ",", ""), 64)
	if err != nil {
		return 0, fmt.Errorf("memory %q: %w", memory, err)
	}
	return int(math.Floor(value*1024 + 1e-6)), nil
}

// ec2PriceListArchitectures maps the Price List's processor attributes onto
// DescribeInstanceTypes' supportedArchitectures. The Price List says "64-bit"
// for both instruction sets, so the processor names the set: AWS Graviton and
// Apple silicon are Arm, everything else is x86, and an Amazon EC2 Mac instance
// type carries the `_mac` variant.
func ec2PriceListArchitectures(instanceType, processorArchitecture, physicalProcessor string) []string {
	arch := "x86_64"
	if strings.Contains(physicalProcessor, "Graviton") || strings.HasPrefix(physicalProcessor, "Apple") {
		arch = "arm64"
	}
	if strings.HasPrefix(instanceType, "mac") {
		return []string{arch + "_mac"}
	}
	if processorArchitecture == "32-bit or 64-bit" {
		return []string{"i386", arch}
	}
	return []string{arch}
}

func ec2InstanceTypeXML(t ec2InstanceTypeFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<item><instanceType>%s</instanceType><currentGeneration>%t</currentGeneration>", xmlEscape(t.Name), t.CurrentGeneration)
	b.WriteString("<processorInfo><supportedArchitectures>")
	for _, arch := range t.Architectures {
		fmt.Fprintf(&b, "<item>%s</item>", arch)
	}
	b.WriteString("</supportedArchitectures></processorInfo>")
	fmt.Fprintf(&b, "<vCpuInfo><defaultVCpus>%d</defaultVCpus></vCpuInfo><memoryInfo><sizeInMiB>%d</sizeInMiB></memoryInfo>", t.VCpus, t.MemoryMiB)
	fmt.Fprintf(&b, "<instanceStorageSupported>%t</instanceStorageSupported>", t.InstanceStorageSupported)
	if t.NetworkPerformance != "" {
		fmt.Fprintf(&b, "<networkInfo><networkPerformance>%s</networkPerformance></networkInfo>", xmlEscape(t.NetworkPerformance))
	}
	b.WriteString("</item>")
	return b.String()
}

// ec2InstanceTypeFilters are the DescribeInstanceTypes filters over the facts
// the catalog carries. A value matches when any of the filter's values does.
var ec2InstanceTypeFilters = map[string]func(t ec2InstanceTypeFacts, value string) bool{
	"instance-type": func(t ec2InstanceTypeFacts, v string) bool { return awsWildcardMatch(v, t.Name) },
	"current-generation": func(t ec2InstanceTypeFacts, v string) bool {
		return v == strconv.FormatBool(t.CurrentGeneration)
	},
	"vcpu-info.default-vcpus": func(t ec2InstanceTypeFacts, v string) bool { return v == strconv.Itoa(t.VCpus) },
	"memory-info.size-in-mib": func(t ec2InstanceTypeFacts, v string) bool { return v == strconv.Itoa(t.MemoryMiB) },
	"processor-info.supported-architecture": func(t ec2InstanceTypeFacts, v string) bool {
		return ec2StrInValues(v, t.Architectures)
	},
	"network-info.network-performance": func(t ec2InstanceTypeFacts, v string) bool { return v == t.NetworkPerformance },
	"instance-storage-supported": func(t ec2InstanceTypeFacts, v string) bool {
		return v == strconv.FormatBool(t.InstanceStorageSupported)
	},
}

// ec2InstanceTypeUncarriedFilters are DescribeInstanceTypes filters the Smithy
// model documents over facts the Price List does not state.
var ec2InstanceTypeUncarriedFilters = []string{
	"auto-recovery-supported", "bare-metal", "burstable-performance-supported", "dedicated-hosts-supported",
	"ebs-info.attachment-limit-type", "ebs-info.maximum-ebs-attachments",
	"ebs-info.ebs-optimized-info.baseline-bandwidth-in-mbps", "ebs-info.ebs-optimized-info.baseline-iops",
	"ebs-info.ebs-optimized-info.baseline-throughput-in-mbps", "ebs-info.ebs-optimized-info.maximum-bandwidth-in-mbps",
	"ebs-info.ebs-optimized-info.maximum-iops", "ebs-info.ebs-optimized-info.maximum-throughput-in-mbps",
	"ebs-info.ebs-optimized-support", "ebs-info.encryption-support", "ebs-info.nvme-support",
	"free-tier-eligible", "hibernation-supported", "hypervisor",
	"instance-storage-info.disk.count", "instance-storage-info.disk.size-in-gb", "instance-storage-info.disk.type",
	"instance-storage-info.encryption-support", "instance-storage-info.nvme-support", "instance-storage-info.total-size-in-gb",
	"network-info.bandwidth-weightings", "network-info.efa-info.maximum-efa-interfaces", "network-info.efa-supported",
	"network-info.ena-support", "network-info.flexible-ena-queues-support", "network-info.encryption-in-transit-supported",
	"network-info.ipv4-addresses-per-interface", "network-info.ipv6-addresses-per-interface", "network-info.ipv6-supported",
	"network-info.maximum-network-cards", "network-info.maximum-network-interfaces",
	"nitro-enclaves-support", "nitro-tpm-support", "nitro-tpm-info.supported-versions",
	"processor-info.sustained-clock-speed-in-ghz", "processor-info.supported-features",
	"reboot-migration-support", "supported-boot-mode", "supported-root-device-type", "supported-usage-class",
	"supported-virtualization-type", "vcpu-info.default-cores", "vcpu-info.default-threads-per-core",
	"vcpu-info.valid-cores", "vcpu-info.valid-threads-per-core",
}

func handleDescribeInstanceTypes(w http.ResponseWriter, r *http.Request) {
	if raw := r.FormValue("MaxResults"); raw != "" {
		if n, err := strconv.Atoi(raw); err != nil || n < 5 || n > 100 {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value ( %s ) for parameter maxResults is invalid. Parameter must be between 5 and 100.", raw), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	for name := range filters {
		if _, ok := ec2InstanceTypeFilters[name]; ok {
			continue
		}
		if ec2StrInValues(name, ec2InstanceTypeUncarriedFilters) {
			ec2ErrorXML(w, "Unsupported", fmt.Sprintf("The filter '%s' is not supported: the instance-type catalog does not carry the fact it selects on.", name), http.StatusBadRequest)
			return
		}
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The filter '%s' is invalid", name), http.StatusBadRequest)
		return
	}

	all, _ := ec2InstanceTypeCatalog()
	candidates := all
	if requested := ec2ParamList(r, "InstanceType"); len(requested) > 0 {
		candidates = nil
		var missing []string
		for _, name := range requested {
			t, ok := ec2LookupInstanceType(name)
			if !ok {
				missing = append(missing, name)
				continue
			}
			candidates = append(candidates, t)
		}
		if len(missing) > 0 {
			ec2ErrorXML(w, "InvalidInstanceType", fmt.Sprintf("The following supplied instance types do not exist: [%s]", strings.Join(missing, ", ")), http.StatusBadRequest)
			return
		}
	}
	var matched []ec2InstanceTypeFacts
	for _, t := range candidates {
		if ec2InstanceTypeMatchesFilters(t, filters) {
			matched = append(matched, t)
		}
	}
	page, next, ok := awsPage(w, ec2BadToken, matched, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 100)
	if !ok {
		return
	}
	var items strings.Builder
	for _, t := range page {
		items.WriteString(ec2InstanceTypeXML(t))
	}
	nextXML := ""
	if next != "" {
		nextXML = "<nextToken>" + xmlEscape(next) + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceTypesResponse %s><requestId>%s</requestId><instanceTypeSet>%s</instanceTypeSet>%s</DescribeInstanceTypesResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String(), nextXML)
}

func ec2InstanceTypeMatchesFilters(t ec2InstanceTypeFacts, filters map[string][]string) bool {
	for name, values := range filters {
		match := ec2InstanceTypeFilters[name]
		matched := false
		for _, v := range values {
			if match(t, v) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
