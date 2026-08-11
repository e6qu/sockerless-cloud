package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon EC2 Reachability Analyzer (Network Insights), Network Access Analyzer
// (Network Insights access scopes), Route Server, and Local Gateway Routes
// families — faithful ec2Query (XML) CRUD.
//
// Resources modeled, each persisted in its own sim.Store:
//   - Network Insights paths (nip-…): a source→destination probe definition.
//   - Network Insights analyses (nia-…): an analysis run of a path. The
//     simulator settles it to "succeeded" synchronously with a real-shaped
//     result (NetworkPathFound=true, empty findings/explanations). No
//     fabricated hop topology beyond the source/destination is invented.
//   - Network Insights access scopes (nis-…) + their content (match/exclude
//     paths) and analyses (nisa-…): Network Access Analyzer.
//   - Route servers (rs-…): associate to a VPC, with endpoints (rse-…),
//     peers (rsp-…), VPC associations, and route-table propagation state.
//   - Local gateway routes on an existing local-gateway-route-table.
//
// XML element names match the smithy.api#xmlName traits in
// specs/cloud-api/aws/ec2.smithy.json.gz exactly (the spec-shape validator
// rejects any unknown or mis-cased field).

type EC2NetworkInsightsPath struct {
	NetworkInsightsPathId  string
	NetworkInsightsPathArn string
	CreatedDate            string
	Source                 string
	Destination            string
	SourceArn              string
	DestinationArn         string
	SourceIp               string
	DestinationIp          string
	Protocol               string
	DestinationPort        int32
	HasDestinationPort     bool
	Tags                   []EC2Tag
}

type EC2NetworkInsightsAnalysis struct {
	NetworkInsightsAnalysisId  string
	NetworkInsightsAnalysisArn string
	NetworkInsightsPathId      string
	StartDate                  string
	Status                     string
	NetworkPathFound           bool
	Tags                       []EC2Tag
}

type EC2NetworkInsightsAccessScope struct {
	NetworkInsightsAccessScopeId  string
	NetworkInsightsAccessScopeArn string
	CreatedDate                   string
	UpdatedDate                   string
	Tags                          []EC2Tag
}

type EC2NetworkInsightsAccessScopeAnalysis struct {
	NetworkInsightsAccessScopeAnalysisId  string
	NetworkInsightsAccessScopeAnalysisArn string
	NetworkInsightsAccessScopeId          string
	Status                                string
	StartDate                             string
	EndDate                               string
	FindingsFound                         string
	AnalyzedEniCount                      int32
	Tags                                  []EC2Tag
}

type EC2RouteServer struct {
	RouteServerId           string
	AmazonSideAsn           int64
	State                   string
	PersistRoutesState      string
	PersistRoutesDuration   int64
	HasPersistRoutesDur     bool
	SnsNotificationsEnabled bool
	SnsTopicArn             string
	// VpcId is the VPC this route server is associated with (AssociateRouteServer).
	VpcId string
	Tags  []EC2Tag
}

type EC2RouteServerEndpoint struct {
	RouteServerId         string
	RouteServerEndpointId string
	VpcId                 string
	SubnetId              string
	EniId                 string
	EniAddress            string
	State                 string
	Tags                  []EC2Tag
}

type EC2RouteServerPeer struct {
	RouteServerPeerId     string
	RouteServerEndpointId string
	RouteServerId         string
	VpcId                 string
	SubnetId              string
	State                 string
	EndpointEniId         string
	EndpointEniAddress    string
	PeerAddress           string
	BgpPeerAsn            int64
	BgpPeerLiveness       string
	BgpStatus             string
	BfdStatus             string
	Tags                  []EC2Tag
}

// EC2RouteServerPropagation records that a route server propagates its routes
// into a given route table (EnableRouteServerPropagation).
type EC2RouteServerPropagation struct {
	RouteServerId string
	RouteTableId  string
	State         string
}

type EC2LocalGatewayRoute struct {
	DestinationCidrBlock                string
	DestinationPrefixListId             string
	LocalGatewayVirtualInterfaceGroupId string
	NetworkInterfaceId                  string
	Type                                string
	State                               string
	LocalGatewayRouteTableId            string
	LocalGatewayRouteTableArn           string
	OwnerId                             string
}

var (
	ec2NetworkInsightsPaths         sim.Store[EC2NetworkInsightsPath]
	ec2NetworkInsightsAnalyses      sim.Store[EC2NetworkInsightsAnalysis]
	ec2NetworkInsightsAccessScopes  sim.Store[EC2NetworkInsightsAccessScope]
	ec2NetworkInsightsScopeAnalyses sim.Store[EC2NetworkInsightsAccessScopeAnalysis]
	ec2RouteServers                 sim.Store[EC2RouteServer]
	ec2RouteServerEndpoints         sim.Store[EC2RouteServerEndpoint]
	ec2RouteServerPeers             sim.Store[EC2RouteServerPeer]
	ec2RouteServerPropagations      sim.Store[EC2RouteServerPropagation]
	ec2LocalGatewayRoutes           sim.Store[EC2LocalGatewayRoute]
)

