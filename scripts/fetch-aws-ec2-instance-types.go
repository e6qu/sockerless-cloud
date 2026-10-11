//go:build ignore

// fetch-aws-ec2-instance-types writes simulator-aws/ec2_instance_types_vendored.json:
// the Amazon EC2 instance types the AWS Price List bulk offer file for
// us-east-1 lists, with the per-type facts it states, and the Dedicated Host
// families it lists, with each host's vCPUs, physical cores and per-size
// instance capacity.
//
// The facts DescribeInstanceTypes answers are AWS's to publish: the EC2 Smithy
// model carries only the InstanceType enum. The Price List offer file is the
// machine-readable, unauthenticated publication of them. The program streams it
// (it runs to hundreds of megabytes), keeps only the Compute Instance products,
// and fails when two products of one instance type disagree on a fact, so the
// catalog never carries a value the source does not state unambiguously. It
// records the pinned offer version URL and the SHA-256 of the file it read.
//
// Run from the repository root, naming an offer version URL to re-read a
// pinned version rather than the current one:
//
//	go run scripts/fetch-aws-ec2-instance-types.go [offer-version-url]
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	priceListHost = "https://pricing.us-east-1.amazonaws.com"
	regionIndex   = priceListHost + "/offers/v1.0/aws/AmazonEC2/current/region_index.json"
	region        = "us-east-1"
	out           = "simulator-aws/ec2_instance_types_vendored.json"
)

var facts = []string{
	"vcpu", "memory", "processorArchitecture", "physicalProcessor",
	"networkPerformance", "currentGeneration", "storage", "instanceFamily",
}

type product struct {
	ProductFamily string            `json:"productFamily"`
	Attributes    map[string]string `json:"attributes"`
}

// dedicatedHost is one Dedicated Host family: the host's vCPUs and physical
// cores, and how many instances of each size it holds, keyed by the size the
// offer file spells in its instanceCapacity-<size> attributes.
type dedicatedHost struct {
	InstanceFamily   string            `json:"instanceFamily"`
	VCPU             string            `json:"vcpu"`
	PhysicalCores    string            `json:"physicalCores"`
	InstanceCapacity map[string]string `json:"instanceCapacity"`
}

