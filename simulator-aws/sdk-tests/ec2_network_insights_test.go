package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_NetworkInsightsReachability exercises the Reachability Analyzer
// path → analysis CRUD: create a path, run an analysis (settles succeeded with
// a reachable path), describe, then tear both down.
func TestEC2_NetworkInsightsReachability(t *testing.T) {
	client := ec2Client()

	pathOut, err := client.CreateNetworkInsightsPath(ctx, &ec2.CreateNetworkInsightsPathInput{
		Source:          aws.String("eni-source"),
		Destination:     aws.String("eni-destination"),
		Protocol:        types.ProtocolTcp,
		DestinationPort: aws.Int32(443),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeNetworkInsightsPath,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("nip-sdk")}},
		}},
	})
	require.NoError(t, err)
	path := pathOut.NetworkInsightsPath
	require.NotNil(t, path)
	pathID := aws.ToString(path.NetworkInsightsPathId)
	assert.Contains(t, pathID, "nip-")
	assert.Contains(t, aws.ToString(path.NetworkInsightsPathArn), "network-insights-path/")
	assert.Equal(t, int32(443), aws.ToInt32(path.DestinationPort))
	defer func() {
		_, _ = client.DeleteNetworkInsightsPath(ctx, &ec2.DeleteNetworkInsightsPathInput{
			NetworkInsightsPathId: aws.String(pathID),
		})
	}()

	descPath, err := client.DescribeNetworkInsightsPaths(ctx, &ec2.DescribeNetworkInsightsPathsInput{
		NetworkInsightsPathIds: []string{pathID},
	})
	require.NoError(t, err)
	require.Len(t, descPath.NetworkInsightsPaths, 1)
	assert.Equal(t, pathID, aws.ToString(descPath.NetworkInsightsPaths[0].NetworkInsightsPathId))

	anaOut, err := client.StartNetworkInsightsAnalysis(ctx, &ec2.StartNetworkInsightsAnalysisInput{
		NetworkInsightsPathId: aws.String(pathID),
	})
	require.NoError(t, err)
	ana := anaOut.NetworkInsightsAnalysis
	require.NotNil(t, ana)
	anaID := aws.ToString(ana.NetworkInsightsAnalysisId)
	assert.Contains(t, anaID, "nia-")
	assert.Equal(t, types.AnalysisStatusSucceeded, ana.Status)
	assert.True(t, aws.ToBool(ana.NetworkPathFound))
	defer func() {
		_, _ = client.DeleteNetworkInsightsAnalysis(ctx, &ec2.DeleteNetworkInsightsAnalysisInput{
			NetworkInsightsAnalysisId: aws.String(anaID),
		})
	}()

	descAna, err := client.DescribeNetworkInsightsAnalyses(ctx, &ec2.DescribeNetworkInsightsAnalysesInput{
		NetworkInsightsAnalysisIds: []string{anaID},
	})
	require.NoError(t, err)
	require.Len(t, descAna.NetworkInsightsAnalyses, 1)
	assert.Equal(t, pathID, aws.ToString(descAna.NetworkInsightsAnalyses[0].NetworkInsightsPathId))
}