func registerEC2NetworkInsights(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2NetworkInsightsPaths = sim.MakeStore[EC2NetworkInsightsPath](srv.DB(), "ec2_network_insights_paths")
	ec2NetworkInsightsAnalyses = sim.MakeStore[EC2NetworkInsightsAnalysis](srv.DB(), "ec2_network_insights_analyses")
	ec2NetworkInsightsAccessScopes = sim.MakeStore[EC2NetworkInsightsAccessScope](srv.DB(), "ec2_network_insights_access_scopes")
	ec2NetworkInsightsScopeAnalyses = sim.MakeStore[EC2NetworkInsightsAccessScopeAnalysis](srv.DB(), "ec2_network_insights_scope_analyses")
	ec2RouteServers = sim.MakeStore[EC2RouteServer](srv.DB(), "ec2_route_servers")
	ec2RouteServerEndpoints = sim.MakeStore[EC2RouteServerEndpoint](srv.DB(), "ec2_route_server_endpoints")
	ec2RouteServerPeers = sim.MakeStore[EC2RouteServerPeer](srv.DB(), "ec2_route_server_peers")
	ec2RouteServerPropagations = sim.MakeStore[EC2RouteServerPropagation](srv.DB(), "ec2_route_server_propagations")
	ec2LocalGatewayRoutes = sim.MakeStore[EC2LocalGatewayRoute](srv.DB(), "ec2_local_gateway_routes")

	// Network Insights paths (Reachability Analyzer).
	r.Register("CreateNetworkInsightsPath", handleCreateNetworkInsightsPath)
	r.Register("DescribeNetworkInsightsPaths", handleDescribeNetworkInsightsPaths)
	r.Register("DeleteNetworkInsightsPath", handleDeleteNetworkInsightsPath)

	// Network Insights analyses.
	r.Register("StartNetworkInsightsAnalysis", handleStartNetworkInsightsAnalysis)
	r.Register("DescribeNetworkInsightsAnalyses", handleDescribeNetworkInsightsAnalyses)
	r.Register("DeleteNetworkInsightsAnalysis", handleDeleteNetworkInsightsAnalysis)

	// Network Insights access scopes (Network Access Analyzer).
	r.Register("CreateNetworkInsightsAccessScope", handleCreateNetworkInsightsAccessScope)
	r.Register("DescribeNetworkInsightsAccessScopes", handleDescribeNetworkInsightsAccessScopes)
	r.Register("DeleteNetworkInsightsAccessScope", handleDeleteNetworkInsightsAccessScope)
	r.Register("GetNetworkInsightsAccessScopeContent", handleGetNetworkInsightsAccessScopeContent)
	r.Register("StartNetworkInsightsAccessScopeAnalysis", handleStartNetworkInsightsAccessScopeAnalysis)
	r.Register("DescribeNetworkInsightsAccessScopeAnalyses", handleDescribeNetworkInsightsAccessScopeAnalyses)
	r.Register("DeleteNetworkInsightsAccessScopeAnalysis", handleDeleteNetworkInsightsAccessScopeAnalysis)
	r.Register("GetNetworkInsightsAccessScopeAnalysisFindings", handleGetNetworkInsightsAccessScopeAnalysisFindings)

	// Route servers.
	r.Register("CreateRouteServer", handleCreateRouteServer)
	r.Register("DescribeRouteServers", handleDescribeRouteServers)
	r.Register("ModifyRouteServer", handleModifyRouteServer)
	r.Register("DeleteRouteServer", handleDeleteRouteServer)
	r.Register("AssociateRouteServer", handleAssociateRouteServer)
	r.Register("DisassociateRouteServer", handleDisassociateRouteServer)
	r.Register("GetRouteServerAssociations", handleGetRouteServerAssociations)
	r.Register("CreateRouteServerEndpoint", handleCreateRouteServerEndpoint)
	r.Register("DescribeRouteServerEndpoints", handleDescribeRouteServerEndpoints)
	r.Register("DeleteRouteServerEndpoint", handleDeleteRouteServerEndpoint)
	r.Register("CreateRouteServerPeer", handleCreateRouteServerPeer)
	r.Register("DescribeRouteServerPeers", handleDescribeRouteServerPeers)
	r.Register("DeleteRouteServerPeer", handleDeleteRouteServerPeer)
	r.Register("EnableRouteServerPropagation", handleEnableRouteServerPropagation)
	r.Register("DisableRouteServerPropagation", handleDisableRouteServerPropagation)
	r.Register("GetRouteServerPropagations", handleGetRouteServerPropagations)
	r.Register("GetRouteServerRoutingDatabase", handleGetRouteServerRoutingDatabase)

	// Local gateway routes. The list operation in the real EC2 API is
	// SearchLocalGatewayRoutes (there is no DescribeLocalGatewayRoutes).
	r.Register("CreateLocalGatewayRoute", handleCreateLocalGatewayRoute)
	r.Register("SearchLocalGatewayRoutes", handleSearchLocalGatewayRoutes)
	r.Register("ModifyLocalGatewayRoute", handleModifyLocalGatewayRoute)
	r.Register("DeleteLocalGatewayRoute", handleDeleteLocalGatewayRoute)
}

// ---- ARN helpers ----

func ec2ResourceArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:%s/%s", awsRegion(), ec2Owner(), resourceType, id)
}

func ec2Response(w http.ResponseWriter, action, body string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, "<%sResponse %s><requestId>%s</requestId>%s</%sResponse>",
		action, ec2Xmlns(), generateUUID(), body, action)
}

// ============================================================================
// Network Insights paths
// ============================================================================

func handleCreateNetworkInsightsPath(w http.ResponseWriter, r *http.Request) {
	id := ec2ID("nip")
	path := EC2NetworkInsightsPath{
		NetworkInsightsPathId:  id,
		NetworkInsightsPathArn: ec2ResourceArn("network-insights-path", id),
		CreatedDate:            ec2NowRFC3339Milli(),
		Source:                 r.FormValue("Source"),
		Destination:            r.FormValue("Destination"),
		SourceIp:               r.FormValue("SourceIp"),
		DestinationIp:          r.FormValue("DestinationIp"),
		Protocol:               r.FormValue("Protocol"),
		Tags:                   parseTags(r),
	}
	// A Source/Destination that is itself an ARN is echoed in the *Arn fields.
	if strings.HasPrefix(path.Source, "arn:") {
		path.SourceArn = path.Source
	}
	if strings.HasPrefix(path.Destination, "arn:") {
		path.DestinationArn = path.Destination
	}
	if v := r.FormValue("DestinationPort"); v != "" {
		if p, err := strconv.ParseInt(v, 10, 32); err == nil {
			path.DestinationPort = int32(p)
			path.HasDestinationPort = true
		}
	}
	ec2NetworkInsightsPaths.Put(id, path)
	ec2Response(w, "CreateNetworkInsightsPath",
		"<networkInsightsPath>"+nipBodyXML(path)+"</networkInsightsPath>")
}

