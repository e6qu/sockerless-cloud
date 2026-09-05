package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/fxamacker/cbor/v2"
)

// CloudWatch dashboards (PutDashboard / GetDashboard / ListDashboards /
// DeleteDashboards). A dashboard is just a name → JSON-body record; the API
// validates the body and returns an (empty) DashboardValidationMessages list.
// Reachable over all three CloudWatch wire protocols, like the metric + alarm
// surfaces: query (older botocore), awsJson1.0 (newer CLI), rpc-v2-cbor (Go SDK
// / terraform's aws_cloudwatch_dashboard).

type CWDashboard struct {
	Name         string `json:"DashboardName" cbor:"DashboardName"`
	Body         string `json:"DashboardBody" cbor:"DashboardBody"`
	LastModified string `json:"-" cbor:"-"`
}

var cwDashboards sim.Store[CWDashboard]

func cwDashboardArn(name string) string {
	// Dashboard ARNs are region-less.
	return fmt.Sprintf("arn:aws:cloudwatch::%s:dashboard/%s", awsAccountID(), name)
}

func cwPutDashboard(name, body string) {
	cwDashboards.Put(name, CWDashboard{Name: name, Body: body, LastModified: time.Now().UTC().Format(time.RFC3339)})
}

func cwListDashboardEntries(prefix string) []map[string]any {
	names := make([]string, 0)
	for _, d := range cwDashboards.List() {
		if prefix == "" || strings.HasPrefix(d.Name, prefix) {
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		d, _ := cwDashboards.Get(n)
		// LastModified must encode as a timestamp (cbor tag-1 / RFC3339 JSON),
		// not a bare string, or the SDK's *time.Time field fails to decode.
		lm, err := time.Parse(time.RFC3339, d.LastModified)
		if err != nil {
			lm = time.Now().UTC()
		}
		out = append(out, map[string]any{
			"DashboardName": d.Name,
			"DashboardArn":  cwDashboardArn(d.Name),
			"LastModified":  lm,
			"Size":          len(d.Body),
		})
	}
	return out
}

// ── awsJson1.0 (aws CLI) ────────────────────────────────────────────────────

func registerCloudWatchDashboardsJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutDashboard", handleCWJSONPutDashboard)
	r.Register("GraniteServiceVersion20100801.GetDashboard", handleCWJSONGetDashboard)
	r.Register("GraniteServiceVersion20100801.ListDashboards", handleCWJSONListDashboards)
	r.Register("GraniteServiceVersion20100801.DeleteDashboards", handleCWJSONDeleteDashboards)
}

func handleCWJSONPutDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardName string `json:"DashboardName"`
		DashboardBody string `json:"DashboardBody"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.DashboardName == "" {
		AWSError(w, "MissingParameter", "The parameter DashboardName is required.", http.StatusBadRequest)
		return
	}
	cwPutDashboard(req.DashboardName, req.DashboardBody)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DashboardValidationMessages": []any{}})
}

func handleCWJSONGetDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardName string `json:"DashboardName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	d, ok := cwDashboards.Get(req.DashboardName)
	if !ok {
		AWSErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Dashboard %s does not exist", req.DashboardName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"DashboardName": d.Name,
		"DashboardArn":  cwDashboardArn(d.Name),
		"DashboardBody": d.Body,
	})
}

func handleCWJSONListDashboards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardNamePrefix string `json:"DashboardNamePrefix"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "The request body is not valid JSON.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DashboardEntries": cwListDashboardEntries(req.DashboardNamePrefix)})
}

func handleCWJSONDeleteDashboards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardNames []string `json:"DashboardNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	for _, n := range req.DashboardNames {
		if _, ok := cwDashboards.Get(n); !ok {
			AWSErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Dashboard %s does not exist", n)
			return
		}
	}
	for _, n := range req.DashboardNames {
		cwDashboards.Delete(n)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ── rpc-v2-cbor (Go SDK / terraform) ────────────────────────────────────────

func registerCloudWatchDashboardsCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutDashboard", handleCWCBORPutDashboard)
	cwCBOR(srv, "GetDashboard", handleCWCBORGetDashboard)
	cwCBOR(srv, "ListDashboards", handleCWCBORListDashboards)
	cwCBOR(srv, "DeleteDashboards", handleCWCBORDeleteDashboards)
}