// TestEC2_NetworkInsightsAccessScope exercises the Network Access Analyzer
// access-scope path: create scope → get content → run scope analysis → get
// findings (none) → teardown.
func TestEC2_NetworkInsightsAccessScope(t *testing.T) {
	client := ec2Client()

	scopeOut, err := client.CreateNetworkInsightsAccessScope(ctx, &ec2.CreateNetworkInsightsAccessScopeInput{
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeNetworkInsightsAccessScope,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("nis-sdk")}},
		}},
	})
	require.NoError(t, err)
	scope := scopeOut.NetworkInsightsAccessScope
	require.NotNil(t, scope)
	scopeID := aws.ToString(scope.NetworkInsightsAccessScopeId)
	assert.Contains(t, scopeID, "nis-")
	assert.Contains(t, aws.ToString(scope.NetworkInsightsAccessScopeArn), "network-insights-access-scope/")
	require.NotNil(t, scopeOut.NetworkInsightsAccessScopeContent)
	assert.Equal(t, scopeID, aws.ToString(scopeOut.NetworkInsightsAccessScopeContent.NetworkInsightsAccessScopeId))
	defer func() {
		_, _ = client.DeleteNetworkInsightsAccessScope(ctx, &ec2.DeleteNetworkInsightsAccessScopeInput{
			NetworkInsightsAccessScopeId: aws.String(scopeID),
		})
	}()

	descScope, err := client.DescribeNetworkInsightsAccessScopes(ctx, &ec2.DescribeNetworkInsightsAccessScopesInput{
		NetworkInsightsAccessScopeIds: []string{scopeID},
	})
	require.NoError(t, err)
	require.Len(t, descScope.NetworkInsightsAccessScopes, 1)

	contentOut, err := client.GetNetworkInsightsAccessScopeContent(ctx, &ec2.GetNetworkInsightsAccessScopeContentInput{
		NetworkInsightsAccessScopeId: aws.String(scopeID),
	})
	require.NoError(t, err)
	require.NotNil(t, contentOut.NetworkInsightsAccessScopeContent)
	assert.Equal(t, scopeID, aws.ToString(contentOut.NetworkInsightsAccessScopeContent.NetworkInsightsAccessScopeId))

	scopeAnaOut, err := client.StartNetworkInsightsAccessScopeAnalysis(ctx, &ec2.StartNetworkInsightsAccessScopeAnalysisInput{
		NetworkInsightsAccessScopeId: aws.String(scopeID),
	})
	require.NoError(t, err)
	scopeAna := scopeAnaOut.NetworkInsightsAccessScopeAnalysis
	require.NotNil(t, scopeAna)
	scopeAnaID := aws.ToString(scopeAna.NetworkInsightsAccessScopeAnalysisId)
	assert.Contains(t, scopeAnaID, "nisa-")
	assert.Equal(t, types.AnalysisStatusSucceeded, scopeAna.Status)
	assert.Equal(t, types.FindingsFoundFalse, scopeAna.FindingsFound)
	defer func() {
		_, _ = client.DeleteNetworkInsightsAccessScopeAnalysis(ctx, &ec2.DeleteNetworkInsightsAccessScopeAnalysisInput{
			NetworkInsightsAccessScopeAnalysisId: aws.String(scopeAnaID),
		})
	}()

	descScopeAna, err := client.DescribeNetworkInsightsAccessScopeAnalyses(ctx, &ec2.DescribeNetworkInsightsAccessScopeAnalysesInput{
		NetworkInsightsAccessScopeAnalysisIds: []string{scopeAnaID},
	})
	require.NoError(t, err)
	require.Len(t, descScopeAna.NetworkInsightsAccessScopeAnalyses, 1)

	findingsOut, err := client.GetNetworkInsightsAccessScopeAnalysisFindings(ctx, &ec2.GetNetworkInsightsAccessScopeAnalysisFindingsInput{
		NetworkInsightsAccessScopeAnalysisId: aws.String(scopeAnaID),
	})
	require.NoError(t, err)
	assert.Equal(t, types.AnalysisStatusSucceeded, findingsOut.AnalysisStatus)
	assert.Empty(t, findingsOut.AnalysisFindings)
}