func handleDescribeNetworkInsightsPaths(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkInsightsPathIds")
	var b strings.Builder
	b.WriteString("<networkInsightsPathSet>")
	for _, p := range ec2NetworkInsightsPaths.List() {
		if len(ids) > 0 && !ec2StrInValues(p.NetworkInsightsPathId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", nipBodyXML(p))
	}
	b.WriteString("</networkInsightsPathSet>")
	ec2Response(w, "DescribeNetworkInsightsPaths", b.String())
}

func handleDeleteNetworkInsightsPath(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInsightsPathId")
	if _, ok := ec2NetworkInsightsPaths.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsPathId.NotFound", "network insights path not found: "+id, http.StatusBadRequest)
		return
	}
	ec2NetworkInsightsPaths.Delete(id)
	ec2Response(w, "DeleteNetworkInsightsPath",
		fmt.Sprintf("<networkInsightsPathId>%s</networkInsightsPathId>", id))
}

func nipBodyXML(p EC2NetworkInsightsPath) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<networkInsightsPathId>%s</networkInsightsPathId>", p.NetworkInsightsPathId)
	fmt.Fprintf(&b, "<networkInsightsPathArn>%s</networkInsightsPathArn>", p.NetworkInsightsPathArn)
	fmt.Fprintf(&b, "<createdDate>%s</createdDate>", p.CreatedDate)
	if p.Source != "" {
		fmt.Fprintf(&b, "<source>%s</source>", xmlEscape(p.Source))
	}
	if p.Destination != "" {
		fmt.Fprintf(&b, "<destination>%s</destination>", xmlEscape(p.Destination))
	}
	if p.SourceArn != "" {
		fmt.Fprintf(&b, "<sourceArn>%s</sourceArn>", xmlEscape(p.SourceArn))
	}
	if p.DestinationArn != "" {
		fmt.Fprintf(&b, "<destinationArn>%s</destinationArn>", xmlEscape(p.DestinationArn))
	}
	if p.SourceIp != "" {
		fmt.Fprintf(&b, "<sourceIp>%s</sourceIp>", p.SourceIp)
	}
	if p.DestinationIp != "" {
		fmt.Fprintf(&b, "<destinationIp>%s</destinationIp>", p.DestinationIp)
	}
	if p.Protocol != "" {
		fmt.Fprintf(&b, "<protocol>%s</protocol>", p.Protocol)
	}
	if p.HasDestinationPort {
		fmt.Fprintf(&b, "<destinationPort>%d</destinationPort>", p.DestinationPort)
	}
	b.WriteString(nipTagSetXML(p.Tags))
	return b.String()
}

// nipTagSetXML emits the <tagSet> element for these families (same shape as
// the core writeTagSetXML).
func nipTagSetXML(tags []EC2Tag) string {
	return writeTagSetXML(tags)
}

// ============================================================================
// Network Insights analyses
// ============================================================================

func handleStartNetworkInsightsAnalysis(w http.ResponseWriter, r *http.Request) {
	pathID := r.FormValue("NetworkInsightsPathId")
	if _, ok := ec2NetworkInsightsPaths.Get(pathID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsPathId.NotFound", "network insights path not found: "+pathID, http.StatusBadRequest)
		return
	}
	id := ec2ID("nia")
	a := EC2NetworkInsightsAnalysis{
		NetworkInsightsAnalysisId:  id,
		NetworkInsightsAnalysisArn: ec2ResourceArn("network-insights-analysis", id),
		NetworkInsightsPathId:      pathID,
		StartDate:                  ec2NowRFC3339Milli(),
		// The analysis settles synchronously. We report a reachable path with
		// no findings rather than fabricate a hop-by-hop topology.
		Status:           "succeeded",
		NetworkPathFound: true,
		Tags:             parseTags(r),
	}
	ec2NetworkInsightsAnalyses.Put(id, a)
	ec2Response(w, "StartNetworkInsightsAnalysis",
		"<networkInsightsAnalysis>"+niaBodyXML(a)+"</networkInsightsAnalysis>")
}

func handleDescribeNetworkInsightsAnalyses(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkInsightsAnalysisIds")
	pathID := r.FormValue("NetworkInsightsPathId")
	var b strings.Builder
	b.WriteString("<networkInsightsAnalysisSet>")
	for _, a := range ec2NetworkInsightsAnalyses.List() {
		if len(ids) > 0 && !ec2StrInValues(a.NetworkInsightsAnalysisId, ids) {
			continue
		}
		if pathID != "" && a.NetworkInsightsPathId != pathID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", niaBodyXML(a))
	}
	b.WriteString("</networkInsightsAnalysisSet>")
	ec2Response(w, "DescribeNetworkInsightsAnalyses", b.String())
}

func handleDeleteNetworkInsightsAnalysis(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInsightsAnalysisId")
	if _, ok := ec2NetworkInsightsAnalyses.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsAnalysisId.NotFound", "network insights analysis not found: "+id, http.StatusBadRequest)
		return
	}
	ec2NetworkInsightsAnalyses.Delete(id)
	ec2Response(w, "DeleteNetworkInsightsAnalysis",
		fmt.Sprintf("<networkInsightsAnalysisId>%s</networkInsightsAnalysisId>", id))
}

func niaBodyXML(a EC2NetworkInsightsAnalysis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<networkInsightsAnalysisId>%s</networkInsightsAnalysisId>", a.NetworkInsightsAnalysisId)
	fmt.Fprintf(&b, "<networkInsightsAnalysisArn>%s</networkInsightsAnalysisArn>", a.NetworkInsightsAnalysisArn)
	fmt.Fprintf(&b, "<networkInsightsPathId>%s</networkInsightsPathId>", a.NetworkInsightsPathId)
	fmt.Fprintf(&b, "<startDate>%s</startDate>", a.StartDate)
	fmt.Fprintf(&b, "<status>%s</status>", a.Status)
	fmt.Fprintf(&b, "<networkPathFound>%t</networkPathFound>", a.NetworkPathFound)
	b.WriteString(nipTagSetXML(a.Tags))
	return b.String()
}