func handleCWCBORPutDashboard(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		DashboardName string `cbor:"DashboardName"`
		DashboardBody string `cbor:"DashboardBody"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	if req.DashboardName == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter DashboardName is required.", http.StatusBadRequest)
		return
	}
	cwPutDashboard(req.DashboardName, req.DashboardBody)
	cwWriteCBOR(w, map[string]any{"DashboardValidationMessages": []any{}})
}

func handleCWCBORGetDashboard(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		DashboardName string `cbor:"DashboardName"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	d, ok := cwDashboards.Get(req.DashboardName)
	if !ok {
		cwWriteCBORErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Dashboard %s does not exist", req.DashboardName)
		return
	}
	cwWriteCBOR(w, map[string]any{
		"DashboardName": d.Name,
		"DashboardArn":  cwDashboardArn(d.Name),
		"DashboardBody": d.Body,
	})
}

func handleCWCBORListDashboards(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		DashboardNamePrefix string `cbor:"DashboardNamePrefix"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	cwWriteCBOR(w, map[string]any{"DashboardEntries": cwListDashboardEntries(req.DashboardNamePrefix)})
}

func handleCWCBORDeleteDashboards(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		DashboardNames []string `cbor:"DashboardNames"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	for _, n := range req.DashboardNames {
		if _, ok := cwDashboards.Get(n); !ok {
			cwWriteCBORErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Dashboard %s does not exist", n)
			return
		}
	}
	for _, n := range req.DashboardNames {
		cwDashboards.Delete(n)
	}
	cwWriteCBOR(w, map[string]any{})
}

// ── query (botocore / older aws CLI) ────────────────────────────────────────

func registerCloudWatchDashboardsQuery(r *AWSQueryRouter) {
	r.Register("PutDashboard", handleCWQueryPutDashboard)
	r.Register("GetDashboard", handleCWQueryGetDashboard)
	r.Register("ListDashboards", handleCWQueryListDashboards)
	r.Register("DeleteDashboards", handleCWQueryDeleteDashboards)
}

func handleCWQueryPutDashboard(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DashboardName")
	if name == "" {
		cwQueryError(w, "MissingParameter", "The parameter DashboardName is required.")
		return
	}
	cwPutDashboard(name, r.FormValue("DashboardBody"))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<PutDashboardResponse %s><PutDashboardResult><DashboardValidationMessages/></PutDashboardResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></PutDashboardResponse>`,
		cwQueryXmlns, generateUUID())
}

func handleCWQueryGetDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := cwDashboards.Get(r.FormValue("DashboardName"))
	if !ok {
		cwQueryError(w, "ResourceNotFound", "Dashboard does not exist")
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetDashboardResponse %s><GetDashboardResult><DashboardName>%s</DashboardName><DashboardArn>%s</DashboardArn><DashboardBody>%s</DashboardBody></GetDashboardResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetDashboardResponse>`,
		cwQueryXmlns, xmlEscape(d.Name), xmlEscape(cwDashboardArn(d.Name)), xmlEscape(d.Body), generateUUID())
}

func handleCWQueryListDashboards(w http.ResponseWriter, r *http.Request) {
	var members strings.Builder
	for _, e := range cwListDashboardEntries(r.FormValue("DashboardNamePrefix")) {
		members.WriteString("<member>")
		fmt.Fprintf(&members, "<DashboardName>%s</DashboardName><DashboardArn>%s</DashboardArn><Size>%v</Size>",
			xmlEscape(fmt.Sprint(e["DashboardName"])), xmlEscape(fmt.Sprint(e["DashboardArn"])), e["Size"])
		members.WriteString("</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListDashboardsResponse %s><ListDashboardsResult><DashboardEntries>%s</DashboardEntries></ListDashboardsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListDashboardsResponse>`,
		cwQueryXmlns, members.String(), generateUUID())
}

func handleCWQueryDeleteDashboards(w http.ResponseWriter, r *http.Request) {
	names := cwQueryStringList(r, "DashboardNames")
	for _, n := range names {
		if _, ok := cwDashboards.Get(n); !ok {
			cwQueryError(w, "ResourceNotFound", fmt.Sprintf("Dashboard %s does not exist", n))
			return
		}
	}
	for _, n := range names {
		cwDashboards.Delete(n)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteDashboardsResponse %s><DeleteDashboardsResult/><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></DeleteDashboardsResponse>`,
		cwQueryXmlns, generateUUID())
}
