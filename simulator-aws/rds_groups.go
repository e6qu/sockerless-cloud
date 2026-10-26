package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func rdsSubnetGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:subgrp:%s", awsRegion(), awsAccountID(), name)
}

func findRDSSubnetGroupByARN(arn string) (RDSSubnetGroup, bool) {
	for _, g := range rdsSubnetGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsSubnetGroups.Get(arn); ok {
		return g, true
	}
	return RDSSubnetGroup{}, false
}

func parseRDSSubnetIDs(r *http.Request) []string {
	var ids []string
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("SubnetIds.SubnetIdentifier.%d", n))
		if v == "" {
			break
		}
		ids = append(ids, v)
	}
	return ids
}

func renderRDSSubnetGroup(g RDSSubnetGroup) string {
	var b strings.Builder
	b.WriteString("<DBSubnetGroup>")
	fmt.Fprintf(&b, "<DBSubnetGroupName>%s</DBSubnetGroupName>", xmlEscape(g.DBSubnetGroupName))
	fmt.Fprintf(&b, "<DBSubnetGroupDescription>%s</DBSubnetGroupDescription>", xmlEscape(g.DBSubnetGroupDescription))
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(g.VpcId))
	fmt.Fprintf(&b, "<SubnetGroupStatus>%s</SubnetGroupStatus>", xmlEscape(g.SubnetGroupStatus))
	fmt.Fprintf(&b, "<DBSubnetGroupArn>%s</DBSubnetGroupArn>", xmlEscape(g.ARN))
	b.WriteString("<Subnets>")
	for i, sid := range g.SubnetIds {
		b.WriteString("<Subnet>")
		fmt.Fprintf(&b, "<SubnetIdentifier>%s</SubnetIdentifier>", xmlEscape(sid))
		if i < len(g.SubnetZones) {
			fmt.Fprintf(&b, "<SubnetAvailabilityZone><Name>%s</Name></SubnetAvailabilityZone>", xmlEscape(g.SubnetZones[i]))
		}
		b.WriteString("<SubnetStatus>Active</SubnetStatus>")
		b.WriteString("</Subnet>")
	}
	b.WriteString("</Subnets>")
	b.WriteString("</DBSubnetGroup>")
	return b.String()
}

// rdsSubnetGroupSubnets resolves the Amazon EC2 subnets a DB subnet group
// names: they must exist, share one VPC and cover two or more Availability
// Zones.
func rdsSubnetGroupSubnets(ids []string) (vpcID string, zones []string, code, message string) {
	if len(ids) == 0 {
		return "", nil, "InvalidParameterValue", "The parameter SubnetIds must be provided and must not be blank."
	}
	for _, id := range ids {
		subnet, ok := ec2Subnets.Get(id)
		if !ok {
			return "", nil, "InvalidSubnet", fmt.Sprintf("The subnet ID '%s' does not exist", id)
		}
		if vpcID != "" && subnet.VpcId != vpcID {
			return "", nil, "InvalidSubnet", "The requested subnet is invalid, or multiple subnets were requested that are not all in a common VPC."
		}
		vpcID = subnet.VpcId
		zones = append(zones, subnet.AvailabilityZone)
	}
	if len(RDSSubnetGroup{SubnetZones: zones}.Zones()) < 2 {
		return "", nil, "DBSubnetGroupDoesNotCoverEnoughAZs", "Subnets in the DB subnet group should cover at least two Availability Zones unless there is only one Availability Zone."
	}
	return vpcID, zones, "", ""
}

func handleRDSCreateSubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSubnetGroupName")
	if name == "" {
		rdsErrorXML(w, "MissingParameter", "DBSubnetGroupName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsSubnetGroups.Get(name); ok {
		rdsErrorXML(w, "DBSubnetGroupAlreadyExists", "DB subnet group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	ids := parseRDSSubnetIDs(r)
	vpcID, zones, code, message := rdsSubnetGroupSubnets(ids)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSSubnetGroup{
		DBSubnetGroupName:        name,
		DBSubnetGroupDescription: r.FormValue("DBSubnetGroupDescription"),
		VpcId:                    vpcID,
		SubnetGroupStatus:        "Complete",
		SubnetIds:                ids,
		SubnetZones:              zones,
		ARN:                      rdsSubnetGroupARN(name),
		Tags:                     parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsSubnetGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBSubnetGroup", renderRDSSubnetGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeSubnetGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBSubnetGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBSubnetGroups>")
	for _, g := range rdsSubnetGroups.List() {
		if wanted != "" && g.DBSubnetGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSSubnetGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBSubnetGroupNotFoundFault",
			fmt.Sprintf("DBSubnetGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBSubnetGroups>")
	rdsXMLResponse(w, "DescribeDBSubnetGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifySubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSubnetGroupName")
	if _, ok := rdsSubnetGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSubnetGroupNotFoundFault", "DB subnet group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ids := parseRDSSubnetIDs(r)
	vpcID, zones, code, message := rdsSubnetGroupSubnets(ids)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsSubnetGroups.Update(name, func(g *RDSSubnetGroup) {
		if v := r.FormValue("DBSubnetGroupDescription"); v != "" {
			g.DBSubnetGroupDescription = v
		}
		g.VpcId, g.SubnetIds, g.SubnetZones = vpcID, ids, zones
	})
	updated, _ := rdsSubnetGroups.Get(name)
	rdsXMLResponse(w, "ModifyDBSubnetGroup", renderRDSSubnetGroup(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteSubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSubnetGroupName")
	if _, ok := rdsSubnetGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSubnetGroupNotFoundFault", "DB subnet group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if users := rdsInstancesUsing(func(i RDSInstance) bool {
		return i.DBSubnetGroupName == name || (i.PendingModifications != nil && i.PendingModifications.DBSubnetGroupName == name)
	}); len(users) > 0 {
		rdsErrorXML(w, "InvalidDBSubnetGroupStateFault",
			fmt.Sprintf("Cannot delete the subnet group '%s' because at least one database instance: %s is still using it.", name, users[0]),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsSubnetGroups.Delete(name)
	// DeleteDBSubnetGroup has an empty result body.
	rdsXMLResponse(w, "DeleteDBSubnetGroup", "", sim.RequestID(r.Context()))
}

func rdsParamGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:pg:%s", awsRegion(), awsAccountID(), name)
}

func findRDSParamGroupByARN(arn string) (RDSParamGroup, bool) {
	for _, g := range rdsParamGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsParamGroups.Get(arn); ok {
		return g, true
	}
	return RDSParamGroup{}, false
}

func renderRDSParamGroup(g RDSParamGroup) string {
	var b strings.Builder
	b.WriteString("<DBParameterGroup>")
	fmt.Fprintf(&b, "<DBParameterGroupName>%s</DBParameterGroupName>", xmlEscape(g.DBParameterGroupName))
	fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(g.DBParameterGroupFamily))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<DBParameterGroupArn>%s</DBParameterGroupArn>", xmlEscape(g.ARN))
	b.WriteString("</DBParameterGroup>")
	return b.String()
}

func handleRDSCreateParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	family := r.FormValue("DBParameterGroupFamily")
	if name == "" || family == "" {
		rdsErrorXML(w, "MissingParameter", "DBParameterGroupName and DBParameterGroupFamily are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsParamGroups.Get(name); ok {
		rdsErrorXML(w, "DBParameterGroupAlreadyExists", "DB parameter group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSParamGroup{
		DBParameterGroupName:   name,
		DBParameterGroupFamily: family,
		Description:            r.FormValue("Description"),
		ARN:                    rdsParamGroupARN(name),
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsParamGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBParameterGroup", renderRDSParamGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeParamGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBParameterGroupName")
	rdsEnsureOfferedDefaultParameterGroup(wanted)
	matched := false
	var b strings.Builder
	b.WriteString("<DBParameterGroups>")
	for _, g := range rdsParamGroups.List() {
		if wanted != "" && g.DBParameterGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSParamGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			fmt.Sprintf("DBParameterGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBParameterGroups>")
	rdsXMLResponse(w, "DescribeDBParameterGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	if strings.HasPrefix(name, "default.") {
		rdsErrorXML(w, "InvalidParameterValue", "Default DBParameterGroup cannot be deleted: "+name, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsParamGroups.Delete(name)
	// DeleteDBParameterGroup has an empty result body.
	rdsXMLResponse(w, "DeleteDBParameterGroup", "", sim.RequestID(r.Context()))
}

func rdsClusterParamGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-pg:%s", awsRegion(), awsAccountID(), name)
}

func findRDSClusterParamGroupByARN(arn string) (RDSClusterParamGroup, bool) {
	for _, g := range rdsClusterParamGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsClusterParamGroups.Get(arn); ok {
		return g, true
	}
	return RDSClusterParamGroup{}, false
}

func renderRDSClusterParamGroup(g RDSClusterParamGroup) string {
	var b strings.Builder
	b.WriteString("<DBClusterParameterGroup>")
	fmt.Fprintf(&b, "<DBClusterParameterGroupName>%s</DBClusterParameterGroupName>", xmlEscape(g.DBClusterParameterGroupName))
	fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(g.DBParameterGroupFamily))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<DBClusterParameterGroupArn>%s</DBClusterParameterGroupArn>", xmlEscape(g.ARN))
	b.WriteString("</DBClusterParameterGroup>")
	return b.String()
}

func handleRDSCreateClusterParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	family := r.FormValue("DBParameterGroupFamily")
	if name == "" || family == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterParameterGroupName and DBParameterGroupFamily are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusterParamGroups.Get(name); ok {
		rdsErrorXML(w, "DBParameterGroupAlreadyExists",
			"DB cluster parameter group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSClusterParamGroup{
		DBClusterParameterGroupName: name,
		DBParameterGroupFamily:      family,
		Description:                 r.FormValue("Description"),
		ARN:                         rdsClusterParamGroupARN(name),
		Tags:                        parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsClusterParamGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBClusterParameterGroup", renderRDSClusterParamGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterParamGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBClusterParameterGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusterParameterGroups>")
	for _, g := range rdsClusterParamGroups.List() {
		if wanted != "" && g.DBClusterParameterGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSClusterParamGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			fmt.Sprintf("DBClusterParameterGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusterParameterGroups>")
	rdsXMLResponse(w, "DescribeDBClusterParameterGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	if _, ok := rdsClusterParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			"DB cluster parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsClusterParamGroups.Delete(name)
	rdsXMLResponse(w, "DeleteDBClusterParameterGroup", "", sim.RequestID(r.Context()))
}

func rdsOptionGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:og:%s", awsRegion(), awsAccountID(), name)
}

func findRDSOptionGroupByARN(arn string) (RDSOptionGroup, bool) {
	for _, g := range rdsOptionGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsOptionGroups.Get(arn); ok {
		return g, true
	}
	return RDSOptionGroup{}, false
}

func renderRDSOptionGroup(g RDSOptionGroup) string {
	var b strings.Builder
	b.WriteString("<OptionGroup>")
	fmt.Fprintf(&b, "<OptionGroupName>%s</OptionGroupName>", xmlEscape(g.OptionGroupName))
	fmt.Fprintf(&b, "<OptionGroupDescription>%s</OptionGroupDescription>", xmlEscape(g.OptionGroupDescription))
	fmt.Fprintf(&b, "<EngineName>%s</EngineName>", xmlEscape(g.EngineName))
	fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(g.MajorEngineVersion))
	fmt.Fprintf(&b, "<AllowsVpcAndNonVpcInstanceMemberships>%t</AllowsVpcAndNonVpcInstanceMemberships>", g.AllowsVpcAndNonVpcInstanceMemberships)
	fmt.Fprintf(&b, "<OptionGroupArn>%s</OptionGroupArn>", xmlEscape(g.ARN))
	b.WriteString("<Options></Options>")
	b.WriteString("</OptionGroup>")
	return b.String()
}

func handleRDSCreateOptionGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("OptionGroupName")
	engine := r.FormValue("EngineName")
	majorVersion := r.FormValue("MajorEngineVersion")
	desc := r.FormValue("OptionGroupDescription")
	if name == "" || engine == "" || majorVersion == "" || desc == "" {
		rdsErrorXML(w, "MissingParameter",
			"OptionGroupName, EngineName, MajorEngineVersion and OptionGroupDescription are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsOptionGroups.Get(name); ok {
		rdsErrorXML(w, "OptionGroupAlreadyExistsFault",
			"Option group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSOptionGroup{
		OptionGroupName:                       name,
		OptionGroupDescription:                desc,
		EngineName:                            engine,
		MajorEngineVersion:                    majorVersion,
		AllowsVpcAndNonVpcInstanceMemberships: true,
		ARN:                                   rdsOptionGroupARN(name),
		Tags:                                  parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsOptionGroups.Put(name, g)
	rdsXMLResponse(w, "CreateOptionGroup", renderRDSOptionGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeOptionGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("OptionGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<OptionGroupsList>")
	for _, g := range rdsOptionGroups.List() {
		if wanted != "" && g.OptionGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSOptionGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "OptionGroupNotFoundFault",
			fmt.Sprintf("OptionGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</OptionGroupsList>")
	rdsXMLResponse(w, "DescribeOptionGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteOptionGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("OptionGroupName")
	if _, ok := rdsOptionGroups.Get(name); !ok {
		rdsErrorXML(w, "OptionGroupNotFoundFault",
			"Option group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if rdsIsDefaultGroupName(name) {
		rdsErrorXML(w, "InvalidOptionGroupStateFault",
			fmt.Sprintf("Default option group %s cannot be deleted.", name), http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if users := rdsInstancesUsing(func(i RDSInstance) bool {
		return i.OptionGroupName == name || (i.PendingModifications != nil && i.PendingModifications.OptionGroupName == name)
	}); len(users) > 0 {
		rdsErrorXML(w, "InvalidOptionGroupStateFault",
			fmt.Sprintf("The option group '%s' cannot be deleted because it is in use by DB instance %s.", name, users[0]),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsOptionGroups.Delete(name)
	rdsXMLResponse(w, "DeleteOptionGroup", "", sim.RequestID(r.Context()))
}

func handleRDSDescribeOrderableOptions(w http.ResponseWriter, r *http.Request) {
	engine := r.FormValue("Engine")
	if engine == "" {
		rdsErrorXML(w, "MissingParameter",
			"Engine is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	versions := rdsOrderableVersions(engine, r.FormValue("EngineVersion"))
	wantClass := r.FormValue("DBInstanceClass")
	classes := []string{"db.t3.micro", "db.t3.small", "db.t3.medium", "db.m5.large", "db.m5.xlarge", "db.r5.large"}
	azs := rdsRegionAvailabilityZones()
	var b strings.Builder
	b.WriteString("<OrderableDBInstanceOptions>")
	for _, version := range versions {
		for _, class := range classes {
			if wantClass != "" && class != wantClass {
				continue
			}
			b.WriteString(rdsRenderOrderableOption(engine, version, class, azs))
		}
	}
	b.WriteString("</OrderableDBInstanceOptions>")
	rdsXMLResponse(w, "DescribeOrderableDBInstanceOptions", b.String(), sim.RequestID(r.Context()))
}

func rdsRenderOrderableOption(engine, version, class string, azs []string) string {
	var b strings.Builder
	b.WriteString("<OrderableDBInstanceOption>")
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(version))
	fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(class))
	b.WriteString("<LicenseModel>general-public-license</LicenseModel>")
	b.WriteString("<MultiAZCapable>true</MultiAZCapable>")
	b.WriteString("<ReadReplicaCapable>true</ReadReplicaCapable>")
	b.WriteString("<Vpc>true</Vpc>")
	b.WriteString("<SupportsStorageEncryption>true</SupportsStorageEncryption>")
	b.WriteString("<SupportsIAMDatabaseAuthentication>true</SupportsIAMDatabaseAuthentication>")
	b.WriteString("<StorageType>gp3</StorageType>")
	b.WriteString("<AvailabilityZones>")
	for _, az := range azs {
		b.WriteString("<AvailabilityZone>")
		fmt.Fprintf(&b, "<Name>%s</Name>", xmlEscape(az))
		b.WriteString("</AvailabilityZone>")
	}
	b.WriteString("</AvailabilityZones>")
	b.WriteString("</OrderableDBInstanceOption>")
	return b.String()
}