// ============================================================================
// Network Insights access scopes (Network Access Analyzer)
// ============================================================================

func handleCreateNetworkInsightsAccessScope(w http.ResponseWriter, r *http.Request) {
	id := ec2ID("nis")
	now := ec2NowRFC3339Milli()
	s := EC2NetworkInsightsAccessScope{
		NetworkInsightsAccessScopeId:  id,
		NetworkInsightsAccessScopeArn: ec2ResourceArn("network-insights-access-scope", id),
		CreatedDate:                   now,
		UpdatedDate:                   now,
		Tags:                          parseTags(r),
	}
	ec2NetworkInsightsAccessScopes.Put(id, s)
	// Create returns both the scope and its content (the match/exclude paths
	// echoed from the request).
	body := "<networkInsightsAccessScope>" + nisBodyXML(s) + "</networkInsightsAccessScope>" +
		"<networkInsightsAccessScopeContent>" + nisContentBodyXML(id) + "</networkInsightsAccessScopeContent>"
	ec2Response(w, "CreateNetworkInsightsAccessScope", body)
}

func handleDescribeNetworkInsightsAccessScopes(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkInsightsAccessScopeIds")
	var b strings.Builder
	b.WriteString("<networkInsightsAccessScopeSet>")
	for _, s := range ec2NetworkInsightsAccessScopes.List() {
		if len(ids) > 0 && !ec2StrInValues(s.NetworkInsightsAccessScopeId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", nisBodyXML(s))
	}
	b.WriteString("</networkInsightsAccessScopeSet>")
	ec2Response(w, "DescribeNetworkInsightsAccessScopes", b.String())
}

func handleDeleteNetworkInsightsAccessScope(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInsightsAccessScopeId")
	if _, ok := ec2NetworkInsightsAccessScopes.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsAccessScopeId.NotFound", "network insights access scope not found: "+id, http.StatusBadRequest)
		return
	}
	ec2NetworkInsightsAccessScopes.Delete(id)
	ec2Response(w, "DeleteNetworkInsightsAccessScope",
		fmt.Sprintf("<networkInsightsAccessScopeId>%s</networkInsightsAccessScopeId>", id))
}

func handleGetNetworkInsightsAccessScopeContent(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInsightsAccessScopeId")
	if _, ok := ec2NetworkInsightsAccessScopes.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsAccessScopeId.NotFound", "network insights access scope not found: "+id, http.StatusBadRequest)
		return
	}
	ec2Response(w, "GetNetworkInsightsAccessScopeContent",
		"<networkInsightsAccessScopeContent>"+nisContentBodyXML(id)+"</networkInsightsAccessScopeContent>")
}

func nisBodyXML(s EC2NetworkInsightsAccessScope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<networkInsightsAccessScopeId>%s</networkInsightsAccessScopeId>", s.NetworkInsightsAccessScopeId)
	fmt.Fprintf(&b, "<networkInsightsAccessScopeArn>%s</networkInsightsAccessScopeArn>", s.NetworkInsightsAccessScopeArn)
	fmt.Fprintf(&b, "<createdDate>%s</createdDate>", s.CreatedDate)
	fmt.Fprintf(&b, "<updatedDate>%s</updatedDate>", s.UpdatedDate)
	b.WriteString(nipTagSetXML(s.Tags))
	return b.String()
}

func nisContentBodyXML(scopeID string) string {
	// MatchPaths/ExcludePaths are optional; the scope content always carries
	// the scope id it belongs to.
	return fmt.Sprintf("<networkInsightsAccessScopeId>%s</networkInsightsAccessScopeId>", scopeID)
}

// ============================================================================
// Network Insights access scope analyses
// ============================================================================

func handleStartNetworkInsightsAccessScopeAnalysis(w http.ResponseWriter, r *http.Request) {
	scopeID := r.FormValue("NetworkInsightsAccessScopeId")
	if _, ok := ec2NetworkInsightsAccessScopes.Get(scopeID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsAccessScopeId.NotFound", "network insights access scope not found: "+scopeID, http.StatusBadRequest)
		return
	}
	id := ec2ID("nisa")
	now := ec2NowRFC3339Milli()
	a := EC2NetworkInsightsAccessScopeAnalysis{
		NetworkInsightsAccessScopeAnalysisId:  id,
		NetworkInsightsAccessScopeAnalysisArn: ec2ResourceArn("network-insights-access-scope-analysis", id),
		NetworkInsightsAccessScopeId:          scopeID,
		Status:                                "succeeded",
		StartDate:                             now,
		EndDate:                               now,
		FindingsFound:                         "false",
		AnalyzedEniCount:                      0,
		Tags:                                  parseTags(r),
	}
	ec2NetworkInsightsScopeAnalyses.Put(id, a)
	ec2Response(w, "StartNetworkInsightsAccessScopeAnalysis",
		"<networkInsightsAccessScopeAnalysis>"+nisaBodyXML(a)+"</networkInsightsAccessScopeAnalysis>")
}

func handleDescribeNetworkInsightsAccessScopeAnalyses(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkInsightsAccessScopeAnalysisIds")
	scopeID := r.FormValue("NetworkInsightsAccessScopeId")
	var b strings.Builder
	b.WriteString("<networkInsightsAccessScopeAnalysisSet>")
	for _, a := range ec2NetworkInsightsScopeAnalyses.List() {
		if len(ids) > 0 && !ec2StrInValues(a.NetworkInsightsAccessScopeAnalysisId, ids) {
			continue
		}
		if scopeID != "" && a.NetworkInsightsAccessScopeId != scopeID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", nisaBodyXML(a))
	}
	b.WriteString("</networkInsightsAccessScopeAnalysisSet>")
	ec2Response(w, "DescribeNetworkInsightsAccessScopeAnalyses", b.String())
}

