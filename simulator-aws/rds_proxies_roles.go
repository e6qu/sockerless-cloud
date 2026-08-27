package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// RDS proxies, IAM-role associations, DB security groups, certificates,
// automated backups, log files, parameter/option-group copies, event
// subscription source identifiers, and pending maintenance actions —
// the awsQuery surface beyond the core instance/cluster lifecycle in
// rds.go. Every handler performs faithful CRUD against a real sim.Store
// and emits the exact awsQuery XML shape the AWS SDK Go v2 and aws CLI
// parse (per specs/cloud-api/aws/rds.smithy.json.gz). No database engine
// is simulated; statuses settle to "available"/"active" inline.

// RDSProxy models an RDS Proxy — a managed connection pooler fronting a
// set of DB instances/clusters. Status settles to "available" inline.
type RDSProxy struct {
	DBProxyName       string
	EngineFamily      string
	RoleArn           string
	VpcId             string
	VpcSubnetIds      []string
	VpcSecurityGroups []string
	RequireTLS        bool
	IdleClientTimeout int
	DebugLogging      bool
	Status            string
	Endpoint          string
	CreatedDate       string
	UpdatedDate       string
	ARN               string
	Tags              map[string]string
}

// RDSProxyEndpoint models an additional DB proxy endpoint (read/write or
// read-only) attached to a proxy.
type RDSProxyEndpoint struct {
	DBProxyEndpointName string
	DBProxyName         string
	VpcId               string
	VpcSubnetIds        []string
	VpcSecurityGroups   []string
	Status              string
	Endpoint            string
	TargetRole          string
	IsDefault           bool
	CreatedDate         string
	ARN                 string
	Tags                map[string]string
}

// RDSProxyTarget models a single registered target (a DB instance or DB
// cluster) of a proxy's default target group. The key is
// proxyName + "/" + targetID.
type RDSProxyTarget struct {
	DBProxyName      string
	Type             string // RDS_INSTANCE | TRACKED_CLUSTER
	RdsResourceId    string
	TargetArn        string
	TrackedClusterId string
	Endpoint         string
	Port             int
}

// RDSProxyTargetGroup models the default target group of a proxy. RDS
// proxies have exactly one target group named "default".
type RDSProxyTargetGroup struct {
	TargetGroupId             string
	TargetGroupName           string
	DBProxyName               string
	IsDefault                 bool
	Status                    string
	ConnectionBorrowTimeout   int
	MaxConnectionsPercent     int
	MaxIdleConnectionsPercent int
	CreatedDate               string
	UpdatedDate               string
	ARN                       string
}

// RDSDBSecurityGroup models an EC2-Classic-style DB security group with
// CIDR (IPRange) and EC2 security-group ingress rules.
type RDSDBSecurityGroup struct {
	DBSecurityGroupName        string
	DBSecurityGroupDescription string
	OwnerId                    string
	VpcId                      string
	IPRanges                   []rdsIPRange
	EC2SecurityGroups          []rdsEC2SecurityGroup
	ARN                        string
	Tags                       map[string]string
}

type rdsIPRange struct {
	CIDRIP string
	Status string
}

type rdsEC2SecurityGroup struct {
	EC2SecurityGroupId      string
	EC2SecurityGroupName    string
	EC2SecurityGroupOwnerId string
	Status                  string
}

// RDSCertificate models an RDS CA certificate (the SSL/TLS root CA the
// service presents to clients).
type RDSCertificate struct {
	CertificateIdentifier string
	CertificateType       string
	Thumbprint            string
	ValidFrom             string
	ValidTill             string
	CustomerOverride      bool
	ARN                   string
}

// RDSInstanceAutomatedBackup models a per-instance automated backup row.
type RDSInstanceAutomatedBackup struct {
	DBInstanceAutomatedBackupsArn string
	DBInstanceIdentifier          string
	DbiResourceId                 string
	Region                        string
	Engine                        string
	EngineVersion                 string
	Status                        string
	AllocatedStorage              int
	MasterUsername                string
	Port                          int
	InstanceCreateTime            string
	BackupRetentionPeriod         int
	EarliestTime                  string
	LatestTime                    string
}

// RDSClusterAutomatedBackup models a per-cluster automated backup row.
type RDSClusterAutomatedBackup struct {
	DBClusterAutomatedBackupsArn string
	DBClusterIdentifier          string
	DbClusterResourceId          string
	Region                       string
	Engine                       string
	EngineVersion                string
	Status                       string
	AllocatedStorage             int
	MasterUsername               string
	Port                         int
	ClusterCreateTime            string
	BackupRetentionPeriod        int
	EarliestTime                 string
	LatestTime                   string
}

// rdsRole models one IAM-role association (RoleArn + FeatureName) on a
// DB cluster or DB instance. Keyed by resourceID in its store.
type rdsRoleSet struct {
	ResourceID string
	Roles      []rdsRole
}

type rdsRole struct {
	RoleArn     string
	FeatureName string
	Status      string
}

// rdsMaintenanceActions holds the pending maintenance actions for one
// resource (keyed by the resource's ARN).
type rdsMaintenanceActions struct {
	ResourceIdentifier string
	Actions            []rdsPendingAction
}

type rdsPendingAction struct {
	Action               string
	OptInStatus          string
	AutoAppliedAfterDate string
	CurrentApplyDate     string
	ForcedApplyDate      string
	Description          string
}

var (
	rdsProxies                  sim.Store[RDSProxy]
	rdsProxyEndpoints           sim.Store[RDSProxyEndpoint]
	rdsProxyTargets             sim.Store[RDSProxyTarget]
	rdsProxyTargetGroups        sim.Store[RDSProxyTargetGroup]
	rdsDBSecurityGroups         sim.Store[RDSDBSecurityGroup]
	rdsCertificates             sim.Store[RDSCertificate]
	rdsInstanceAutomatedBackups sim.Store[RDSInstanceAutomatedBackup]
	rdsClusterAutomatedBackups  sim.Store[RDSClusterAutomatedBackup]
	rdsClusterRoles             sim.Store[rdsRoleSet]
	rdsInstanceRoles            sim.Store[rdsRoleSet]
	rdsPendingMaintenance       sim.Store[rdsMaintenanceActions]
)

