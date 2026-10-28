package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	_ "cloud.google.com/go/bigtable/apiv2/bigtablepb"
	_ "cloud.google.com/go/firestore/apiv1/firestorepb"
	_ "cloud.google.com/go/spanner/apiv1/spannerpb"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// The RPCs the IAM gate derives a permission from when the call is a gRPC
// call or reaches an API defined by its protocol buffers: every RPC of the
// audited One Platform services, and of the data-plane services Google does
// not audit through them.

var gateRPCServices = append(append([]auditAPI{}, auditOnePlatformServices...),
	auditAPI{service: "google.firestore.v1.Firestore"},
	auditAPI{service: "google.spanner.v1.Spanner", restPrefix: "/spanner"},
	auditAPI{service: "google.bigtable.v2.Bigtable"},
)

type gateRPCIndex struct {
	byName   map[string]*auditRPC
	bindings []*auditHTTPBinding
}

var gateRPCs = sync.OnceValues(func() (*gateRPCIndex, error) {
	idx := &gateRPCIndex{byName: map[string]*auditRPC{}}
	for _, api := range gateRPCServices {
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(api.service)
		if err != nil {
			return nil, fmt.Errorf("iam gate: service %s: %w", api.service, err)
		}
		service, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			return nil, fmt.Errorf("iam gate: %s is not a service", api.service)
		}
		host, _ := proto.GetExtension(service.Options(), annotations.E_DefaultHost).(string)
		methods := service.Methods()
		for i := range methods.Len() {
			m := methods.Get(i)
			rpc := &auditRPC{method: m, serviceName: host, methodName: string(m.FullName())}
			if api.methodName != nil {
				rpc.methodName = api.methodName(m)
			}
			rpc.vendoredKey = rpc.methodName
			if api.vendoredKey != nil {
				rpc.vendoredKey = api.vendoredKey(m)
			}
			if rule, _ := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule); rule != nil {
				for _, r := range append([]*annotations.HttpRule{rule}, rule.GetAdditionalBindings()...) {
					binding, err := auditCompileBinding(rpc, r, api.restPrefix)
					if err != nil {
						return nil, fmt.Errorf("iam gate: %s: %w", m.FullName(), err)
					}
					rpc.bindings = append(rpc.bindings, binding)
					idx.bindings = append(idx.bindings, binding)
				}
			}
			idx.byName[string(m.FullName())] = rpc
		}
	}
	return idx, nil
})

// gateMatchRPC is the RPC of the given API a REST call reaches through its
// google.api.http binding, with the variables the path binds.
func gateMatchRPC(host, httpMethod, escapedPath string) (*auditRPC, *auditHTTPBinding, map[string]string) {
	idx, err := gateRPCs()
	if err != nil {
		return nil, nil, nil
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	custom := ""
	if last := parts[len(parts)-1]; auditCustomMethodSuffix(last) {
		i := strings.LastIndex(last, ":")
		custom = last[i+1:]
		parts = append(parts[:len(parts)-1:len(parts)-1], last[:i])
	}
	var best *auditHTTPBinding
	var bestVars map[string]string
	for _, b := range idx.bindings {
		if b.rpc.serviceName != host {
			continue
		}
		vars, ok := b.match(httpMethod, parts, custom)
		if ok && (best == nil || b.literals > best.literals) {
			best, bestVars = b, vars
		}
	}
	if best == nil {
		return nil, nil, nil
	}
	return best.rpc, best, bestVars
}

// gateRESTOf is the REST call a gRPC request is: the HTTP method and path of
// the RPC's binding its fields fill, the way a REST client would send it.
func gateRESTOf(rpc *auditRPC, request map[string]any) (string, string, bool) {
	binding := rpc.auditBindingFor(request)
	if binding == nil {
		return "", "", false
	}
	input := rpc.method.Input()
	var parts []string
	for i, segment := range binding.segments {
		if segment.varIdx < 0 {
			parts = append(parts, segment.literal)
			continue
		}
		v := binding.vars[segment.varIdx]
		if v.first != i {
			continue
		}
		value := auditGetPath(request, auditJSONPath(input, v.field))
		if value == "" {
			return "", "", false
		}
		for _, piece := range strings.Split(value, "/") {
			parts = append(parts, url.PathEscape(piece))
		}
	}
	path := "/" + strings.Join(parts, "/")
	if binding.custom != "" {
		path += ":" + binding.custom
	}
	return binding.verb, path, true
}

// gateStreamingREST spells the RPCs without an HTTP binding as the REST
// method they stand for: StreamingPull pulls from the subscription it names.
var gateStreamingREST = map[string]func(map[string]any) (string, string, bool){
	"google.pubsub.v1.Subscriber.StreamingPull": func(request map[string]any) (string, string, bool) {
		subscription, _ := request["subscription"].(string)
		return http.MethodPost, "/v1/" + subscription + ":pull", subscription != ""
	},
}