func handleDeleteNetworkInsightsAccessScopeAnalysis(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInsightsAccessScopeAnalysisId")
	if _, ok := ec2NetworkInsightsScopeAnalyses.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsAccessScopeAnalysisId.NotFound", "network insights access scope analysis not found: "+id, http.StatusBadRequest)
		return
	}
	ec2NetworkInsightsScopeAnalyses.Delete(id)
	ec2Response(w, "DeleteNetworkInsightsAccessScopeAnalysis",
		fmt.Sprintf("<networkInsightsAccessScopeAnalysisId>%s</networkInsightsAccessScopeAnalysisId>", id))
}

func handleGetNetworkInsightsAccessScopeAnalysisFindings(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInsightsAccessScopeAnalysisId")
	a, ok := ec2NetworkInsightsScopeAnalyses.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidNetworkInsightsAccessScopeAnalysisId.NotFound", "network insights access scope analysis not found: "+id, http.StatusBadRequest)
		return
	}
	// No findings are fabricated; the analysis reported FindingsFound=false.
	var b strings.Builder
	fmt.Fprintf(&b, "<networkInsightsAccessScopeAnalysisId>%s</networkInsightsAccessScopeAnalysisId>", a.NetworkInsightsAccessScopeAnalysisId)
	fmt.Fprintf(&b, "<analysisStatus>%s</analysisStatus>", a.Status)
	b.WriteString("<analysisFindingSet/>")
	ec2Response(w, "GetNetworkInsightsAccessScopeAnalysisFindings", b.String())
}

func nisaBodyXML(a EC2NetworkInsightsAccessScopeAnalysis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<networkInsightsAccessScopeAnalysisId>%s</networkInsightsAccessScopeAnalysisId>", a.NetworkInsightsAccessScopeAnalysisId)
	fmt.Fprintf(&b, "<networkInsightsAccessScopeAnalysisArn>%s</networkInsightsAccessScopeAnalysisArn>", a.NetworkInsightsAccessScopeAnalysisArn)
	fmt.Fprintf(&b, "<networkInsightsAccessScopeId>%s</networkInsightsAccessScopeId>", a.NetworkInsightsAccessScopeId)
	fmt.Fprintf(&b, "<status>%s</status>", a.Status)
	fmt.Fprintf(&b, "<startDate>%s</startDate>", a.StartDate)
	fmt.Fprintf(&b, "<endDate>%s</endDate>", a.EndDate)
	fmt.Fprintf(&b, "<findingsFound>%s</findingsFound>", a.FindingsFound)
	fmt.Fprintf(&b, "<analyzedEniCount>%d</analyzedEniCount>", a.AnalyzedEniCount)
	b.WriteString(nipTagSetXML(a.Tags))
	return b.String()
}

// ============================================================================
// Route servers
// ============================================================================

func handleCreateRouteServer(w http.ResponseWriter, r *http.Request) {
	id := ec2ID("rs")
	rs := EC2RouteServer{
		RouteServerId:           id,
		State:                   "available",
		PersistRoutesState:      "disabled",
		SnsNotificationsEnabled: r.FormValue("SnsNotificationsEnabled") == "true",
		Tags:                    parseTags(r),
	}
	if v := r.FormValue("AmazonSideAsn"); v != "" {
		if asn, err := strconv.ParseInt(v, 10, 64); err == nil {
			rs.AmazonSideAsn = asn
		}
	}
	if r.FormValue("PersistRoutes") == "enable" {
		rs.PersistRoutesState = "enabled"
	}
	if v := r.FormValue("PersistRoutesDuration"); v != "" {
		if d, err := strconv.ParseInt(v, 10, 64); err == nil {
			rs.PersistRoutesDuration = d
			rs.HasPersistRoutesDur = true
		}
	}
	ec2RouteServers.Put(id, rs)
	ec2Response(w, "CreateRouteServer", "<routeServer>"+routeServerBodyXML(rs)+"</routeServer>")
}

func handleDescribeRouteServers(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "RouteServerIds")
	var b strings.Builder
	b.WriteString("<routeServerSet>")
	for _, rs := range ec2RouteServers.List() {
		if len(ids) > 0 && !ec2StrInValues(rs.RouteServerId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", routeServerBodyXML(rs))
	}
	b.WriteString("</routeServerSet>")
	ec2Response(w, "DescribeRouteServers", b.String())
}

func handleModifyRouteServer(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerId")
	rs, ok := ec2RouteServers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+id, http.StatusBadRequest)
		return
	}
	if v := r.FormValue("PersistRoutes"); v != "" {
		switch v {
		case "enable":
			rs.PersistRoutesState = "enabled"
		case "disable":
			rs.PersistRoutesState = "disabled"
		}
	}
	if v := r.FormValue("PersistRoutesDuration"); v != "" {
		if d, err := strconv.ParseInt(v, 10, 64); err == nil {
			rs.PersistRoutesDuration = d
			rs.HasPersistRoutesDur = true
		}
	}
	if v := r.FormValue("SnsNotificationsEnabled"); v != "" {
		rs.SnsNotificationsEnabled = v == "true"
	}
	ec2RouteServers.Put(id, rs)
	ec2Response(w, "ModifyRouteServer", "<routeServer>"+routeServerBodyXML(rs)+"</routeServer>")
}

func handleDeleteRouteServer(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerId")
	rs, ok := ec2RouteServers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+id, http.StatusBadRequest)
		return
	}
	rs.State = "deleted"
	ec2RouteServers.Delete(id)
	ec2Response(w, "DeleteRouteServer", "<routeServer>"+routeServerBodyXML(rs)+"</routeServer>")
}

func handleAssociateRouteServer(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerId")
	vpcID := r.FormValue("VpcId")
	rs, ok := ec2RouteServers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+id, http.StatusBadRequest)
		return
	}
	rs.VpcId = vpcID
	ec2RouteServers.Put(id, rs)
	body := fmt.Sprintf("<routeServerAssociation><routeServerId>%s</routeServerId><vpcId>%s</vpcId><state>associated</state></routeServerAssociation>", id, vpcID)
	ec2Response(w, "AssociateRouteServer", body)
}

