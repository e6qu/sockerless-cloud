package main

import (
	"encoding/json"
	"net/http"
)

func init() {
	registerIAMRequestConditionPopulator("servicequotas", func(_ *http.Request, _ string, body []byte, ctx map[string][]string) {
		var req struct{ ServiceCode string }
		if json.Unmarshal(body, &req) == nil && req.ServiceCode != "" {
			ctx["servicequotas:service"] = []string{req.ServiceCode}
		}
	})
	registerIAMRequestConditionPopulator("pi", func(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
		var input map[string]any
		if json.Unmarshal(body, &input) != nil {
			return
		}
		if dims := piRequestedDimensions(operation, input); len(dims) > 0 {
			ctx["pi:Dimensions"] = dims
		}
	})
}

// piRequestedDimensions are the dimensions a request reads, which
// pi:Dimensions carries: a group's named dimensions, or every dimension of a
// group that names none, and the dimensions GetDimensionKeyDetails asks for.
func piRequestedDimensions(operation string, input map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	group := func(raw any) {
		g, ok := raw.(map[string]any)
		if !ok {
			return
		}
		name, _ := g["Group"].(string)
		dims, _ := g["Dimensions"].([]any)
		if len(dims) == 0 {
			for _, family := range []map[string][]string{piGroups("postgres"), piGroups("mysql")} {
				for _, d := range family[name] {
					add(d)
				}
			}
		}
		for _, d := range dims {
			s, _ := d.(string)
			add(s)
		}
	}
	filter := func(raw any) {
		if f, ok := raw.(map[string]any); ok {
			for d := range f {
				add(d)
			}
		}
	}
	switch operation {
	case "GetResourceMetrics":
		queries, _ := input["MetricQueries"].([]any)
		for _, q := range queries {
			if m, ok := q.(map[string]any); ok {
				group(m["GroupBy"])
				filter(m["Filter"])
			}
		}
	case "DescribeDimensionKeys":
		group(input["GroupBy"])
		group(input["PartitionBy"])
		filter(input["Filter"])
	case "GetDimensionKeyDetails":
		requested, _ := input["RequestedDimensions"].([]any)
		for _, d := range requested {
			s, _ := d.(string)
			add(s)
		}
		if len(requested) == 0 {
			switch input["Group"] {
			case "db.sql":
				add("db.sql.statement")
			case "db.sql_tokenized":
				add("db.sql_tokenized.statement")
			}
		}
	}
	return out
}
