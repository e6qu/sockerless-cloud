//go:build ignore

// generate-sfn-rest-integrations compiles the vendored AWS Smithy HTTP traits
// into the small routing table used by generic AWS Step Functions AWS SDK
// integrations. Run from the repository root:
//
//	go run scripts/generate-sfn-rest-integrations.go
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type shape struct {
	Type    string                     `json:"type"`
	Input   *target                    `json:"input"`
	Output  *target                    `json:"output"`
	Member  *memberRef                 `json:"member"`
	Key     *memberRef                 `json:"key"`
	Value   *memberRef                 `json:"value"`
	Members map[string]memberRef       `json:"members"`
	Traits  map[string]json.RawMessage `json:"traits"`
}

type target struct {
	Target string `json:"target"`
}

type memberRef struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits"`
}

type binding struct {
	Name       string
	WireName   string
	Location   string
	Target     string
	TargetType string
	XMLName    string
	Flattened  bool
}

type operation struct {
	Name           string
	Method         string
	URI            string
	InputTarget    string
	Bindings       []binding
	OutputBindings []binding
}

var models = map[string]string{
	"amplify":      "amplify.smithy.json.gz",
	"apigateway":   "api-gateway.smithy.json.gz",
	"apigatewayv2": "apigatewayv2.smithy.json.gz",
	"batch":        "batch.smithy.json.gz",
	"efs":          "efs.smithy.json.gz",
	"lambda":       "lambda.smithy.json.gz",
	"scheduler":    "scheduler.smithy.json.gz",
}

var restXMLModels = map[string]string{
	"cloudfront": "cloudfront.smithy.json.gz",
	"route53":    "route-53.smithy.json.gz",
	"s3":         "s3.smithy.json.gz",
}