func handleDisassociateRouteServer(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerId")
	vpcID := r.FormValue("VpcId")
	rs, ok := ec2RouteServers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+id, http.StatusBadRequest)
		return
	}
	if vpcID == "" {
		vpcID = rs.VpcId
	}
	rs.VpcId = ""
	ec2RouteServers.Put(id, rs)
	body := fmt.Sprintf("<routeServerAssociation><routeServerId>%s</routeServerId><vpcId>%s</vpcId><state>disassociated</state></routeServerAssociation>", id, vpcID)
	ec2Response(w, "DisassociateRouteServer", body)
}

func handleGetRouteServerAssociations(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerId")
	rs, ok := ec2RouteServers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+id, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<routeServerAssociationSet>")
	if rs.VpcId != "" {
		fmt.Fprintf(&b, "<item><routeServerId>%s</routeServerId><vpcId>%s</vpcId><state>associated</state></item>", id, rs.VpcId)
	}
	b.WriteString("</routeServerAssociationSet>")
	ec2Response(w, "GetRouteServerAssociations", b.String())
}

func routeServerBodyXML(rs EC2RouteServer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<routeServerId>%s</routeServerId>", rs.RouteServerId)
	fmt.Fprintf(&b, "<amazonSideAsn>%d</amazonSideAsn>", rs.AmazonSideAsn)
	fmt.Fprintf(&b, "<state>%s</state>", rs.State)
	if rs.PersistRoutesState != "" {
		fmt.Fprintf(&b, "<persistRoutesState>%s</persistRoutesState>", rs.PersistRoutesState)
	}
	if rs.HasPersistRoutesDur {
		fmt.Fprintf(&b, "<persistRoutesDuration>%d</persistRoutesDuration>", rs.PersistRoutesDuration)
	}
	fmt.Fprintf(&b, "<snsNotificationsEnabled>%t</snsNotificationsEnabled>", rs.SnsNotificationsEnabled)
	if rs.SnsTopicArn != "" {
		fmt.Fprintf(&b, "<snsTopicArn>%s</snsTopicArn>", xmlEscape(rs.SnsTopicArn))
	}
	b.WriteString(nipTagSetXML(rs.Tags))
	return b.String()
}

// ---- Route server endpoints ----

func handleCreateRouteServerEndpoint(w http.ResponseWriter, r *http.Request) {
	rsID := r.FormValue("RouteServerId")
	rs, ok := ec2RouteServers.Get(rsID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+rsID, http.StatusBadRequest)
		return
	}
	subnetID := r.FormValue("SubnetId")
	id := ec2ID("rse")
	ep := EC2RouteServerEndpoint{
		RouteServerId:         rsID,
		RouteServerEndpointId: id,
		VpcId:                 rs.VpcId,
		SubnetId:              subnetID,
		EniId:                 ec2ID("eni"),
		EniAddress:            "10.0.0.10",
		State:                 "available",
		Tags:                  parseTags(r),
	}
	if subnet, ok := ec2Subnets.Get(subnetID); ok && subnet.VpcId != "" {
		ep.VpcId = subnet.VpcId
	}
	ec2RouteServerEndpoints.Put(id, ep)
	ec2Response(w, "CreateRouteServerEndpoint", "<routeServerEndpoint>"+routeServerEndpointBodyXML(ep)+"</routeServerEndpoint>")
}

func handleDescribeRouteServerEndpoints(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "RouteServerEndpointIds")
	var b strings.Builder
	b.WriteString("<routeServerEndpointSet>")
	for _, ep := range ec2RouteServerEndpoints.List() {
		if len(ids) > 0 && !ec2StrInValues(ep.RouteServerEndpointId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", routeServerEndpointBodyXML(ep))
	}
	b.WriteString("</routeServerEndpointSet>")
	ec2Response(w, "DescribeRouteServerEndpoints", b.String())
}

func handleDeleteRouteServerEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerEndpointId")
	ep, ok := ec2RouteServerEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerEndpointId.NotFound", "route server endpoint not found: "+id, http.StatusBadRequest)
		return
	}
	ep.State = "deleted"
	ec2RouteServerEndpoints.Delete(id)
	ec2Response(w, "DeleteRouteServerEndpoint", "<routeServerEndpoint>"+routeServerEndpointBodyXML(ep)+"</routeServerEndpoint>")
}

func routeServerEndpointBodyXML(ep EC2RouteServerEndpoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<routeServerId>%s</routeServerId>", ep.RouteServerId)
	fmt.Fprintf(&b, "<routeServerEndpointId>%s</routeServerEndpointId>", ep.RouteServerEndpointId)
	if ep.VpcId != "" {
		fmt.Fprintf(&b, "<vpcId>%s</vpcId>", ep.VpcId)
	}
	if ep.SubnetId != "" {
		fmt.Fprintf(&b, "<subnetId>%s</subnetId>", ep.SubnetId)
	}
	if ep.EniId != "" {
		fmt.Fprintf(&b, "<eniId>%s</eniId>", ep.EniId)
	}
	if ep.EniAddress != "" {
		fmt.Fprintf(&b, "<eniAddress>%s</eniAddress>", ep.EniAddress)
	}
	fmt.Fprintf(&b, "<state>%s</state>", ep.State)
	b.WriteString(nipTagSetXML(ep.Tags))
	return b.String()
}

// ---- Route server peers ----