func registerRDSProxiesRoles(r *sim.AWSQueryRouter, srv *sim.Server) {
	rdsProxies = sim.MakeStore[RDSProxy](srv.DB(), "rds_proxies")
	rdsProxyEndpoints = sim.MakeStore[RDSProxyEndpoint](srv.DB(), "rds_proxy_endpoints")
	rdsProxyTargets = sim.MakeStore[RDSProxyTarget](srv.DB(), "rds_proxy_targets")
	rdsProxyTargetGroups = sim.MakeStore[RDSProxyTargetGroup](srv.DB(), "rds_proxy_target_groups")
	rdsDBSecurityGroups = sim.MakeStore[RDSDBSecurityGroup](srv.DB(), "rds_db_security_groups")
	rdsCertificates = sim.MakeStore[RDSCertificate](srv.DB(), "rds_certificates")
	rdsInstanceAutomatedBackups = sim.MakeStore[RDSInstanceAutomatedBackup](srv.DB(), "rds_instance_automated_backups")
	rdsClusterAutomatedBackups = sim.MakeStore[RDSClusterAutomatedBackup](srv.DB(), "rds_cluster_automated_backups")
	rdsClusterRoles = sim.MakeStore[rdsRoleSet](srv.DB(), "rds_cluster_roles")
	rdsInstanceRoles = sim.MakeStore[rdsRoleSet](srv.DB(), "rds_instance_roles")
	rdsPendingMaintenance = sim.MakeStore[rdsMaintenanceActions](srv.DB(), "rds_pending_maintenance")

	// DB proxies
	r.RegisterVersioned(rdsAPIVersion, "CreateDBProxy", handleRDSCreateProxy)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBProxies", handleRDSDescribeProxies)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBProxy", handleRDSModifyProxy)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBProxy", handleRDSDeleteProxy)
	r.RegisterVersioned(rdsAPIVersion, "CreateDBProxyEndpoint", handleRDSCreateProxyEndpoint)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBProxyEndpoints", handleRDSDescribeProxyEndpoints)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBProxyEndpoint", handleRDSModifyProxyEndpoint)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBProxyEndpoint", handleRDSDeleteProxyEndpoint)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBProxyTargets", handleRDSDescribeProxyTargets)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBProxyTargetGroups", handleRDSDescribeProxyTargetGroups)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBProxyTargetGroup", handleRDSModifyProxyTargetGroup)
	r.RegisterVersioned(rdsAPIVersion, "RegisterDBProxyTargets", handleRDSRegisterProxyTargets)
	r.RegisterVersioned(rdsAPIVersion, "DeregisterDBProxyTargets", handleRDSDeregisterProxyTargets)

	// IAM role associations
	r.RegisterVersioned(rdsAPIVersion, "AddRoleToDBCluster", handleRDSAddRoleToCluster)
	r.RegisterVersioned(rdsAPIVersion, "RemoveRoleFromDBCluster", handleRDSRemoveRoleFromCluster)
	r.RegisterVersioned(rdsAPIVersion, "AddRoleToDBInstance", handleRDSAddRoleToInstance)
	r.RegisterVersioned(rdsAPIVersion, "RemoveRoleFromDBInstance", handleRDSRemoveRoleFromInstance)

	// DB security groups (EC2-Classic style)
	r.RegisterVersioned(rdsAPIVersion, "CreateDBSecurityGroup", handleRDSCreateSecurityGroup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBSecurityGroups", handleRDSDescribeSecurityGroups)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBSecurityGroup", handleRDSDeleteSecurityGroup)
	r.RegisterVersioned(rdsAPIVersion, "AuthorizeDBSecurityGroupIngress", handleRDSAuthorizeSecurityGroupIngress)
	r.RegisterVersioned(rdsAPIVersion, "RevokeDBSecurityGroupIngress", handleRDSRevokeSecurityGroupIngress)

	// Certificates
	r.RegisterVersioned(rdsAPIVersion, "DescribeCertificates", handleRDSDescribeCertificates)
	r.RegisterVersioned(rdsAPIVersion, "ModifyCertificates", handleRDSModifyCertificates)

	// Automated backups
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBInstanceAutomatedBackups", handleRDSDescribeInstanceAutomatedBackups)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBInstanceAutomatedBackup", handleRDSDeleteInstanceAutomatedBackup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterAutomatedBackups", handleRDSDescribeClusterAutomatedBackups)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBClusterAutomatedBackup", handleRDSDeleteClusterAutomatedBackup)

	// Log files
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBLogFiles", handleRDSDescribeLogFiles)
	r.RegisterVersioned(rdsAPIVersion, "DownloadDBLogFilePortion", handleRDSDownloadLogFilePortion)

	// Parameter / option group copies
	r.RegisterVersioned(rdsAPIVersion, "CopyDBClusterParameterGroup", handleRDSCopyClusterParameterGroup)
	r.RegisterVersioned(rdsAPIVersion, "CopyDBParameterGroup", handleRDSCopyParameterGroup)
	r.RegisterVersioned(rdsAPIVersion, "CopyOptionGroup", handleRDSCopyOptionGroup)

	// Event subscription source identifiers
	r.RegisterVersioned(rdsAPIVersion, "AddSourceIdentifierToSubscription", handleRDSAddSourceIdentifier)
	r.RegisterVersioned(rdsAPIVersion, "RemoveSourceIdentifierFromSubscription", handleRDSRemoveSourceIdentifier)

	// Pending maintenance actions
	r.RegisterVersioned(rdsAPIVersion, "ApplyPendingMaintenanceAction", handleRDSApplyPendingMaintenanceAction)
	r.RegisterVersioned(rdsAPIVersion, "DescribePendingMaintenanceActions", handleRDSDescribePendingMaintenanceActions)
}

func rdsProxyARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:db-proxy:%s", awsRegion(), awsAccountID(), name)
}

func rdsProxyEndpointARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:db-proxy-endpoint:%s", awsRegion(), awsAccountID(), name)
}

// rdsTargetGroupARN builds the ARN AWS publishes for a proxy target group:
// "target-group:<id>", one identifier rather than the proxy and group names
// that address it in a request.
func rdsTargetGroupARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:target-group:%s", awsRegion(), awsAccountID(), id)
}

// rdsTargetGroupID mints the identifier AWS assigns a proxy target group,
// in the shape its own identifiers take.
func rdsTargetGroupID() string {
	return "prx-tg-" + strings.ReplaceAll(generateUUID(), "-", "")[:17]
}

func rdsSecurityGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:secgrp:%s", awsRegion(), awsAccountID(), name)
}

func rdsCertificateARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s::cert:%s", awsRegion(), id)
}

func rdsInstanceAutoBackupARN(resID string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:auto-backup:%s", awsRegion(), awsAccountID(), resID)
}

func rdsClusterAutoBackupARN(resID string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-auto-backup:%s", awsRegion(), awsAccountID(), resID)
}

// DB proxies

func renderRDSProxyInner(p RDSProxy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DBProxyName>%s</DBProxyName>", xmlEscape(p.DBProxyName))
	fmt.Fprintf(&b, "<DBProxyArn>%s</DBProxyArn>", xmlEscape(p.ARN))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(p.Status))
	fmt.Fprintf(&b, "<EngineFamily>%s</EngineFamily>", xmlEscape(p.EngineFamily))
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(p.VpcId))
	b.WriteString("<VpcSecurityGroupIds>")
	for _, sg := range p.VpcSecurityGroups {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(sg))
	}
	b.WriteString("</VpcSecurityGroupIds>")
	b.WriteString("<VpcSubnetIds>")
	for _, sn := range p.VpcSubnetIds {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(sn))
	}
	b.WriteString("</VpcSubnetIds>")
	fmt.Fprintf(&b, "<RoleArn>%s</RoleArn>", xmlEscape(p.RoleArn))
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(p.Endpoint))
	fmt.Fprintf(&b, "<RequireTLS>%t</RequireTLS>", p.RequireTLS)
	fmt.Fprintf(&b, "<IdleClientTimeout>%d</IdleClientTimeout>", p.IdleClientTimeout)
	fmt.Fprintf(&b, "<DebugLogging>%t</DebugLogging>", p.DebugLogging)
	fmt.Fprintf(&b, "<CreatedDate>%s</CreatedDate>", xmlEscape(p.CreatedDate))
	fmt.Fprintf(&b, "<UpdatedDate>%s</UpdatedDate>", xmlEscape(p.UpdatedDate))
	return b.String()
}

// renderRDSProxy wraps the proxy in a <DBProxy> element (the single-field
// shape used by Create/Modify/Delete responses).
func renderRDSProxy(p RDSProxy) string {
	return "<DBProxy>" + renderRDSProxyInner(p) + "</DBProxy>"
}

// renderRDSProxyMember wraps the proxy in a <member> element (the list
// element shape DBProxyList uses, since its member carries no xmlName).
func renderRDSProxyMember(p RDSProxy) string {
	return "<member>" + renderRDSProxyInner(p) + "</member>"
}

