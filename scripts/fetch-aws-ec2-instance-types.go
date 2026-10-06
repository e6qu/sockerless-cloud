//go:build ignore

// fetch-aws-ec2-instance-types writes simulator-aws/ec2_instance_types_vendored.json:
// the Amazon EC2 instance types the AWS Price List bulk offer file for
// us-east-1 lists, with the per-type facts it states.
//
// The facts DescribeInstanceTypes answers are AWS's to publish: the EC2 Smithy
// model carries only the InstanceType enum. The Price List offer file is the
// machine-readable, unauthenticated publication of them. The program streams it
// (it runs to hundreds of megabytes), keeps only the Compute Instance products,
// and fails when two products of one instance type disagree on a fact, so the
// catalog never carries a value the source does not state unambiguously. It
// records the pinned offer version URL and the SHA-256 of the file it read.
//
// Run from the repository root:
//
//	go run scripts/fetch-aws-ec2-instance-types.go
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

	resp, err = get(offerURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	digest := sha256.New()
	dec := json.NewDecoder(io.TeeReader(resp.Body, digest))

	var publicationDate, version string
	byType := map[string]map[string]string{}
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
			if err := readProducts(dec, byType); err != nil {
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

	payload := struct {
		Source          string         `json:"source"`
		Region          string         `json:"region"`
		Version         string         `json:"version"`
		PublicationDate string         `json:"publicationDate"`
		SHA256          string         `json:"sha256"`
		Retrieved       string         `json:"retrieved"`
		Note            string         `json:"note"`
		InstanceTypes   []instanceType `json:"instanceTypes"`
	}{
		Source:          offerURL,
		Region:          region,
		Version:         version,
		PublicationDate: publicationDate,
		SHA256:          hex.EncodeToString(digest.Sum(nil)),
		Retrieved:       time.Now().UTC().Format(time.RFC3339),
		Note: "Every value is an attribute of the offer file's Compute Instance products, verbatim. " +
			"The offer file does not state cores, threads per core, network interface limits, " +
			"supported usage classes or Free Tier eligibility, and the catalog leaves them absent.",
		InstanceTypes: types,
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
	fmt.Printf("%d instance types from %s -> %s\n", len(types), offerURL, out)
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

func readProducts(dec *json.Decoder, byType map[string]map[string]string) error {
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
		if p.ProductFamily != "Compute Instance" && p.ProductFamily != "Compute Instance (bare metal)" {
			continue
		}
		if p.Attributes["locationType"] != "AWS Region" || p.Attributes["regionCode"] != region {
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