func handleCreateRouteServerPeer(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("RouteServerEndpointId")
	ep, ok := ec2RouteServerEndpoints.Get(epID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerEndpointId.NotFound", "route server endpoint not found: "+epID, http.StatusBadRequest)
		return
	}
	id := ec2ID("rsp")
	peer := EC2RouteServerPeer{
		RouteServerPeerId:     id,
		RouteServerEndpointId: epID,
		RouteServerId:         ep.RouteServerId,
		VpcId:                 ep.VpcId,
		SubnetId:              ep.SubnetId,
		State:                 "available",
		EndpointEniId:         ep.EniId,
		EndpointEniAddress:    ep.EniAddress,
		PeerAddress:           r.FormValue("PeerAddress"),
		BgpStatus:             "up",
		BfdStatus:             "up",
		Tags:                  parseTags(r),
	}
	if v := r.FormValue("BgpOptions.PeerAsn"); v != "" {
		if asn, err := strconv.ParseInt(v, 10, 64); err == nil {
			peer.BgpPeerAsn = asn
		}
	}
	peer.BgpPeerLiveness = r.FormValue("BgpOptions.PeerLivenessDetection")
	ec2RouteServerPeers.Put(id, peer)
	ec2Response(w, "CreateRouteServerPeer", "<routeServerPeer>"+routeServerPeerBodyXML(peer)+"</routeServerPeer>")
}

func handleDescribeRouteServerPeers(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "RouteServerPeerIds")
	var b strings.Builder
	b.WriteString("<routeServerPeerSet>")
	for _, p := range ec2RouteServerPeers.List() {
		if len(ids) > 0 && !ec2StrInValues(p.RouteServerPeerId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", routeServerPeerBodyXML(p))
	}
	b.WriteString("</routeServerPeerSet>")
	ec2Response(w, "DescribeRouteServerPeers", b.String())
}

func handleDeleteRouteServerPeer(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteServerPeerId")
	p, ok := ec2RouteServerPeers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerPeerId.NotFound", "route server peer not found: "+id, http.StatusBadRequest)
		return
	}
	p.State = "deleted"
	ec2RouteServerPeers.Delete(id)
	ec2Response(w, "DeleteRouteServerPeer", "<routeServerPeer>"+routeServerPeerBodyXML(p)+"</routeServerPeer>")
}

func routeServerPeerBodyXML(p EC2RouteServerPeer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<routeServerPeerId>%s</routeServerPeerId>", p.RouteServerPeerId)
	fmt.Fprintf(&b, "<routeServerEndpointId>%s</routeServerEndpointId>", p.RouteServerEndpointId)
	fmt.Fprintf(&b, "<routeServerId>%s</routeServerId>", p.RouteServerId)
	if p.VpcId != "" {
		fmt.Fprintf(&b, "<vpcId>%s</vpcId>", p.VpcId)
	}
	if p.SubnetId != "" {
		fmt.Fprintf(&b, "<subnetId>%s</subnetId>", p.SubnetId)
	}
	fmt.Fprintf(&b, "<state>%s</state>", p.State)
	if p.EndpointEniId != "" {
		fmt.Fprintf(&b, "<endpointEniId>%s</endpointEniId>", p.EndpointEniId)
	}
	if p.EndpointEniAddress != "" {
		fmt.Fprintf(&b, "<endpointEniAddress>%s</endpointEniAddress>", p.EndpointEniAddress)
	}
	if p.PeerAddress != "" {
		fmt.Fprintf(&b, "<peerAddress>%s</peerAddress>", p.PeerAddress)
	}
	if p.BgpPeerAsn != 0 || p.BgpPeerLiveness != "" {
		b.WriteString("<bgpOptions>")
		if p.BgpPeerAsn != 0 {
			fmt.Fprintf(&b, "<peerAsn>%d</peerAsn>", p.BgpPeerAsn)
		}
		if p.BgpPeerLiveness != "" {
			fmt.Fprintf(&b, "<peerLivenessDetection>%s</peerLivenessDetection>", p.BgpPeerLiveness)
		}
		b.WriteString("</bgpOptions>")
	}
	if p.BgpStatus != "" {
		fmt.Fprintf(&b, "<bgpStatus><status>%s</status></bgpStatus>", p.BgpStatus)
	}
	if p.BfdStatus != "" {
		fmt.Fprintf(&b, "<bfdStatus><status>%s</status></bfdStatus>", p.BfdStatus)
	}
	b.WriteString(nipTagSetXML(p.Tags))
	return b.String()
}

// ---- Route server propagations ----

func handleEnableRouteServerPropagation(w http.ResponseWriter, r *http.Request) {
	rsID := r.FormValue("RouteServerId")
	rtID := r.FormValue("RouteTableId")
	if _, ok := ec2RouteServers.Get(rsID); !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+rsID, http.StatusBadRequest)
		return
	}
	prop := EC2RouteServerPropagation{RouteServerId: rsID, RouteTableId: rtID, State: "available"}
	ec2RouteServerPropagations.Put(rsID+"/"+rtID, prop)
	ec2Response(w, "EnableRouteServerPropagation", "<routeServerPropagation>"+routeServerPropagationBodyXML(prop)+"</routeServerPropagation>")
}

func handleDisableRouteServerPropagation(w http.ResponseWriter, r *http.Request) {
	rsID := r.FormValue("RouteServerId")
	rtID := r.FormValue("RouteTableId")
	key := rsID + "/" + rtID
	prop, ok := ec2RouteServerPropagations.Get(key)
	if !ok {
		prop = EC2RouteServerPropagation{RouteServerId: rsID, RouteTableId: rtID}
	}
	prop.State = "deleted"
	ec2RouteServerPropagations.Delete(key)
	ec2Response(w, "DisableRouteServerPropagation", "<routeServerPropagation>"+routeServerPropagationBodyXML(prop)+"</routeServerPropagation>")
}

func handleGetRouteServerPropagations(w http.ResponseWriter, r *http.Request) {
	rsID := r.FormValue("RouteServerId")
	if _, ok := ec2RouteServers.Get(rsID); !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+rsID, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<routeServerPropagationSet>")
	for _, p := range ec2RouteServerPropagations.List() {
		if p.RouteServerId != rsID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", routeServerPropagationBodyXML(p))
	}
	b.WriteString("</routeServerPropagationSet>")
	ec2Response(w, "GetRouteServerPropagations", b.String())
}