func handleRDSCreateProxy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBProxyName")
	if name == "" {
		rdsErrorXML(w, "MissingParameter", "DBProxyName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsProxies.Get(name); ok {
		rdsErrorXML(w, "DBProxyAlreadyExistsFault", "DB proxy already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	idle := 1800
	if v := atoiOrZero(r.FormValue("IdleClientTimeout")); v > 0 {
		idle = v
	}
	now := time.Now().UTC().Format(time.RFC3339)
	p := RDSProxy{
		DBProxyName:       name,
		EngineFamily:      r.FormValue("EngineFamily"),
		RoleArn:           r.FormValue("RoleArn"),
		VpcId:             "vpc-sim00000000000000",
		VpcSubnetIds:      parseRDSIndexedStringList(r, "VpcSubnetIds.member"),
		VpcSecurityGroups: parseRDSIndexedStringList(r, "VpcSecurityGroupIds.member"),
		RequireTLS:        r.FormValue("RequireTLS") == "true",
		IdleClientTimeout: idle,
		DebugLogging:      r.FormValue("DebugLogging") == "true",
		Status:            "available",
		Endpoint:          fmt.Sprintf("%s.proxy-sim.%s.rds.amazonaws.com", name, awsRegion()),
		CreatedDate:       now,
		UpdatedDate:       now,
		ARN:               rdsProxyARN(name),
		Tags:              parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsProxies.Put(name, p)
	// A proxy is created with a default target group.
	targetGroupID := rdsTargetGroupID()
	tg := RDSProxyTargetGroup{
		TargetGroupId:             targetGroupID,
		TargetGroupName:           "default",
		DBProxyName:               name,
		IsDefault:                 true,
		Status:                    "available",
		ConnectionBorrowTimeout:   120,
		MaxConnectionsPercent:     100,
		MaxIdleConnectionsPercent: 50,
		CreatedDate:               now,
		UpdatedDate:               now,
		ARN:                       rdsTargetGroupARN(targetGroupID),
	}
	rdsProxyTargetGroups.Put(name+"/default", tg)
	rdsXMLResponse(w, "CreateDBProxy", renderRDSProxy(p), sim.RequestID(r.Context()))
}

func handleRDSDescribeProxies(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBProxyName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBProxies>")
	for _, p := range rdsProxies.List() {
		if wanted != "" && p.DBProxyName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSProxyMember(p))
	}
	b.WriteString("</DBProxies>")
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeDBProxies", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyProxy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBProxyName")
	if _, ok := rdsProxies.Get(name); !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsProxies.Update(name, func(p *RDSProxy) {
		if v := r.FormValue("NewDBProxyName"); v != "" {
			p.DBProxyName = v
		}
		if v := r.FormValue("RoleArn"); v != "" {
			p.RoleArn = v
		}
		if v := r.FormValue("RequireTLS"); v != "" {
			p.RequireTLS = v == "true"
		}
		if v := r.FormValue("DebugLogging"); v != "" {
			p.DebugLogging = v == "true"
		}
		if v := atoiOrZero(r.FormValue("IdleClientTimeout")); v > 0 {
			p.IdleClientTimeout = v
		}
		if sgs := parseRDSIndexedStringList(r, "SecurityGroups.member"); len(sgs) > 0 {
			p.VpcSecurityGroups = sgs
		}
		p.UpdatedDate = time.Now().UTC().Format(time.RFC3339)
	})
	updated, _ := rdsProxies.Get(name)
	if updated.DBProxyName != name {
		// Renamed: re-key the row.
		rdsProxies.Delete(name)
		updated.ARN = rdsProxyARN(updated.DBProxyName)
		rdsProxies.Put(updated.DBProxyName, updated)
	}
	rdsXMLResponse(w, "ModifyDBProxy", renderRDSProxy(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteProxy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBProxyName")
	p, ok := rdsProxies.Get(name)
	if !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	p.Status = "deleting"
	rdsProxies.Delete(name)
	rdsProxyTargetGroups.Delete(name + "/default")
	for _, t := range rdsProxyTargets.List() {
		if t.DBProxyName == name {
			rdsProxyTargets.Delete(rdsProxyTargetKey(name, t))
		}
	}
	rdsXMLResponse(w, "DeleteDBProxy", renderRDSProxy(p), sim.RequestID(r.Context()))
}

// DB proxy endpoints

func renderRDSProxyEndpointInner(e RDSProxyEndpoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DBProxyEndpointName>%s</DBProxyEndpointName>", xmlEscape(e.DBProxyEndpointName))
	fmt.Fprintf(&b, "<DBProxyEndpointArn>%s</DBProxyEndpointArn>", xmlEscape(e.ARN))
	fmt.Fprintf(&b, "<DBProxyName>%s</DBProxyName>", xmlEscape(e.DBProxyName))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(e.Status))
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(e.VpcId))
	b.WriteString("<VpcSecurityGroupIds>")
	for _, sg := range e.VpcSecurityGroups {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(sg))
	}
	b.WriteString("</VpcSecurityGroupIds>")
	b.WriteString("<VpcSubnetIds>")
	for _, sn := range e.VpcSubnetIds {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(sn))
	}
	b.WriteString("</VpcSubnetIds>")
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(e.Endpoint))
	fmt.Fprintf(&b, "<CreatedDate>%s</CreatedDate>", xmlEscape(e.CreatedDate))
	fmt.Fprintf(&b, "<TargetRole>%s</TargetRole>", xmlEscape(e.TargetRole))
	fmt.Fprintf(&b, "<IsDefault>%t</IsDefault>", e.IsDefault)
	return b.String()
}

func renderRDSProxyEndpoint(e RDSProxyEndpoint) string {
	return "<DBProxyEndpoint>" + renderRDSProxyEndpointInner(e) + "</DBProxyEndpoint>"
}

func renderRDSProxyEndpointMember(e RDSProxyEndpoint) string {
	return "<member>" + renderRDSProxyEndpointInner(e) + "</member>"
}