type instanceType struct {
	InstanceType          string `json:"instanceType"`
	VCPU                  string `json:"vcpu"`
	Memory                string `json:"memory"`
	ProcessorArchitecture string `json:"processorArchitecture"`
	PhysicalProcessor     string `json:"physicalProcessor"`
	NetworkPerformance    string `json:"networkPerformance"`
	CurrentGeneration     string `json:"currentGeneration"`
	Storage               string `json:"storage"`
	InstanceFamily        string `json:"instanceFamily"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fetch-aws-ec2-instance-types:", err)
		os.Exit(1)
	}
}

func get(url string) (*http.Response, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func run() error {
	resp, err := get(regionIndex)
	if err != nil {
		return err
	}
	var index struct {
		Regions map[string]struct {
			CurrentVersionURL string `json:"currentVersionUrl"`
		} `json:"regions"`
	}
	err = json.NewDecoder(resp.Body).Decode(&index)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("region index: %w", err)
	}
	offerURL := priceListHost + index.Regions[region].CurrentVersionURL
	if index.Regions[region].CurrentVersionURL == "" {
		return fmt.Errorf("region index lists no offer file for %s", region)
	}
	if len(os.Args) > 1 {
		offerURL = os.Args[1]
	}

	resp, err = get(offerURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	digest := sha256.New()
	dec := json.NewDecoder(io.TeeReader(resp.Body, digest))

	var publicationDate, version string
	byType := map[string]map[string]string{}
	hosts := map[string]map[string]string{}
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		switch key {
		case "publicationDate":
			if err := dec.Decode(&publicationDate); err != nil {
				return err
			}
		case "version":
			if err := dec.Decode(&version); err != nil {
				return err
			}
		case "products":
			if err := readProducts(dec, byType, hosts); err != nil {
				return err
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return err
			}
		}
	}
	if _, err := io.Copy(digest, resp.Body); err != nil {
		return err
	}
	if len(byType) == 0 {
		return fmt.Errorf("the offer file lists no Compute Instance products")
	}

	types := make([]instanceType, 0, len(byType))
	for name, a := range byType {
		types = append(types, instanceType{
			InstanceType: name, VCPU: a["vcpu"], Memory: a["memory"],
			ProcessorArchitecture: a["processorArchitecture"], PhysicalProcessor: a["physicalProcessor"],
			NetworkPerformance: a["networkPerformance"], CurrentGeneration: a["currentGeneration"],
			Storage: a["storage"], InstanceFamily: a["instanceFamily"],
		})
	}
	sort.Slice(types, func(i, j int) bool { return types[i].InstanceType < types[j].InstanceType })

	families := make([]dedicatedHost, 0, len(hosts))
	for family, a := range hosts {
		host := dedicatedHost{InstanceFamily: family, VCPU: a["vcpu"], PhysicalCores: a["physicalCores"],
			InstanceCapacity: map[string]string{}}
		for name, value := range a {
			if size, ok := strings.CutPrefix(name, "instanceCapacity-"); ok {
				host.InstanceCapacity[size] = value
			}
		}
		families = append(families, host)
	}
	sort.Slice(families, func(i, j int) bool { return families[i].InstanceFamily < families[j].InstanceFamily })

	payload := struct {
		Source          string          `json:"source"`
		Region          string          `json:"region"`
		Version         string          `json:"version"`
		PublicationDate string          `json:"publicationDate"`
		SHA256          string          `json:"sha256"`
		Retrieved       string          `json:"retrieved"`
		Note            string          `json:"note"`
		InstanceTypes   []instanceType  `json:"instanceTypes"`
		DedicatedHosts  []dedicatedHost `json:"dedicatedHosts"`
	}{
		Source:          offerURL,
		Region:          region,
		Version:         version,
		PublicationDate: publicationDate,
		SHA256:          hex.EncodeToString(digest.Sum(nil)),
		Retrieved:       time.Now().UTC().Format(time.RFC3339),
		Note: "Every value is an attribute of the offer file's Compute Instance products, verbatim. " +
			"The offer file does not state cores, threads per core, network interface limits, " +
			"supported usage classes or Free Tier eligibility, and the catalog leaves them absent. " +
			"Dedicated Host values are attributes of its Dedicated Host products, verbatim; it states no socket count.",
		InstanceTypes:  types,
		DedicatedHosts: families,
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	if err := enc.Encode(payload); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("%d instance types and %d Dedicated Host families from %s -> %s\n", len(types), len(families), offerURL, out)
	return nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// hostFacts are the Dedicated Host attributes kept besides each
// instanceCapacity-<size>.
var hostFacts = []string{"vcpu", "physicalCores"}

func readProducts(dec *json.Decoder, byType, hosts map[string]map[string]string) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		sku, err := dec.Token()
		if err != nil {
			return err
		}
		var p product
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("product %v: %w", sku, err)
		}
		if p.Attributes["locationType"] != "AWS Region" || p.Attributes["regionCode"] != region {
			continue
		}
		if p.ProductFamily == "Dedicated Host" {
			if err := readDedicatedHost(sku, p, hosts); err != nil {
				return err
			}
			continue
		}
		if p.ProductFamily != "Compute Instance" && p.ProductFamily != "Compute Instance (bare metal)" {
			continue
		}
		name := p.Attributes["instanceType"]
		if name == "" {
			return fmt.Errorf("product %v names no instanceType", sku)
		}
		seen, ok := byType[name]
		if !ok {
			seen = map[string]string{}
			for _, fact := range facts {
				seen[fact] = p.Attributes[fact]
			}
			byType[name] = seen
			continue
		}
		for _, fact := range facts {
			if v := p.Attributes[fact]; v != seen[fact] {
				return fmt.Errorf("instance type %s: product %v states %s %q, another %q", name, sku, fact, v, seen[fact])
			}
		}
	}
	return expectDelim(dec, '}')
}

// readDedicatedHost records one Dedicated Host product's facts under its
// family, failing when two products of a family disagree.
func readDedicatedHost(sku json.Token, p product, hosts map[string]map[string]string) error {
	family := p.Attributes["instanceType"]
	if family == "" {
		return fmt.Errorf("Dedicated Host product %v names no instanceType", sku)
	}
	facts := map[string]string{}
	for _, fact := range hostFacts {
		facts[fact] = p.Attributes[fact]
	}
	for name, value := range p.Attributes {
		if strings.HasPrefix(name, "instanceCapacity-") {
			facts[name] = value
		}
	}
	seen, ok := hosts[family]
	if !ok {
		hosts[family] = facts
		return nil
	}
	if len(seen) != len(facts) {
		return fmt.Errorf("Dedicated Host family %s: product %v states %d facts, another %d", family, sku, len(facts), len(seen))
	}
	for name, value := range facts {
		if seen[name] != value {
			return fmt.Errorf("Dedicated Host family %s: product %v states %s %q, another %q", family, sku, name, value, seen[name])
		}
	}
	return nil
}