// TestEC2_RouteServerCore drives the full route-server graph: route server →
// VPC association → endpoint → BGP peer → route-table propagation → routing
// database, plus modify and tolerant teardown.
func TestEC2_RouteServerCore(t *testing.T) {
	client := ec2Client()

	// VPC + subnet to anchor the endpoint.
	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.70.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)
	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.70.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)

	rsOut, err := client.CreateRouteServer(ctx, &ec2.CreateRouteServerInput{
		AmazonSideAsn: aws.Int64(65001),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeRouteServer,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("rs-sdk")}},
		}},
	})
	require.NoError(t, err)
	rs := rsOut.RouteServer
	require.NotNil(t, rs)
	rsID := aws.ToString(rs.RouteServerId)
	assert.Contains(t, rsID, "rs-")
	assert.Equal(t, int64(65001), aws.ToInt64(rs.AmazonSideAsn))
	assert.Equal(t, types.RouteServerStateAvailable, rs.State)
	defer func() {
		_, _ = client.DeleteRouteServer(ctx, &ec2.DeleteRouteServerInput{RouteServerId: aws.String(rsID)})
	}()

	descRS, err := client.DescribeRouteServers(ctx, &ec2.DescribeRouteServersInput{RouteServerIds: []string{rsID}})
	require.NoError(t, err)
	require.Len(t, descRS.RouteServers, 1)

	modRS, err := client.ModifyRouteServer(ctx, &ec2.ModifyRouteServerInput{
		RouteServerId: aws.String(rsID),
		PersistRoutes: types.RouteServerPersistRoutesActionEnable,
	})
	require.NoError(t, err)
	assert.Equal(t, types.RouteServerPersistRoutesStateEnabled, modRS.RouteServer.PersistRoutesState)

	// Associate to the VPC.
	assocOut, err := client.AssociateRouteServer(ctx, &ec2.AssociateRouteServerInput{
		RouteServerId: aws.String(rsID), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	require.NotNil(t, assocOut.RouteServerAssociation)
	assert.Equal(t, vpcID, aws.ToString(assocOut.RouteServerAssociation.VpcId))
	assert.Equal(t, types.RouteServerAssociationStateAssociated, assocOut.RouteServerAssociation.State)

	getAssoc, err := client.GetRouteServerAssociations(ctx, &ec2.GetRouteServerAssociationsInput{
		RouteServerId: aws.String(rsID),
	})
	require.NoError(t, err)
	require.Len(t, getAssoc.RouteServerAssociations, 1)
	assert.Equal(t, vpcID, aws.ToString(getAssoc.RouteServerAssociations[0].VpcId))

	// Endpoint.
	epOut, err := client.CreateRouteServerEndpoint(ctx, &ec2.CreateRouteServerEndpointInput{
		RouteServerId: aws.String(rsID), SubnetId: aws.String(subnetID),
	})
	require.NoError(t, err)
	ep := epOut.RouteServerEndpoint
	require.NotNil(t, ep)
	epID := aws.ToString(ep.RouteServerEndpointId)
	assert.Contains(t, epID, "rse-")
	assert.Equal(t, subnetID, aws.ToString(ep.SubnetId))
	defer func() {
		_, _ = client.DeleteRouteServerEndpoint(ctx, &ec2.DeleteRouteServerEndpointInput{
			RouteServerEndpointId: aws.String(epID),
		})
	}()

	descEP, err := client.DescribeRouteServerEndpoints(ctx, &ec2.DescribeRouteServerEndpointsInput{
		RouteServerEndpointIds: []string{epID},
	})
	require.NoError(t, err)
	require.Len(t, descEP.RouteServerEndpoints, 1)

	// Peer.
	peerOut, err := client.CreateRouteServerPeer(ctx, &ec2.CreateRouteServerPeerInput{
		RouteServerEndpointId: aws.String(epID),
		PeerAddress:           aws.String("10.70.1.20"),
		BgpOptions: &types.RouteServerBgpOptionsRequest{
			PeerAsn:               aws.Int64(65002),
			PeerLivenessDetection: types.RouteServerPeerLivenessModeBgpKeepalive,
		},
	})
	require.NoError(t, err)
	peer := peerOut.RouteServerPeer
	require.NotNil(t, peer)
	peerID := aws.ToString(peer.RouteServerPeerId)
	assert.Contains(t, peerID, "rsp-")
	assert.Equal(t, "10.70.1.20", aws.ToString(peer.PeerAddress))
	require.NotNil(t, peer.BgpOptions)
	assert.Equal(t, int64(65002), aws.ToInt64(peer.BgpOptions.PeerAsn))
	defer func() {
		_, _ = client.DeleteRouteServerPeer(ctx, &ec2.DeleteRouteServerPeerInput{
			RouteServerPeerId: aws.String(peerID),
		})
	}()

	descPeer, err := client.DescribeRouteServerPeers(ctx, &ec2.DescribeRouteServerPeersInput{
		RouteServerPeerIds: []string{peerID},
	})
	require.NoError(t, err)
	require.Len(t, descPeer.RouteServerPeers, 1)

	// Route-table propagation.
	rtOut, err := client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	rtID := aws.ToString(rtOut.RouteTable.RouteTableId)

	enProp, err := client.EnableRouteServerPropagation(ctx, &ec2.EnableRouteServerPropagationInput{
		RouteServerId: aws.String(rsID), RouteTableId: aws.String(rtID),
	})
	require.NoError(t, err)
	require.NotNil(t, enProp.RouteServerPropagation)
	assert.Equal(t, rtID, aws.ToString(enProp.RouteServerPropagation.RouteTableId))

	getProp, err := client.GetRouteServerPropagations(ctx, &ec2.GetRouteServerPropagationsInput{
		RouteServerId: aws.String(rsID),
	})
	require.NoError(t, err)
	require.Len(t, getProp.RouteServerPropagations, 1)
	assert.Equal(t, rtID, aws.ToString(getProp.RouteServerPropagations[0].RouteTableId))

	dbOut, err := client.GetRouteServerRoutingDatabase(ctx, &ec2.GetRouteServerRoutingDatabaseInput{
		RouteServerId: aws.String(rsID),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dbOut.AreRoutesPersisted))
	assert.Empty(t, dbOut.Routes)

	_, err = client.DisableRouteServerPropagation(ctx, &ec2.DisableRouteServerPropagationInput{
		RouteServerId: aws.String(rsID), RouteTableId: aws.String(rtID),
	})
	require.NoError(t, err)

	_, err = client.DisassociateRouteServer(ctx, &ec2.DisassociateRouteServerInput{
		RouteServerId: aws.String(rsID), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
}

// TestEC2_LocalGatewayRouteCRUD exercises the local-gateway-route CRUD path on
// a local-gateway-route-table id: create → search → modify → delete.
func TestEC2_LocalGatewayRouteCRUD(t *testing.T) {
	client := ec2Client()

	rtID := "lgw-rtb-0123456789abcdef0"
	routeOut, err := client.CreateLocalGatewayRoute(ctx, &ec2.CreateLocalGatewayRouteInput{
		LocalGatewayRouteTableId:            aws.String(rtID),
		DestinationCidrBlock:                aws.String("192.168.10.0/24"),
		LocalGatewayVirtualInterfaceGroupId: aws.String("lgw-vif-grp-0123456789abcdef0"),
	})
	require.NoError(t, err)
	route := routeOut.Route
	require.NotNil(t, route)
	assert.Equal(t, "192.168.10.0/24", aws.ToString(route.DestinationCidrBlock))
	assert.Equal(t, types.LocalGatewayRouteTypeStatic, route.Type)
	assert.Equal(t, types.LocalGatewayRouteStateActive, route.State)
	assert.Equal(t, rtID, aws.ToString(route.LocalGatewayRouteTableId))

	searchOut, err := client.SearchLocalGatewayRoutes(ctx, &ec2.SearchLocalGatewayRoutesInput{
		LocalGatewayRouteTableId: aws.String(rtID),
		Filters: []types.Filter{{
			Name:   aws.String("type"),
			Values: []string{"static"},
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, searchOut.Routes)
	var found bool
	for _, rt := range searchOut.Routes {
		if aws.ToString(rt.DestinationCidrBlock) == "192.168.10.0/24" {
			found = true
		}
	}
	assert.True(t, found, "created route should appear in search results")

	modOut, err := client.ModifyLocalGatewayRoute(ctx, &ec2.ModifyLocalGatewayRouteInput{
		LocalGatewayRouteTableId: aws.String(rtID),
		DestinationCidrBlock:     aws.String("192.168.10.0/24"),
		NetworkInterfaceId:       aws.String("eni-0123456789abcdef0"),
	})
	require.NoError(t, err)
	assert.Equal(t, "eni-0123456789abcdef0", aws.ToString(modOut.Route.NetworkInterfaceId))

	delOut, err := client.DeleteLocalGatewayRoute(ctx, &ec2.DeleteLocalGatewayRouteInput{
		LocalGatewayRouteTableId: aws.String(rtID),
		DestinationCidrBlock:     aws.String("192.168.10.0/24"),
	})
	require.NoError(t, err)
	assert.Equal(t, types.LocalGatewayRouteStateDeleted, delOut.Route.State)
}