func handleRDSCreateProxyEndpoint(w http.ResponseWriter, r *http.Request) {
	proxyName := r.FormValue("DBProxyName")
	epName := r.FormValue("DBProxyEndpointName")
	if proxyName == "" || epName == "" {
		rdsErrorXML(w, "MissingParameter", "DBProxyName and DBProxyEndpointName are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	proxy, ok := rdsProxies.Get(proxyName)
	if !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", proxyName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsProxyEndpoints.Get(epName); ok {
		rdsErrorXML(w, "DBProxyEndpointAlreadyExistsFault", "DB proxy endpoint already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	role := r.FormValue("TargetRole")
	if role == "" {
		role = "READ_WRITE"
	}
	sn := parseRDSIndexedStringList(r, "VpcSubnetIds.member")
	if len(sn) == 0 {
		sn = proxy.VpcSubnetIds
	}
	sg := parseRDSIndexedStringList(r, "VpcSecurityGroupIds.member")
	if len(sg) == 0 {
		sg = proxy.VpcSecurityGroups
	}
	e := RDSProxyEndpoint{
		DBProxyEndpointName: epName,
		DBProxyName:         proxyName,
		VpcId:               proxy.VpcId,
		VpcSubnetIds:        sn,
		VpcSecurityGroups:   sg,
		Status:              "available",
		Endpoint:            fmt.Sprintf("%s.endpoint.proxy-sim.%s.rds.amazonaws.com", epName, awsRegion()),
		TargetRole:          role,
		IsDefault:           false,
		CreatedDate:         time.Now().UTC().Format(time.RFC3339),
		ARN:                 rdsProxyEndpointARN(epName),
		Tags:                parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsProxyEndpoints.Put(epName, e)
	rdsXMLResponse(w, "CreateDBProxyEndpoint", renderRDSProxyEndpoint(e), sim.RequestID(r.Context()))
}

func handleRDSDescribeProxyEndpoints(w http.ResponseWriter, r *http.Request) {
	wantProxy := r.FormValue("DBProxyName")
	wantEP := r.FormValue("DBProxyEndpointName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBProxyEndpoints>")
	for _, e := range rdsProxyEndpoints.List() {
		if wantProxy != "" && e.DBProxyName != wantProxy {
			continue
		}
		if wantEP != "" && e.DBProxyEndpointName != wantEP {
			continue
		}
		matched = true
		b.WriteString(renderRDSProxyEndpointMember(e))
	}
	b.WriteString("</DBProxyEndpoints>")
	if wantEP != "" && !matched {
		rdsErrorXML(w, "DBProxyEndpointNotFoundFault", fmt.Sprintf("DBProxyEndpoint %q not found", wantEP), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeDBProxyEndpoints", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyProxyEndpoint(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBProxyEndpointName")
	if _, ok := rdsProxyEndpoints.Get(name); !ok {
		rdsErrorXML(w, "DBProxyEndpointNotFoundFault", fmt.Sprintf("DBProxyEndpoint %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsProxyEndpoints.Update(name, func(e *RDSProxyEndpoint) {
		if v := r.FormValue("NewDBProxyEndpointName"); v != "" {
			e.DBProxyEndpointName = v
		}
		if sgs := parseRDSIndexedStringList(r, "VpcSecurityGroupIds.member"); len(sgs) > 0 {
			e.VpcSecurityGroups = sgs
		}
	})
	updated, _ := rdsProxyEndpoints.Get(name)
	if updated.DBProxyEndpointName != name {
		rdsProxyEndpoints.Delete(name)
		updated.ARN = rdsProxyEndpointARN(updated.DBProxyEndpointName)
		rdsProxyEndpoints.Put(updated.DBProxyEndpointName, updated)
	}
	rdsXMLResponse(w, "ModifyDBProxyEndpoint", renderRDSProxyEndpoint(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteProxyEndpoint(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBProxyEndpointName")
	e, ok := rdsProxyEndpoints.Get(name)
	if !ok {
		rdsErrorXML(w, "DBProxyEndpointNotFoundFault", fmt.Sprintf("DBProxyEndpoint %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	e.Status = "deleting"
	rdsProxyEndpoints.Delete(name)
	rdsXMLResponse(w, "DeleteDBProxyEndpoint", renderRDSProxyEndpoint(e), sim.RequestID(r.Context()))
}

// DB proxy targets and target groups

func rdsProxyTargetKey(proxy string, t RDSProxyTarget) string {
	id := t.RdsResourceId
	if t.Type == "TRACKED_CLUSTER" {
		id = t.TrackedClusterId
	}
	return proxy + "/" + id
}

func renderRDSProxyTarget(t RDSProxyTarget) string {
	var b strings.Builder
	b.WriteString("<member>")
	fmt.Fprintf(&b, "<TargetArn>%s</TargetArn>", xmlEscape(t.TargetArn))
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(t.Endpoint))
	fmt.Fprintf(&b, "<RdsResourceId>%s</RdsResourceId>", xmlEscape(t.RdsResourceId))
	if t.TrackedClusterId != "" {
		fmt.Fprintf(&b, "<TrackedClusterId>%s</TrackedClusterId>", xmlEscape(t.TrackedClusterId))
	}
	fmt.Fprintf(&b, "<Port>%d</Port>", t.Port)
	fmt.Fprintf(&b, "<Type>%s</Type>", xmlEscape(t.Type))
	fmt.Fprintf(&b, "<Role>%s</Role>", "READ_WRITE")
	b.WriteString("<TargetHealth><State>AVAILABLE</State></TargetHealth>")
	b.WriteString("</member>")
	return b.String()
}

func renderRDSProxyTargetGroupInner(g RDSProxyTargetGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DBProxyName>%s</DBProxyName>", xmlEscape(g.DBProxyName))
	fmt.Fprintf(&b, "<TargetGroupName>%s</TargetGroupName>", xmlEscape(g.TargetGroupName))
	fmt.Fprintf(&b, "<TargetGroupArn>%s</TargetGroupArn>", xmlEscape(g.ARN))
	fmt.Fprintf(&b, "<IsDefault>%t</IsDefault>", g.IsDefault)
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	b.WriteString("<ConnectionPoolConfig>")
	fmt.Fprintf(&b, "<MaxConnectionsPercent>%d</MaxConnectionsPercent>", g.MaxConnectionsPercent)
	fmt.Fprintf(&b, "<MaxIdleConnectionsPercent>%d</MaxIdleConnectionsPercent>", g.MaxIdleConnectionsPercent)
	fmt.Fprintf(&b, "<ConnectionBorrowTimeout>%d</ConnectionBorrowTimeout>", g.ConnectionBorrowTimeout)
	b.WriteString("<SessionPinningFilters/>")
	b.WriteString("</ConnectionPoolConfig>")
	fmt.Fprintf(&b, "<CreatedDate>%s</CreatedDate>", xmlEscape(g.CreatedDate))
	fmt.Fprintf(&b, "<UpdatedDate>%s</UpdatedDate>", xmlEscape(g.UpdatedDate))
	return b.String()
}

func renderRDSProxyTargetGroupMember(g RDSProxyTargetGroup) string {
	return "<member>" + renderRDSProxyTargetGroupInner(g) + "</member>"
}

func rdsRegisterTargets(proxyName string) []RDSProxyTarget {
	var out []RDSProxyTarget
	for _, t := range rdsProxyTargets.List() {
		if t.DBProxyName == proxyName {
			out = append(out, t)
		}
	}
	return out
}

func handleRDSDescribeProxyTargets(w http.ResponseWriter, r *http.Request) {
	proxyName := r.FormValue("DBProxyName")
	if _, ok := rdsProxies.Get(proxyName); !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", proxyName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<Targets>")
	for _, t := range rdsRegisterTargets(proxyName) {
		b.WriteString(renderRDSProxyTarget(t))
	}
	b.WriteString("</Targets>")
	rdsXMLResponse(w, "DescribeDBProxyTargets", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeProxyTargetGroups(w http.ResponseWriter, r *http.Request) {
	proxyName := r.FormValue("DBProxyName")
	if _, ok := rdsProxies.Get(proxyName); !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", proxyName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<TargetGroups>")
	for _, g := range rdsProxyTargetGroups.List() {
		if g.DBProxyName == proxyName {
			b.WriteString(renderRDSProxyTargetGroupMember(g))
		}
	}
	b.WriteString("</TargetGroups>")
	rdsXMLResponse(w, "DescribeDBProxyTargetGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyProxyTargetGroup(w http.ResponseWriter, r *http.Request) {
	proxyName := r.FormValue("DBProxyName")
	groupName := r.FormValue("TargetGroupName")
	if groupName == "" {
		groupName = "default"
	}
	key := proxyName + "/" + groupName
	if _, ok := rdsProxyTargetGroups.Get(key); !ok {
		rdsErrorXML(w, "DBProxyTargetGroupNotFoundFault", fmt.Sprintf("Target group %q not found", groupName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsProxyTargetGroups.Update(key, func(g *RDSProxyTargetGroup) {
		if v := atoiOrZero(r.FormValue("ConnectionPoolConfig.MaxConnectionsPercent")); v > 0 {
			g.MaxConnectionsPercent = v
		}
		if v := atoiOrZero(r.FormValue("ConnectionPoolConfig.MaxIdleConnectionsPercent")); v > 0 {
			g.MaxIdleConnectionsPercent = v
		}
		if v := atoiOrZero(r.FormValue("ConnectionPoolConfig.ConnectionBorrowTimeout")); v > 0 {
			g.ConnectionBorrowTimeout = v
		}
		g.UpdatedDate = time.Now().UTC().Format(time.RFC3339)
	})
	updated, _ := rdsProxyTargetGroups.Get(key)
	rdsXMLResponse(w, "ModifyDBProxyTargetGroup", "<DBProxyTargetGroup>"+renderRDSProxyTargetGroupInner(updated)+"</DBProxyTargetGroup>", sim.RequestID(r.Context()))
}

func handleRDSRegisterProxyTargets(w http.ResponseWriter, r *http.Request) {
	proxyName := r.FormValue("DBProxyName")
	if _, ok := rdsProxies.Get(proxyName); !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", proxyName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var registered []RDSProxyTarget
	for _, instID := range parseRDSIndexedStringList(r, "DBInstanceIdentifiers.member") {
		inst, ok := rdsInstances.Get(instID)
		if !ok {
			rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", instID), http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
		t := RDSProxyTarget{
			DBProxyName:   proxyName,
			Type:          "RDS_INSTANCE",
			RdsResourceId: inst.DbiResourceId,
			TargetArn:     inst.ARN,
			Endpoint:      inst.Endpoint,
			Port:          inst.Port,
		}
		rdsProxyTargets.Put(rdsProxyTargetKey(proxyName, t), t)
		registered = append(registered, t)
	}
	for _, clID := range parseRDSIndexedStringList(r, "DBClusterIdentifiers.member") {
		cl, ok := rdsClusters.Get(clID)
		if !ok {
			rdsErrorXML(w, "DBClusterNotFoundFault", fmt.Sprintf("DBCluster %q not found", clID), http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
		t := RDSProxyTarget{
			DBProxyName:      proxyName,
			Type:             "TRACKED_CLUSTER",
			RdsResourceId:    cl.DbClusterResourceId,
			TrackedClusterId: cl.DBClusterIdentifier,
			TargetArn:        cl.ARN,
			Endpoint:         cl.Endpoint,
			Port:             cl.Port,
		}
		rdsProxyTargets.Put(rdsProxyTargetKey(proxyName, t), t)
		registered = append(registered, t)
	}
	var b strings.Builder
	b.WriteString("<DBProxyTargets>")
	for _, t := range registered {
		b.WriteString(renderRDSProxyTarget(t))
	}
	b.WriteString("</DBProxyTargets>")
	rdsXMLResponse(w, "RegisterDBProxyTargets", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeregisterProxyTargets(w http.ResponseWriter, r *http.Request) {
	proxyName := r.FormValue("DBProxyName")
	if _, ok := rdsProxies.Get(proxyName); !ok {
		rdsErrorXML(w, "DBProxyNotFoundFault", fmt.Sprintf("DBProxy %q not found", proxyName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	for _, instID := range parseRDSIndexedStringList(r, "DBInstanceIdentifiers.member") {
		if inst, ok := rdsInstances.Get(instID); ok {
			rdsProxyTargets.Delete(proxyName + "/" + inst.DbiResourceId)
		}
	}
	for _, clID := range parseRDSIndexedStringList(r, "DBClusterIdentifiers.member") {
		rdsProxyTargets.Delete(proxyName + "/" + clID)
	}
	rdsXMLResponse(w, "DeregisterDBProxyTargets", "", sim.RequestID(r.Context()))
}

// IAM role associations on clusters / instances

func rdsAddRole(store sim.Store[rdsRoleSet], resID, roleArn, feature string) {
	rs, ok := store.Get(resID)
	if !ok {
		rs = rdsRoleSet{ResourceID: resID}
	}
	for _, existing := range rs.Roles {
		if existing.RoleArn == roleArn && existing.FeatureName == feature {
			store.Put(resID, rs)
			return
		}
	}
	rs.Roles = append(rs.Roles, rdsRole{RoleArn: roleArn, FeatureName: feature, Status: "ACTIVE"})
	store.Put(resID, rs)
}

func rdsRemoveRole(store sim.Store[rdsRoleSet], resID, roleArn, feature string) {
	rs, ok := store.Get(resID)
	if !ok {
		return
	}
	var kept []rdsRole
	for _, role := range rs.Roles {
		if role.RoleArn == roleArn && (feature == "" || role.FeatureName == feature) {
			continue
		}
		kept = append(kept, role)
	}
	rs.Roles = kept
	store.Put(resID, rs)
}

func handleRDSAddRoleToCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	roleArn := r.FormValue("RoleArn")
	if _, ok := rdsClusters.Get(id); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", fmt.Sprintf("DBCluster %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if roleArn == "" {
		rdsErrorXML(w, "MissingParameter", "RoleArn is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsAddRole(rdsClusterRoles, id, roleArn, r.FormValue("FeatureName"))
	rdsXMLResponse(w, "AddRoleToDBCluster", "", sim.RequestID(r.Context()))
}

func handleRDSRemoveRoleFromCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if _, ok := rdsClusters.Get(id); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", fmt.Sprintf("DBCluster %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsRemoveRole(rdsClusterRoles, id, r.FormValue("RoleArn"), r.FormValue("FeatureName"))
	rdsXMLResponse(w, "RemoveRoleFromDBCluster", "", sim.RequestID(r.Context()))
}

func handleRDSAddRoleToInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	roleArn := r.FormValue("RoleArn")
	if _, ok := rdsInstances.Get(id); !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if roleArn == "" {
		rdsErrorXML(w, "MissingParameter", "RoleArn is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsAddRole(rdsInstanceRoles, id, roleArn, r.FormValue("FeatureName"))
	rdsXMLResponse(w, "AddRoleToDBInstance", "", sim.RequestID(r.Context()))
}

func handleRDSRemoveRoleFromInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if _, ok := rdsInstances.Get(id); !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsRemoveRole(rdsInstanceRoles, id, r.FormValue("RoleArn"), r.FormValue("FeatureName"))
	rdsXMLResponse(w, "RemoveRoleFromDBInstance", "", sim.RequestID(r.Context()))
}

// DB security groups (EC2-Classic style)

func renderRDSDBSecurityGroup(g RDSDBSecurityGroup) string {
	var b strings.Builder
	b.WriteString("<DBSecurityGroup>")
	fmt.Fprintf(&b, "<DBSecurityGroupName>%s</DBSecurityGroupName>", xmlEscape(g.DBSecurityGroupName))
	fmt.Fprintf(&b, "<DBSecurityGroupDescription>%s</DBSecurityGroupDescription>", xmlEscape(g.DBSecurityGroupDescription))
	fmt.Fprintf(&b, "<OwnerId>%s</OwnerId>", xmlEscape(g.OwnerId))
	fmt.Fprintf(&b, "<DBSecurityGroupArn>%s</DBSecurityGroupArn>", xmlEscape(g.ARN))
	if g.VpcId != "" {
		fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(g.VpcId))
	}
	b.WriteString("<EC2SecurityGroups>")
	for _, e := range g.EC2SecurityGroups {
		b.WriteString("<EC2SecurityGroup>")
		fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(e.Status))
		fmt.Fprintf(&b, "<EC2SecurityGroupName>%s</EC2SecurityGroupName>", xmlEscape(e.EC2SecurityGroupName))
		fmt.Fprintf(&b, "<EC2SecurityGroupId>%s</EC2SecurityGroupId>", xmlEscape(e.EC2SecurityGroupId))
		fmt.Fprintf(&b, "<EC2SecurityGroupOwnerId>%s</EC2SecurityGroupOwnerId>", xmlEscape(e.EC2SecurityGroupOwnerId))
		b.WriteString("</EC2SecurityGroup>")
	}
	b.WriteString("</EC2SecurityGroups>")
	b.WriteString("<IPRanges>")
	for _, ip := range g.IPRanges {
		b.WriteString("<IPRange>")
		fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(ip.Status))
		fmt.Fprintf(&b, "<CIDRIP>%s</CIDRIP>", xmlEscape(ip.CIDRIP))
		b.WriteString("</IPRange>")
	}
	b.WriteString("</IPRanges>")
	b.WriteString("</DBSecurityGroup>")
	return b.String()
}

func handleRDSCreateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSecurityGroupName")
	desc := r.FormValue("DBSecurityGroupDescription")
	if name == "" || desc == "" {
		rdsErrorXML(w, "MissingParameter", "DBSecurityGroupName and DBSecurityGroupDescription are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsDBSecurityGroups.Get(name); ok {
		rdsErrorXML(w, "DBSecurityGroupAlreadyExists", "DB security group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSDBSecurityGroup{
		DBSecurityGroupName:        name,
		DBSecurityGroupDescription: desc,
		OwnerId:                    awsAccountID(),
		ARN:                        rdsSecurityGroupARN(name),
		Tags:                       parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsDBSecurityGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBSecurityGroup", renderRDSDBSecurityGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeSecurityGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBSecurityGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBSecurityGroups>")
	for _, g := range rdsDBSecurityGroups.List() {
		if wanted != "" && g.DBSecurityGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSDBSecurityGroup(g))
	}
	b.WriteString("</DBSecurityGroups>")
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBSecurityGroupNotFound", fmt.Sprintf("DBSecurityGroup %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeDBSecurityGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSecurityGroupName")
	if _, ok := rdsDBSecurityGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSecurityGroupNotFound", fmt.Sprintf("DBSecurityGroup %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsDBSecurityGroups.Delete(name)
	rdsXMLResponse(w, "DeleteDBSecurityGroup", "", sim.RequestID(r.Context()))
}

func handleRDSAuthorizeSecurityGroupIngress(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSecurityGroupName")
	if _, ok := rdsDBSecurityGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSecurityGroupNotFound", fmt.Sprintf("DBSecurityGroup %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	cidr := r.FormValue("CIDRIP")
	ec2Name := r.FormValue("EC2SecurityGroupName")
	ec2Id := r.FormValue("EC2SecurityGroupId")
	rdsDBSecurityGroups.Update(name, func(g *RDSDBSecurityGroup) {
		if cidr != "" {
			g.IPRanges = append(g.IPRanges, rdsIPRange{CIDRIP: cidr, Status: "authorized"})
		}
		if ec2Name != "" || ec2Id != "" {
			owner := r.FormValue("EC2SecurityGroupOwnerId")
			if owner == "" {
				owner = awsAccountID()
			}
			g.EC2SecurityGroups = append(g.EC2SecurityGroups, rdsEC2SecurityGroup{
				EC2SecurityGroupId:      ec2Id,
				EC2SecurityGroupName:    ec2Name,
				EC2SecurityGroupOwnerId: owner,
				Status:                  "authorized",
			})
		}
	})
	updated, _ := rdsDBSecurityGroups.Get(name)
	rdsXMLResponse(w, "AuthorizeDBSecurityGroupIngress", renderRDSDBSecurityGroup(updated), sim.RequestID(r.Context()))
}

func handleRDSRevokeSecurityGroupIngress(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSecurityGroupName")
	if _, ok := rdsDBSecurityGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSecurityGroupNotFound", fmt.Sprintf("DBSecurityGroup %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	cidr := r.FormValue("CIDRIP")
	ec2Name := r.FormValue("EC2SecurityGroupName")
	ec2Id := r.FormValue("EC2SecurityGroupId")
	rdsDBSecurityGroups.Update(name, func(g *RDSDBSecurityGroup) {
		if cidr != "" {
			var kept []rdsIPRange
			for _, ip := range g.IPRanges {
				if ip.CIDRIP != cidr {
					kept = append(kept, ip)
				}
			}
			g.IPRanges = kept
		}
		if ec2Name != "" || ec2Id != "" {
			var kept []rdsEC2SecurityGroup
			for _, e := range g.EC2SecurityGroups {
				if (ec2Name != "" && e.EC2SecurityGroupName == ec2Name) || (ec2Id != "" && e.EC2SecurityGroupId == ec2Id) {
					continue
				}
				kept = append(kept, e)
			}
			g.EC2SecurityGroups = kept
		}
	})
	updated, _ := rdsDBSecurityGroups.Get(name)
	rdsXMLResponse(w, "RevokeDBSecurityGroupIngress", renderRDSDBSecurityGroup(updated), sim.RequestID(r.Context()))
}

// Certificates

func rdsSeedCertificates() {
	if len(rdsCertificates.List()) > 0 {
		return
	}
	now := time.Now().UTC()
	for _, id := range []string{"rds-ca-rsa2048-g1", "rds-ca-rsa4096-g1", "rds-ca-ecc384-g1"} {
		c := RDSCertificate{
			CertificateIdentifier: id,
			CertificateType:       "CA",
			Thumbprint:            strings.ToUpper(strings.ReplaceAll(generateUUID(), "-", "")),
			ValidFrom:             now.AddDate(-1, 0, 0).Format(time.RFC3339),
			ValidTill:             now.AddDate(40, 0, 0).Format(time.RFC3339),
			CustomerOverride:      false,
			ARN:                   rdsCertificateARN(id),
		}
		rdsCertificates.Put(id, c)
	}
}

func renderRDSCertificate(c RDSCertificate) string {
	var b strings.Builder
	b.WriteString("<Certificate>")
	fmt.Fprintf(&b, "<CertificateIdentifier>%s</CertificateIdentifier>", xmlEscape(c.CertificateIdentifier))
	fmt.Fprintf(&b, "<CertificateType>%s</CertificateType>", xmlEscape(c.CertificateType))
	fmt.Fprintf(&b, "<Thumbprint>%s</Thumbprint>", xmlEscape(c.Thumbprint))
	fmt.Fprintf(&b, "<ValidFrom>%s</ValidFrom>", xmlEscape(c.ValidFrom))
	fmt.Fprintf(&b, "<ValidTill>%s</ValidTill>", xmlEscape(c.ValidTill))
	fmt.Fprintf(&b, "<CertificateArn>%s</CertificateArn>", xmlEscape(c.ARN))
	fmt.Fprintf(&b, "<CustomerOverride>%t</CustomerOverride>", c.CustomerOverride)
	b.WriteString("</Certificate>")
	return b.String()
}

func handleRDSDescribeCertificates(w http.ResponseWriter, r *http.Request) {
	rdsSeedCertificates()
	wanted := r.FormValue("CertificateIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<Certificates>")
	for _, c := range rdsCertificates.List() {
		if wanted != "" && c.CertificateIdentifier != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSCertificate(c))
	}
	b.WriteString("</Certificates>")
	if wanted != "" && !matched {
		rdsErrorXML(w, "CertificateNotFound", fmt.Sprintf("Certificate %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeCertificates", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyCertificates(w http.ResponseWriter, r *http.Request) {
	rdsSeedCertificates()
	id := r.FormValue("CertificateIdentifier")
	if r.FormValue("RemoveCustomerOverride") == "true" {
		for _, c := range rdsCertificates.List() {
			rdsCertificates.Update(c.CertificateIdentifier, func(cert *RDSCertificate) {
				cert.CustomerOverride = false
			})
		}
		if id != "" {
			if c, ok := rdsCertificates.Get(id); ok {
				rdsXMLResponse(w, "ModifyCertificates", renderRDSCertificate(c), sim.RequestID(r.Context()))
				return
			}
		}
		rdsXMLResponse(w, "ModifyCertificates", "", sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsCertificates.Get(id); !ok {
		rdsErrorXML(w, "CertificateNotFound", fmt.Sprintf("Certificate %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	for _, other := range rdsCertificates.List() {
		rdsCertificates.Update(other.CertificateIdentifier, func(cert *RDSCertificate) {
			cert.CustomerOverride = cert.CertificateIdentifier == id
		})
	}
	c, _ := rdsCertificates.Get(id)
	rdsXMLResponse(w, "ModifyCertificates", renderRDSCertificate(c), sim.RequestID(r.Context()))
}

// Automated backups

func rdsEnsureInstanceAutoBackup(inst RDSInstance) RDSInstanceAutomatedBackup {
	if b, ok := rdsInstanceAutomatedBackups.Get(inst.DbiResourceId); ok {
		return b
	}
	now := time.Now().UTC()
	b := RDSInstanceAutomatedBackup{
		DBInstanceAutomatedBackupsArn: rdsInstanceAutoBackupARN(inst.DbiResourceId),
		DBInstanceIdentifier:          inst.DBInstanceIdentifier,
		DbiResourceId:                 inst.DbiResourceId,
		Region:                        awsRegion(),
		Engine:                        inst.Engine,
		EngineVersion:                 inst.EngineVersion,
		Status:                        "active",
		AllocatedStorage:              inst.AllocatedStorage,
		MasterUsername:                inst.MasterUsername,
		Port:                          inst.Port,
		InstanceCreateTime:            inst.InstanceCreateTime,
		BackupRetentionPeriod:         7,
		EarliestTime:                  now.AddDate(0, 0, -7).Format(time.RFC3339),
		LatestTime:                    now.Format(time.RFC3339),
	}
	rdsInstanceAutomatedBackups.Put(inst.DbiResourceId, b)
	return b
}

// rdsAutoBackupCommonXML writes the fields shared by the instance and cluster
// automated-backup shapes (everything but the wrapper element + the id fields +
// the resource-specific create-time element name).
func rdsAutoBackupCommonXML(sb *strings.Builder, region, engine, engineVersion, status string, allocatedStorage int, masterUsername string, port, backupRetentionPeriod int, createTimeElem, createTime, earliestTime, latestTime string) {
	fmt.Fprintf(sb, "<Region>%s</Region>", xmlEscape(region))
	fmt.Fprintf(sb, "<Engine>%s</Engine>", xmlEscape(engine))
	fmt.Fprintf(sb, "<EngineVersion>%s</EngineVersion>", xmlEscape(engineVersion))
	fmt.Fprintf(sb, "<Status>%s</Status>", xmlEscape(status))
	fmt.Fprintf(sb, "<AllocatedStorage>%d</AllocatedStorage>", allocatedStorage)
	fmt.Fprintf(sb, "<MasterUsername>%s</MasterUsername>", xmlEscape(masterUsername))
	fmt.Fprintf(sb, "<Port>%d</Port>", port)
	fmt.Fprintf(sb, "<%s>%s</%s>", createTimeElem, xmlEscape(createTime), createTimeElem)
	fmt.Fprintf(sb, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", backupRetentionPeriod)
	fmt.Fprintf(sb, "<RestoreWindow><EarliestTime>%s</EarliestTime><LatestTime>%s</LatestTime></RestoreWindow>", xmlEscape(earliestTime), xmlEscape(latestTime))
}

func renderRDSInstanceAutoBackup(b RDSInstanceAutomatedBackup) string {
	var sb strings.Builder
	sb.WriteString("<DBInstanceAutomatedBackup>")
	fmt.Fprintf(&sb, "<DBInstanceAutomatedBackupsArn>%s</DBInstanceAutomatedBackupsArn>", xmlEscape(b.DBInstanceAutomatedBackupsArn))
	fmt.Fprintf(&sb, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(b.DBInstanceIdentifier))
	fmt.Fprintf(&sb, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(b.DbiResourceId))
	rdsAutoBackupCommonXML(&sb, b.Region, b.Engine, b.EngineVersion, b.Status, b.AllocatedStorage, b.MasterUsername, b.Port, b.BackupRetentionPeriod, "InstanceCreateTime", b.InstanceCreateTime, b.EarliestTime, b.LatestTime)
	sb.WriteString("</DBInstanceAutomatedBackup>")
	return sb.String()
}

func handleRDSDescribeInstanceAutomatedBackups(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("DBInstanceIdentifier")
	wantRes := r.FormValue("DbiResourceId")
	// Ensure every existing instance has a backup row.
	for _, inst := range rdsInstances.List() {
		rdsEnsureInstanceAutoBackup(inst)
	}
	var b strings.Builder
	b.WriteString("<DBInstanceAutomatedBackups>")
	for _, ab := range rdsInstanceAutomatedBackups.List() {
		if wantID != "" && ab.DBInstanceIdentifier != wantID {
			continue
		}
		if wantRes != "" && ab.DbiResourceId != wantRes {
			continue
		}
		b.WriteString(renderRDSInstanceAutoBackup(ab))
	}
	b.WriteString("</DBInstanceAutomatedBackups>")
	rdsXMLResponse(w, "DescribeDBInstanceAutomatedBackups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteInstanceAutomatedBackup(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("DBInstanceAutomatedBackupsArn")
	resID := r.FormValue("DbiResourceId")
	var found RDSInstanceAutomatedBackup
	var key string
	for _, ab := range rdsInstanceAutomatedBackups.List() {
		if (arn != "" && ab.DBInstanceAutomatedBackupsArn == arn) || (resID != "" && ab.DbiResourceId == resID) {
			found = ab
			key = ab.DbiResourceId
			break
		}
	}
	if key == "" {
		rdsErrorXML(w, "DBInstanceAutomatedBackupNotFound", "Automated backup not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	found.Status = "deleting"
	rdsInstanceAutomatedBackups.Delete(key)
	rdsXMLResponse(w, "DeleteDBInstanceAutomatedBackup", renderRDSInstanceAutoBackup(found), sim.RequestID(r.Context()))
}

func rdsEnsureClusterAutoBackup(cl RDSCluster) RDSClusterAutomatedBackup {
	if b, ok := rdsClusterAutomatedBackups.Get(cl.DbClusterResourceId); ok {
		return b
	}
	now := time.Now().UTC()
	b := RDSClusterAutomatedBackup{
		DBClusterAutomatedBackupsArn: rdsClusterAutoBackupARN(cl.DbClusterResourceId),
		DBClusterIdentifier:          cl.DBClusterIdentifier,
		DbClusterResourceId:          cl.DbClusterResourceId,
		Region:                       awsRegion(),
		Engine:                       cl.Engine,
		EngineVersion:                cl.EngineVersion,
		Status:                       "retained",
		AllocatedStorage:             cl.AllocatedStorage,
		MasterUsername:               cl.MasterUsername,
		Port:                         cl.Port,
		ClusterCreateTime:            cl.ClusterCreateTime,
		BackupRetentionPeriod:        cl.BackupRetentionPeriod,
		EarliestTime:                 now.AddDate(0, 0, -7).Format(time.RFC3339),
		LatestTime:                   now.Format(time.RFC3339),
	}
	rdsClusterAutomatedBackups.Put(cl.DbClusterResourceId, b)
	return b
}

func renderRDSClusterAutoBackup(b RDSClusterAutomatedBackup) string {
	var sb strings.Builder
	sb.WriteString("<DBClusterAutomatedBackup>")
	fmt.Fprintf(&sb, "<DBClusterAutomatedBackupsArn>%s</DBClusterAutomatedBackupsArn>", xmlEscape(b.DBClusterAutomatedBackupsArn))
	fmt.Fprintf(&sb, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(b.DBClusterIdentifier))
	fmt.Fprintf(&sb, "<DbClusterResourceId>%s</DbClusterResourceId>", xmlEscape(b.DbClusterResourceId))
	rdsAutoBackupCommonXML(&sb, b.Region, b.Engine, b.EngineVersion, b.Status, b.AllocatedStorage, b.MasterUsername, b.Port, b.BackupRetentionPeriod, "ClusterCreateTime", b.ClusterCreateTime, b.EarliestTime, b.LatestTime)
	sb.WriteString("</DBClusterAutomatedBackup>")
	return sb.String()
}

func handleRDSDescribeClusterAutomatedBackups(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("DBClusterIdentifier")
	wantRes := r.FormValue("DbClusterResourceId")
	for _, cl := range rdsClusters.List() {
		rdsEnsureClusterAutoBackup(cl)
	}
	var b strings.Builder
	b.WriteString("<DBClusterAutomatedBackups>")
	for _, ab := range rdsClusterAutomatedBackups.List() {
		if wantID != "" && ab.DBClusterIdentifier != wantID {
			continue
		}
		if wantRes != "" && ab.DbClusterResourceId != wantRes {
			continue
		}
		b.WriteString(renderRDSClusterAutoBackup(ab))
	}
	b.WriteString("</DBClusterAutomatedBackups>")
	rdsXMLResponse(w, "DescribeDBClusterAutomatedBackups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterAutomatedBackup(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("DBClusterAutomatedBackupsArn")
	resID := r.FormValue("DbClusterResourceId")
	var found RDSClusterAutomatedBackup
	var key string
	for _, ab := range rdsClusterAutomatedBackups.List() {
		if (arn != "" && ab.DBClusterAutomatedBackupsArn == arn) || (resID != "" && ab.DbClusterResourceId == resID) {
			found = ab
			key = ab.DbClusterResourceId
			break
		}
	}
	if key == "" {
		rdsErrorXML(w, "DBClusterAutomatedBackupNotFoundFault", "Automated backup not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	found.Status = "deleting"
	rdsClusterAutomatedBackups.Delete(key)
	rdsXMLResponse(w, "DeleteDBClusterAutomatedBackup", renderRDSClusterAutoBackup(found), sim.RequestID(r.Context()))
}

// Log files

func rdsLogFileData(inst RDSInstance) string {
	return fmt.Sprintf(
		"%s UTC [1]: [1-1] LOG:  database system is ready to accept connections\n"+
			"%s UTC [1]: [2-1] LOG:  instance %q (%s) started\n",
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		inst.DBInstanceIdentifier, inst.Engine)
}

func handleRDSDescribeLogFiles(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	data := rdsLogFileData(inst)
	var b strings.Builder
	b.WriteString("<DescribeDBLogFiles>")
	for _, fn := range []string{"error/postgresql.log.1", "error/postgresql.log"} {
		b.WriteString("<DescribeDBLogFilesDetails>")
		fmt.Fprintf(&b, "<LogFileName>%s</LogFileName>", xmlEscape(fn))
		fmt.Fprintf(&b, "<LastWritten>%d</LastWritten>", time.Now().UnixMilli())
		fmt.Fprintf(&b, "<Size>%d</Size>", len(data))
		b.WriteString("</DescribeDBLogFilesDetails>")
	}
	b.WriteString("</DescribeDBLogFiles>")
	rdsXMLResponse(w, "DescribeDBLogFiles", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDownloadLogFilePortion(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if r.FormValue("LogFileName") == "" {
		rdsErrorXML(w, "MissingParameter", "LogFileName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	data := rdsLogFileData(inst)
	var b strings.Builder
	fmt.Fprintf(&b, "<LogFileData>%s</LogFileData>", xmlEscape(data))
	b.WriteString("<Marker>0:0</Marker>")
	b.WriteString("<AdditionalDataPending>false</AdditionalDataPending>")
	rdsXMLResponse(w, "DownloadDBLogFilePortion", b.String(), sim.RequestID(r.Context()))
}

// Parameter / option group copies

// rdsCopyGroup is the shared copy-a-named-group flow (cluster parameter group /
// parameter group / option group): validate the source/target/description,
// 404 if the source is absent, reject a duplicate target, then build + store +
// render the copy. The per-type build differs, so it is supplied by the caller.
func rdsCopyGroup[T any](w http.ResponseWriter, r *http.Request, srcF, targetF, descF, notFoundCode, notFoundMsg, existsCode, opName string, store sim.Store[T], build func(src T, target string) T, render func(T) string) {
	src := r.FormValue(srcF)
	target := r.FormValue(targetF)
	desc := r.FormValue(descF)
	if src == "" || target == "" || desc == "" {
		rdsErrorXML(w, "MissingParameter", "Source/Target identifiers and description are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	srcGroup, ok := store.Get(src)
	if !ok {
		rdsErrorXML(w, notFoundCode, fmt.Sprintf(notFoundMsg, src), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, ok := store.Get(target); ok {
		rdsErrorXML(w, existsCode, "Target group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := build(srcGroup, target)
	store.Put(target, g)
	rdsXMLResponse(w, opName, render(g), sim.RequestID(r.Context()))
}

func handleRDSCopyClusterParameterGroup(w http.ResponseWriter, r *http.Request) {
	rdsCopyGroup(w, r, "SourceDBClusterParameterGroupIdentifier", "TargetDBClusterParameterGroupIdentifier", "TargetDBClusterParameterGroupDescription",
		"DBParameterGroupNotFound", "DBClusterParameterGroup %q not found", "DBParameterGroupAlreadyExists", "CopyDBClusterParameterGroup",
		rdsClusterParamGroups, func(src RDSClusterParamGroup, target string) RDSClusterParamGroup {
			params := map[string]string{}
			for k, v := range src.Parameters {
				params[k] = v
			}
			return RDSClusterParamGroup{
				DBClusterParameterGroupName: target,
				DBParameterGroupFamily:      src.DBParameterGroupFamily,
				Description:                 r.FormValue("TargetDBClusterParameterGroupDescription"),
				Parameters:                  params,
				ARN:                         rdsClusterParamGroupARN(target),
				Tags:                        parseAWSQueryTagMap(r, "Tags.Tag"),
			}
		}, renderRDSClusterParamGroup)
}

func handleRDSCopyParameterGroup(w http.ResponseWriter, r *http.Request) {
	rdsCopyGroup(w, r, "SourceDBParameterGroupIdentifier", "TargetDBParameterGroupIdentifier", "TargetDBParameterGroupDescription",
		"DBParameterGroupNotFound", "DBParameterGroup %q not found", "DBParameterGroupAlreadyExists", "CopyDBParameterGroup",
		rdsParamGroups, func(src RDSParamGroup, target string) RDSParamGroup {
			params := map[string]string{}
			for k, v := range src.Parameters {
				params[k] = v
			}
			return RDSParamGroup{
				DBParameterGroupName:   target,
				DBParameterGroupFamily: src.DBParameterGroupFamily,
				Description:            r.FormValue("TargetDBParameterGroupDescription"),
				Parameters:             params,
				ARN:                    rdsParamGroupARN(target),
				Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
			}
		}, renderRDSParamGroup)
}

func handleRDSCopyOptionGroup(w http.ResponseWriter, r *http.Request) {
	rdsCopyGroup(w, r, "SourceOptionGroupIdentifier", "TargetOptionGroupIdentifier", "TargetOptionGroupDescription",
		"OptionGroupNotFoundFault", "OptionGroup %q not found", "OptionGroupAlreadyExistsFault", "CopyOptionGroup",
		rdsOptionGroups, func(src RDSOptionGroup, target string) RDSOptionGroup {
			return RDSOptionGroup{
				OptionGroupName:                       target,
				OptionGroupDescription:                r.FormValue("TargetOptionGroupDescription"),
				EngineName:                            src.EngineName,
				MajorEngineVersion:                    src.MajorEngineVersion,
				AllowsVpcAndNonVpcInstanceMemberships: src.AllowsVpcAndNonVpcInstanceMemberships,
				ARN:                                   rdsOptionGroupARN(target),
				Tags:                                  parseAWSQueryTagMap(r, "Tags.Tag"),
			}
		}, renderRDSOptionGroup)
}

// Event subscription source identifiers

func handleRDSAddSourceIdentifier(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	sourceID := r.FormValue("SourceIdentifier")
	if name == "" || sourceID == "" {
		rdsErrorXML(w, "MissingParameter", "SubscriptionName and SourceIdentifier are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsEventSubscriptions.Get(name); !ok {
		rdsErrorXML(w, "SubscriptionNotFound", fmt.Sprintf("Event subscription %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsEventSubscriptions.Update(name, func(s *RDSEventSubscription) {
		for _, existing := range s.SourceIds {
			if existing == sourceID {
				return
			}
		}
		s.SourceIds = append(s.SourceIds, sourceID)
	})
	updated, _ := rdsEventSubscriptions.Get(name)
	rdsXMLResponse(w, "AddSourceIdentifierToSubscription", renderRDSEventSubscription(updated), sim.RequestID(r.Context()))
}

func handleRDSRemoveSourceIdentifier(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	sourceID := r.FormValue("SourceIdentifier")
	if name == "" || sourceID == "" {
		rdsErrorXML(w, "MissingParameter", "SubscriptionName and SourceIdentifier are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsEventSubscriptions.Get(name); !ok {
		rdsErrorXML(w, "SubscriptionNotFound", fmt.Sprintf("Event subscription %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsEventSubscriptions.Update(name, func(s *RDSEventSubscription) {
		var kept []string
		for _, existing := range s.SourceIds {
			if existing != sourceID {
				kept = append(kept, existing)
			}
		}
		s.SourceIds = kept
	})
	updated, _ := rdsEventSubscriptions.Get(name)
	rdsXMLResponse(w, "RemoveSourceIdentifierFromSubscription", renderRDSEventSubscription(updated), sim.RequestID(r.Context()))
}

// Pending maintenance actions

func rdsEnsureMaintenanceActions(resourceID string) rdsMaintenanceActions {
	if m, ok := rdsPendingMaintenance.Get(resourceID); ok {
		return m
	}
	now := time.Now().UTC()
	m := rdsMaintenanceActions{
		ResourceIdentifier: resourceID,
		Actions: []rdsPendingAction{{
			Action:               "system-update",
			OptInStatus:          "",
			AutoAppliedAfterDate: now.AddDate(0, 0, 14).Format(time.RFC3339),
			CurrentApplyDate:     now.AddDate(0, 0, 14).Format(time.RFC3339),
			ForcedApplyDate:      now.AddDate(0, 0, 30).Format(time.RFC3339),
			Description:          "New system software update is available",
		}},
	}
	rdsPendingMaintenance.Put(resourceID, m)
	return m
}

func renderRDSMaintenanceActions(m rdsMaintenanceActions) string {
	var b strings.Builder
	b.WriteString("<ResourcePendingMaintenanceActions>")
	fmt.Fprintf(&b, "<ResourceIdentifier>%s</ResourceIdentifier>", xmlEscape(m.ResourceIdentifier))
	b.WriteString("<PendingMaintenanceActionDetails>")
	for _, a := range m.Actions {
		b.WriteString("<PendingMaintenanceAction>")
		fmt.Fprintf(&b, "<Action>%s</Action>", xmlEscape(a.Action))
		if a.OptInStatus != "" {
			fmt.Fprintf(&b, "<OptInStatus>%s</OptInStatus>", xmlEscape(a.OptInStatus))
		}
		fmt.Fprintf(&b, "<AutoAppliedAfterDate>%s</AutoAppliedAfterDate>", xmlEscape(a.AutoAppliedAfterDate))
		fmt.Fprintf(&b, "<CurrentApplyDate>%s</CurrentApplyDate>", xmlEscape(a.CurrentApplyDate))
		fmt.Fprintf(&b, "<ForcedApplyDate>%s</ForcedApplyDate>", xmlEscape(a.ForcedApplyDate))
		fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(a.Description))
		b.WriteString("</PendingMaintenanceAction>")
	}
	b.WriteString("</PendingMaintenanceActionDetails>")
	b.WriteString("</ResourcePendingMaintenanceActions>")
	return b.String()
}

// rdsResolveMaintenanceResource maps a resource ARN to the canonical
// resource ID the sim keys maintenance actions on, returning false if no
// such RDS resource exists.
func rdsResolveMaintenanceResource(arn string) (string, bool) {
	for _, inst := range rdsInstances.List() {
		if inst.ARN == arn || inst.DBInstanceIdentifier == arn {
			return inst.ARN, true
		}
	}
	for _, cl := range rdsClusters.List() {
		if cl.ARN == arn || cl.DBClusterIdentifier == arn {
			return cl.ARN, true
		}
	}
	return "", false
}

func handleRDSApplyPendingMaintenanceAction(w http.ResponseWriter, r *http.Request) {
	resArn := r.FormValue("ResourceIdentifier")
	action := r.FormValue("ApplyAction")
	optIn := r.FormValue("OptInType")
	resID, ok := rdsResolveMaintenanceResource(resArn)
	if !ok {
		rdsErrorXML(w, "ResourceNotFoundFault", fmt.Sprintf("Resource %q not found", resArn), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsEnsureMaintenanceActions(resID)
	rdsPendingMaintenance.Update(resID, func(m *rdsMaintenanceActions) {
		for i := range m.Actions {
			if m.Actions[i].Action == action {
				m.Actions[i].OptInStatus = optIn
			}
		}
	})
	updated, _ := rdsPendingMaintenance.Get(resID)
	rdsXMLResponse(w, "ApplyPendingMaintenanceAction", renderRDSMaintenanceActions(updated), sim.RequestID(r.Context()))
}

func handleRDSDescribePendingMaintenanceActions(w http.ResponseWriter, r *http.Request) {
	resArn := r.FormValue("ResourceIdentifier")
	// Seed a pending action for every known instance/cluster.
	for _, inst := range rdsInstances.List() {
		rdsEnsureMaintenanceActions(inst.ARN)
	}
	for _, cl := range rdsClusters.List() {
		rdsEnsureMaintenanceActions(cl.ARN)
	}
	var b strings.Builder
	b.WriteString("<PendingMaintenanceActions>")
	for _, m := range rdsPendingMaintenance.List() {
		if resArn != "" && m.ResourceIdentifier != resArn {
			continue
		}
		b.WriteString(renderRDSMaintenanceActions(m))
	}
	b.WriteString("</PendingMaintenanceActions>")
	rdsXMLResponse(w, "DescribePendingMaintenanceActions", b.String(), sim.RequestID(r.Context()))
}