func handleGetRouteServerRoutingDatabase(w http.ResponseWriter, r *http.Request) {
	rsID := r.FormValue("RouteServerId")
	rs, ok := ec2RouteServers.Get(rsID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteServerId.NotFound", "route server not found: "+rsID, http.StatusBadRequest)
		return
	}
	// No routes are fabricated — the routing database is empty until real BGP
	// peers advertise prefixes.
	var b strings.Builder
	fmt.Fprintf(&b, "<areRoutesPersisted>%t</areRoutesPersisted>", rs.PersistRoutesState == "enabled")
	b.WriteString("<routeSet/>")
	ec2Response(w, "GetRouteServerRoutingDatabase", b.String())
}

func routeServerPropagationBodyXML(p EC2RouteServerPropagation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<routeServerId>%s</routeServerId>", p.RouteServerId)
	if p.RouteTableId != "" {
		fmt.Fprintf(&b, "<routeTableId>%s</routeTableId>", p.RouteTableId)
	}
	fmt.Fprintf(&b, "<state>%s</state>", p.State)
	return b.String()
}

// ============================================================================
// Local gateway routes
// ============================================================================

func lgwRouteKey(rtID, cidr, prefix string) string {
	if cidr != "" {
		return rtID + "/" + cidr
	}
	return rtID + "/" + prefix
}

func handleCreateLocalGatewayRoute(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("LocalGatewayRouteTableId")
	if rtID == "" {
		ec2ErrorXML(w, "MissingParameter", "LocalGatewayRouteTableId is required", http.StatusBadRequest)
		return
	}
	route := EC2LocalGatewayRoute{
		DestinationCidrBlock:                r.FormValue("DestinationCidrBlock"),
		DestinationPrefixListId:             r.FormValue("DestinationPrefixListId"),
		LocalGatewayVirtualInterfaceGroupId: r.FormValue("LocalGatewayVirtualInterfaceGroupId"),
		NetworkInterfaceId:                  r.FormValue("NetworkInterfaceId"),
		Type:                                "static",
		State:                               "active",
		LocalGatewayRouteTableId:            rtID,
		LocalGatewayRouteTableArn:           ec2ResourceArn("local-gateway-route-table", rtID),
		OwnerId:                             ec2Owner(),
	}
	ec2LocalGatewayRoutes.Put(lgwRouteKey(rtID, route.DestinationCidrBlock, route.DestinationPrefixListId), route)
	ec2Response(w, "CreateLocalGatewayRoute", "<route>"+localGatewayRouteBodyXML(route)+"</route>")
}

func handleSearchLocalGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("LocalGatewayRouteTableId")
	var b strings.Builder
	b.WriteString("<routeSet>")
	for _, route := range ec2LocalGatewayRoutes.List() {
		if rtID != "" && route.LocalGatewayRouteTableId != rtID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", localGatewayRouteBodyXML(route))
	}
	b.WriteString("</routeSet>")
	ec2Response(w, "SearchLocalGatewayRoutes", b.String())
}

func handleModifyLocalGatewayRoute(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("LocalGatewayRouteTableId")
	cidr := r.FormValue("DestinationCidrBlock")
	prefix := r.FormValue("DestinationPrefixListId")
	key := lgwRouteKey(rtID, cidr, prefix)
	route, ok := ec2LocalGatewayRoutes.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableID.NotFound", "local gateway route not found", http.StatusBadRequest)
		return
	}
	if v := r.FormValue("LocalGatewayVirtualInterfaceGroupId"); v != "" {
		route.LocalGatewayVirtualInterfaceGroupId = v
		route.NetworkInterfaceId = ""
	}
	if v := r.FormValue("NetworkInterfaceId"); v != "" {
		route.NetworkInterfaceId = v
		route.LocalGatewayVirtualInterfaceGroupId = ""
	}
	ec2LocalGatewayRoutes.Put(key, route)
	ec2Response(w, "ModifyLocalGatewayRoute", "<route>"+localGatewayRouteBodyXML(route)+"</route>")
}

func handleDeleteLocalGatewayRoute(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("LocalGatewayRouteTableId")
	cidr := r.FormValue("DestinationCidrBlock")
	prefix := r.FormValue("DestinationPrefixListId")
	key := lgwRouteKey(rtID, cidr, prefix)
	route, ok := ec2LocalGatewayRoutes.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableID.NotFound", "local gateway route not found", http.StatusBadRequest)
		return
	}
	route.State = "deleted"
	ec2LocalGatewayRoutes.Delete(key)
	ec2Response(w, "DeleteLocalGatewayRoute", "<route>"+localGatewayRouteBodyXML(route)+"</route>")
}

func localGatewayRouteBodyXML(route EC2LocalGatewayRoute) string {
	var b strings.Builder
	if route.DestinationCidrBlock != "" {
		fmt.Fprintf(&b, "<destinationCidrBlock>%s</destinationCidrBlock>", route.DestinationCidrBlock)
	}
	if route.DestinationPrefixListId != "" {
		fmt.Fprintf(&b, "<destinationPrefixListId>%s</destinationPrefixListId>", route.DestinationPrefixListId)
	}
	if route.LocalGatewayVirtualInterfaceGroupId != "" {
		fmt.Fprintf(&b, "<localGatewayVirtualInterfaceGroupId>%s</localGatewayVirtualInterfaceGroupId>", route.LocalGatewayVirtualInterfaceGroupId)
	}
	if route.NetworkInterfaceId != "" {
		fmt.Fprintf(&b, "<networkInterfaceId>%s</networkInterfaceId>", route.NetworkInterfaceId)
	}
	fmt.Fprintf(&b, "<type>%s</type>", route.Type)
	fmt.Fprintf(&b, "<state>%s</state>", route.State)
	fmt.Fprintf(&b, "<localGatewayRouteTableId>%s</localGatewayRouteTableId>", route.LocalGatewayRouteTableId)
	fmt.Fprintf(&b, "<localGatewayRouteTableArn>%s</localGatewayRouteTableArn>", route.LocalGatewayRouteTableArn)
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", route.OwnerId)
	return b.String()
}