func traitString(traits map[string]json.RawMessage, name string) string {
	raw, ok := traits[name]
	if !ok {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func hasTrait(traits map[string]json.RawMessage, name string) bool {
	_, ok := traits[name]
	return ok
}

func short(id string) string {
	if index := strings.LastIndexByte(id, '#'); index >= 0 {
		return id[index+1:]
	}
	return id
}

func load(path string) (map[string]shape, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var document struct {
		Shapes map[string]shape `json:"shapes"`
	}
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		return nil, err
	}
	return document.Shapes, nil
}

func bindings(shapes map[string]shape, targetID string) []binding {
	var result []binding
	input := shapes[targetID]
	for name, ref := range input.Members {
		wireName := traitString(ref.Traits, "smithy.api#jsonName")
		if wireName == "" {
			wireName = name
		}
		location := "document"
		switch {
		case hasTrait(ref.Traits, "smithy.api#httpLabel"):
			location = "label"
		case traitString(ref.Traits, "smithy.api#httpQuery") != "":
			location = "query"
			wireName = traitString(ref.Traits, "smithy.api#httpQuery")
		case traitString(ref.Traits, "smithy.api#httpHeader") != "":
			location = "header"
			wireName = traitString(ref.Traits, "smithy.api#httpHeader")
		case hasTrait(ref.Traits, "smithy.api#httpQueryParams"):
			location = "queryParams"
		case hasTrait(ref.Traits, "smithy.api#httpPayload"):
			location = "payload"
		}
		result = append(result, binding{
			Name: name, WireName: wireName, Location: location,
			Target: short(ref.Target), TargetType: shapes[ref.Target].Type,
			XMLName:   traitString(ref.Traits, "smithy.api#xmlName"),
			Flattened: hasTrait(ref.Traits, "smithy.api#xmlFlattened"),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func operations(shapes map[string]shape) []operation {
	var result []operation
	for id, candidate := range shapes {
		if candidate.Type != "operation" || candidate.Input == nil {
			continue
		}
		rawHTTP, ok := candidate.Traits["smithy.api#http"]
		if !ok {
			continue
		}
		var httpTrait struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
		}
		if json.Unmarshal(rawHTTP, &httpTrait) != nil {
			continue
		}
		op := operation{
			Name: short(id), Method: httpTrait.Method, URI: httpTrait.URI,
			InputTarget: short(candidate.Input.Target),
			Bindings:    bindings(shapes, candidate.Input.Target),
		}
		if candidate.Output != nil {
			op.OutputBindings = bindings(shapes, candidate.Output.Target)
		}
		result = append(result, op)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func quote(value string) string { return strconv.Quote(value) }

func emitOperations(source *strings.Builder, variable string, sourceModels map[string]string) {
	fmt.Fprintf(source, "var %s = map[string]map[string]sfnRESTOperation{\n", variable)
	services := make([]string, 0, len(sourceModels))
	for service := range sourceModels {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		shapes, err := load(filepath.Join("specs", "cloud-api", "aws", sourceModels[service]))
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(source, "\t%s: {\n", quote(service))
		for _, op := range operations(shapes) {
			fmt.Fprintf(source, "\t\t%s: {Method: %s, URI: %s, InputTarget: %s, Bindings: []sfnRESTBinding{\n",
				quote(strings.ToLower(op.Name[:1])+op.Name[1:]), quote(op.Method), quote(op.URI), quote(op.InputTarget))
			for _, member := range op.Bindings {
				fmt.Fprintf(source, "\t\t\t{Name: %s, WireName: %s, Location: %s, Target: %s, TargetType: %s, XMLName: %s, Flattened: %t},\n",
					quote(member.Name), quote(member.WireName), quote(member.Location), quote(member.Target),
					quote(member.TargetType), quote(member.XMLName), member.Flattened)
			}
			source.WriteString("\t\t}, OutputBindings: []sfnRESTBinding{\n")
			for _, member := range op.OutputBindings {
				fmt.Fprintf(source, "\t\t\t{Name: %s, WireName: %s, Location: %s, Target: %s, TargetType: %s, XMLName: %s, Flattened: %t},\n",
					quote(member.Name), quote(member.WireName), quote(member.Location), quote(member.Target),
					quote(member.TargetType), quote(member.XMLName), member.Flattened)
			}
			source.WriteString("\t\t}},\n")
		}
		source.WriteString("\t},\n")
	}
	source.WriteString("}\n\n")
}

func emitRESTXMLShapes(source *strings.Builder) {
	source.WriteString("var sfnAWSRESTXMLShapes = map[string]map[string]sfnRESTXMLShape{\n")
	services := make([]string, 0, len(restXMLModels))
	for service := range restXMLModels {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		shapes, err := load(filepath.Join("specs", "cloud-api", "aws", restXMLModels[service]))
		if err != nil {
			panic(err)
		}
		ids := make([]string, 0, len(shapes))
		for id := range shapes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Fprintf(source, "\t%s: {\n", quote(service))
		for _, id := range ids {
			candidate := shapes[id]
			if candidate.Type == "service" || candidate.Type == "operation" || candidate.Type == "resource" {
				continue
			}
			fmt.Fprintf(source, "\t\t%s: {Type: %s, XMLName: %s",
				quote(short(id)), quote(candidate.Type), quote(traitString(candidate.Traits, "smithy.api#xmlName")))
			if candidate.Member != nil {
				fmt.Fprintf(source, ", Member: sfnRESTXMLMember{Name: %s, Target: %s, XMLName: %s, Flattened: %t}",
					quote("member"), quote(short(candidate.Member.Target)),
					quote(traitString(candidate.Member.Traits, "smithy.api#xmlName")),
					hasTrait(candidate.Member.Traits, "smithy.api#xmlFlattened"))
			}
			if candidate.Key != nil {
				fmt.Fprintf(source, ", Key: sfnRESTXMLMember{Name: %s, Target: %s, XMLName: %s}",
					quote("key"), quote(short(candidate.Key.Target)),
					quote(traitString(candidate.Key.Traits, "smithy.api#xmlName")))
			}
			if candidate.Value != nil {
				fmt.Fprintf(source, ", Value: sfnRESTXMLMember{Name: %s, Target: %s, XMLName: %s}",
					quote("value"), quote(short(candidate.Value.Target)),
					quote(traitString(candidate.Value.Traits, "smithy.api#xmlName")))
			}
			if len(candidate.Members) > 0 {
				source.WriteString(", Members: []sfnRESTXMLMember{\n")
				names := make([]string, 0, len(candidate.Members))
				for name := range candidate.Members {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					member := candidate.Members[name]
					fmt.Fprintf(source, "\t\t\t{Name: %s, Target: %s, XMLName: %s, Flattened: %t},\n",
						quote(name), quote(short(member.Target)),
						quote(traitString(member.Traits, "smithy.api#xmlName")),
						hasTrait(member.Traits, "smithy.api#xmlFlattened"))
				}
				source.WriteString("\t\t}")
			}
			source.WriteString("},\n")
		}
		source.WriteString("\t},\n")
	}
	source.WriteString("}\n")
}

func main() {
	var source strings.Builder
	source.WriteString("// Code generated by scripts/generate-sfn-rest-integrations.go; DO NOT EDIT.\n\n")
	source.WriteString("package main\n\n")
	emitOperations(&source, "sfnAWSRESTJSONOperations", models)
	emitOperations(&source, "sfnAWSRESTXMLOperations", restXMLModels)
	emitRESTXMLShapes(&source)
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		panic(err)
	}
	output := filepath.Join("simulator-aws", "stepfunctions_rest_integrations_gen.go")
	if err := os.WriteFile(output, formatted, 0o644); err != nil {
		panic(err)
	}
}
