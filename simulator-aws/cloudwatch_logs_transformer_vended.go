package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A built-in parser reads only @message.
func cwtCompileVended(name string, c *cwtSourceOnly, parse func(msg string) (*cwtObj, error)) (cwtStep, error) {
	if c.Source != nil && *c.Source != "@message" {
		return nil, cwtInvalid("%s.source accepts only @message", name)
	}
	return cwtFunc(func(ev *cwtEvent) error {
		obj, err := parse(ev.message)
		if err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
		for _, k := range obj.keys {
			ev.root.Put(k, obj.m[k])
		}
		return nil
	}), nil
}

var (
	cwtIntRE    = regexp.MustCompile(`^-?[0-9]+$`)
	cwtDoubleRE = regexp.MustCompile(`^-?[0-9]+\.[0-9]+$`)
)

// cwtNumberOrString keeps an integer or a double as a JSON number, written as
// the log wrote it.
func cwtNumberOrString(s string) any {
	if cwtIntRE.MatchString(s) || cwtDoubleRE.MatchString(s) {
		return json.Number(s)
	}
	return s
}

var cwtCloudfrontFields = []string{
	"date", "time", "x-edge-location", "sc-bytes", "c-ip", "cs-method", "cs(Host)",
	"cs-uri-stem", "sc-status", "cs(Referer)", "cs(User-Agent)", "cs-uri-query",
	"cs(Cookie)", "x-edge-result-type", "x-edge-request-id", "x-host-header",
	"cs-protocol", "cs-bytes", "time-taken", "x-forwarded-for", "ssl-protocol",
	"ssl-cipher", "x-edge-response-result-type", "cs-protocol-version", "fle-status",
	"fle-encrypted-fields", "c-port", "time-to-first-byte", "x-edge-detailed-result-type",
	"sc-content-type", "sc-content-len", "sc-range-start", "sc-range-end",
}

// CloudFront standard logs URL-encode their values.
func cwtParseCloudfrontEvent(msg string) (*cwtObj, error) {
	values := strings.Fields(msg)
	if len(values) != len(cwtCloudfrontFields) {
		return nil, fmt.Errorf("expected %d fields, found %d", len(cwtCloudfrontFields), len(values))
	}
	out := newCWTObj()
	for i, raw := range values {
		v := raw
		if decoded, err := url.PathUnescape(raw); err == nil {
			v = decoded
		}
		out.Put(cwtCloudfrontFields[i], cwtNumberOrString(v))
	}
	return out, nil
}

var cwtRoute53Fields = []string{
	"version", "queryTimestamp", "hostZoneId", "queryName", "queryType",
	"responseCode", "protocol", "edgeLocation", "resolverIp", "ednsClientSubnet",
}

var cwtRoute53OctalRE = regexp.MustCompile(`\\[0-7]{3}`)

// Route 53 query logs write a character outside printable ASCII in a query
// name as a backslash and three octal digits.
func cwtParseRoute53Event(msg string) (*cwtObj, error) {
	values := strings.Fields(msg)
	if len(values) != len(cwtRoute53Fields) {
		return nil, fmt.Errorf("expected %d fields, found %d", len(cwtRoute53Fields), len(values))
	}
	out := newCWTObj()
	for i, v := range values {
		name := cwtRoute53Fields[i]
		switch name {
		case "version":
			out.Put(name, cwtNumberOrString(v))
		case "queryName":
			out.Put(name, cwtRoute53OctalRE.ReplaceAllStringFunc(v, func(m string) string {
				n, err := strconv.ParseUint(m[1:], 8, 8)
				if err != nil {
					return m
				}
				return string([]byte{byte(n)})
			}))
		default:
			out.Put(name, v)
		}
	}
	return out, nil
}

var cwtVPCFields = []struct {
	name    string
	numeric bool
}{
	{"version", true}, {"accountId", false}, {"interfaceId", false}, {"srcAddr", false},
	{"dstAddr", false}, {"srcPort", true}, {"dstPort", true}, {"protocol", true},
	{"packets", true}, {"bytes", true}, {"start", true}, {"end", true},
	{"action", false}, {"logStatus", false},
}

// parseVPC reads the default flow log format.
func cwtParseVPCEvent(msg string) (*cwtObj, error) {
	values := strings.Fields(msg)
	if len(values) != len(cwtVPCFields) {
		return nil, fmt.Errorf("expected the %d fields of the default flow log format, found %d", len(cwtVPCFields), len(values))
	}
	out := newCWTObj()
	for i, v := range values {
		f := cwtVPCFields[i]
		if f.numeric {
			out.Put(f.name, cwtNumberOrString(v))
		} else {
			out.Put(f.name, v)
		}
	}
	return out, nil
}

// The RDS for PostgreSQL default log_line_prefix, %t:%r:%u@%d:[%p]:, followed
// by the message's severity.
var cwtPostgresRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)? [A-Za-z0-9+-]+):([^:]*):([^@:]*)@([^:]*):\[(\d+)\]:([A-Z0-9]+):`)

func cwtParsePostgresEvent(msg string) (*cwtObj, error) {
	m := cwtPostgresRE.FindStringSubmatch(msg)
	if m == nil {
		return nil, fmt.Errorf("the message does not start with the RDS for PostgreSQL log line prefix")
	}
	out := newCWTObj()
	for i, name := range []string{"logTime", "srcIp", "userName", "dbName", "processId", "logLevel"} {
		out.Put(name, m[i+1])
	}
	return out, nil
}

// parseWAF turns httpRequest.headers into an object of header names and
// values, and labels into one object, then keeps the rest of the record.
func cwtParseWAFEvent(msg string) (*cwtObj, error) {
	v, err := cwtDecode(msg)
	if err != nil {
		return nil, fmt.Errorf("the message is not valid JSON: %v", err)
	}
	rec, isObj := v.(*cwtObj)
	if !isObj {
		return nil, fmt.Errorf("the message is not a JSON object")
	}
	out := newCWTObj()
	if hr, ok := rec.Get("httpRequest"); ok {
		if req, isReq := hr.(*cwtObj); isReq {
			newReq := newCWTObj()
			if hv, ok := req.Get("headers"); ok {
				newReq.Put("headers", cwtNameValueObject(hv))
			}
			for _, k := range req.keys {
				if k != "headers" {
					newReq.Put(k, req.m[k])
				}
			}
			out.Put("httpRequest", newReq)
		}
	}
	if lv, ok := rec.Get("labels"); ok {
		labels := newCWTObj()
		if list, isList := lv.([]any); isList {
			for _, item := range list {
				if obj, isObj := item.(*cwtObj); isObj {
					for _, k := range obj.keys {
						labels.Put(k, obj.m[k])
					}
				}
			}
			out.Put("labels", labels)
		} else {
			out.Put("labels", lv)
		}
	}
	for _, k := range rec.keys {
		if _, done := out.Get(k); !done {
			out.Put(k, rec.m[k])
		}
	}
	return out, nil
}

func cwtNameValueObject(v any) any {
	list, isList := v.([]any)
	if !isList {
		return v
	}
	out := newCWTObj()
	for _, item := range list {
		obj, isObj := item.(*cwtObj)
		if !isObj {
			continue
		}
		name, okName := obj.Get("name")
		value, okValue := obj.Get("value")
		if n, isStr := name.(string); okName && okValue && isStr {
			out.Put(n, value)
		}
	}
	return out
}
