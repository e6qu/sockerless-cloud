package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ELBv2TrustStore is a mutual-TLS trust store: a named resource holding a CA
// certificate bundle (an Amazon S3 reference) plus a set of numbered revocation
// lists. Status is ACTIVE once the bundle is ingested.
type ELBv2TrustStore struct {
	Arn                                 string
	Name                                string
	Status                              string
	NumberOfCaCertificates              int
	CaCertificatesBundleS3Bucket        string
	CaCertificatesBundleS3Key           string
	CaCertificatesBundleS3ObjectVersion string
	Revocations                         []ELBv2TrustStoreRevocation
	NextRevocationID                    int64
	Tags                                map[string]string
}

// ELBv2TrustStoreRevocation is one numbered revocation list added to a trust
// store via AddTrustStoreRevocations.
type ELBv2TrustStoreRevocation struct {
	RevocationID           int64
	RevocationType         string
	NumberOfRevokedEntries int
	S3Bucket               string
	S3Key                  string
	S3ObjectVersion        string
}

// ELBv2TrustStoreAssociation records a resource (a listener ARN) that uses the
// trust store for mutual TLS verification.
type ELBv2TrustStoreAssociation struct {
	TrustStoreArn string
	ResourceArn   string
}

var (
	elbv2TrustStores            sim.Store[ELBv2TrustStore]
	elbv2TrustStoreAssociations sim.Store[ELBv2TrustStoreAssociation]
)

func registerELBv2TrustStores(r *AWSQueryRouter, srv *sim.Server) {
	elbv2TrustStores = sim.MakeStore[ELBv2TrustStore](srv.DB(), "elbv2_trust_stores")
	elbv2TrustStoreAssociations = sim.MakeStore[ELBv2TrustStoreAssociation](srv.DB(), "elbv2_trust_store_associations")

	r.RegisterVersioned(elbv2APIVersion, "CreateTrustStore", handleELBv2CreateTrustStore)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTrustStores", handleELBv2DescribeTrustStores)
	r.RegisterVersioned(elbv2APIVersion, "ModifyTrustStore", handleELBv2ModifyTrustStore)
	r.RegisterVersioned(elbv2APIVersion, "DeleteTrustStore", handleELBv2DeleteTrustStore)
	r.RegisterVersioned(elbv2APIVersion, "GetTrustStoreCaCertificatesBundle", handleELBv2GetTrustStoreCaCertificatesBundle)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTrustStoreAssociations", handleELBv2DescribeTrustStoreAssociations)
	r.RegisterVersioned(elbv2APIVersion, "DeleteSharedTrustStoreAssociation", handleELBv2DeleteSharedTrustStoreAssociation)
	r.RegisterVersioned(elbv2APIVersion, "AddTrustStoreRevocations", handleELBv2AddTrustStoreRevocations)
	r.RegisterVersioned(elbv2APIVersion, "RemoveTrustStoreRevocations", handleELBv2RemoveTrustStoreRevocations)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTrustStoreRevocations", handleELBv2DescribeTrustStoreRevocations)
	r.RegisterVersioned(elbv2APIVersion, "GetTrustStoreRevocationContent", handleELBv2GetTrustStoreRevocationContent)

	r.RegisterVersioned(elbv2APIVersion, "DescribeSSLPolicies", handleELBv2DescribeSSLPolicies)
	r.RegisterVersioned(elbv2APIVersion, "GetResourcePolicy", handleELBv2GetResourcePolicy)
	r.RegisterVersioned(elbv2APIVersion, "ModifyCapacityReservation", handleELBv2ModifyCapacityReservation)
	r.RegisterVersioned(elbv2APIVersion, "ModifyIpPools", handleELBv2ModifyIpPools)
}

func handleELBv2CreateTrustStore(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		elbv2ErrorXML(w, "ValidationError", "Name is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	bucket := r.FormValue("CaCertificatesBundleS3Bucket")
	key := r.FormValue("CaCertificatesBundleS3Key")
	if bucket == "" || key == "" {
		elbv2ErrorXML(w, "ValidationError", "CaCertificatesBundleS3Bucket and CaCertificatesBundleS3Key are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	for _, existing := range elbv2TrustStores.List() {
		if existing.Name == name {
			elbv2ErrorXML(w, "DuplicateTrustStoreName",
				fmt.Sprintf("A trust store with the same name '%s' already exists.", name),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	}
	id := generateUUID()[:12]
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:truststore/%s/%s", awsRegion(), awsAccountID(), name, id)
	ts := ELBv2TrustStore{
		Arn:                                 arn,
		Name:                                name,
		Status:                              "ACTIVE",
		NumberOfCaCertificates:              1,
		CaCertificatesBundleS3Bucket:        bucket,
		CaCertificatesBundleS3Key:           key,
		CaCertificatesBundleS3ObjectVersion: r.FormValue("CaCertificatesBundleS3ObjectVersion"),
		NextRevocationID:                    1,
		Tags:                                parseELBv2Tags(r, "Tags"),
	}
	elbv2TrustStores.Put(arn, ts)
	elbv2XMLResponse(w, "CreateTrustStore", "<TrustStores>"+elbv2TrustStoreXML(ts)+"</TrustStores>", sim.RequestID(r.Context()))
}

func handleELBv2DescribeTrustStores(w http.ResponseWriter, r *http.Request) {
	stores, nf := filterELBv2TrustStores(r)
	if nf != nil {
		elbv2ErrorXML(w, nf.code, nf.message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<TrustStores>")
	for _, ts := range stores {
		b.WriteString(elbv2TrustStoreXML(ts))
	}
	b.WriteString("</TrustStores>")
	elbv2XMLResponse(w, "DescribeTrustStores", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2ModifyTrustStore(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	bucket := r.FormValue("CaCertificatesBundleS3Bucket")
	key := r.FormValue("CaCertificatesBundleS3Key")
	if bucket == "" || key == "" {
		elbv2ErrorXML(w, "ValidationError", "CaCertificatesBundleS3Bucket and CaCertificatesBundleS3Key are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if !elbv2TrustStores.Update(arn, func(ts *ELBv2TrustStore) {
		ts.CaCertificatesBundleS3Bucket = bucket
		ts.CaCertificatesBundleS3Key = key
		ts.CaCertificatesBundleS3ObjectVersion = r.FormValue("CaCertificatesBundleS3ObjectVersion")
	}) {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ts, _ := elbv2TrustStores.Get(arn)
	elbv2XMLResponse(w, "ModifyTrustStore", "<TrustStores>"+elbv2TrustStoreXML(ts)+"</TrustStores>", sim.RequestID(r.Context()))
}

func handleELBv2DeleteTrustStore(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	if _, ok := elbv2TrustStores.Get(arn); !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if assoc := elbv2TrustStoreAssociations.Filter(func(a ELBv2TrustStoreAssociation) bool { return a.TrustStoreArn == arn }); len(assoc) > 0 {
		elbv2ErrorXML(w, "TrustStoreInUse", "Trust store is associated with one or more resources", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2TrustStores.Delete(arn)
	elbv2XMLResponse(w, "DeleteTrustStore", "", sim.RequestID(r.Context()))
}

func handleELBv2GetTrustStoreCaCertificatesBundle(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	ts, ok := elbv2TrustStores.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	loc := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", ts.CaCertificatesBundleS3Bucket, awsRegion(), ts.CaCertificatesBundleS3Key)
	elbv2XMLResponse(w, "GetTrustStoreCaCertificatesBundle", fmt.Sprintf("<Location>%s</Location>", xmlEscape(loc)), sim.RequestID(r.Context()))
}

func handleELBv2DescribeTrustStoreAssociations(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	if _, ok := elbv2TrustStores.Get(arn); !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<TrustStoreAssociations>")
	for _, a := range elbv2TrustStoreAssociations.Filter(func(a ELBv2TrustStoreAssociation) bool { return a.TrustStoreArn == arn }) {
		fmt.Fprintf(&b, "<member><ResourceArn>%s</ResourceArn></member>", xmlEscape(a.ResourceArn))
	}
	b.WriteString("</TrustStoreAssociations>")
	elbv2XMLResponse(w, "DescribeTrustStoreAssociations", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2DeleteSharedTrustStoreAssociation(w http.ResponseWriter, r *http.Request) {
	tsArn := r.FormValue("TrustStoreArn")
	resourceArn := r.FormValue("ResourceArn")
	if _, ok := elbv2TrustStores.Get(tsArn); !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	found := false
	for _, a := range elbv2TrustStoreAssociations.Filter(func(a ELBv2TrustStoreAssociation) bool {
		return a.TrustStoreArn == tsArn && a.ResourceArn == resourceArn
	}) {
		elbv2TrustStoreAssociations.Delete(elbv2TrustStoreAssociationKey(a))
		found = true
	}
	if !found {
		elbv2ErrorXML(w, "TrustStoreAssociationNotFound", "Trust store association not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "DeleteSharedTrustStoreAssociation", "", sim.RequestID(r.Context()))
}

func handleELBv2AddTrustStoreRevocations(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	if _, ok := elbv2TrustStores.Get(arn); !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var added []ELBv2TrustStoreRevocation
	elbv2TrustStores.Update(arn, func(ts *ELBv2TrustStore) {
		for i := 1; ; i++ {
			base := fmt.Sprintf("RevocationContents.member.%d", i)
			bucket := r.FormValue(base + ".S3Bucket")
			key := r.FormValue(base + ".S3Key")
			revType := r.FormValue(base + ".RevocationType")
			if bucket == "" && key == "" && revType == "" {
				break
			}
			if revType == "" {
				revType = "CRL"
			}
			rev := ELBv2TrustStoreRevocation{
				RevocationID:           ts.NextRevocationID,
				RevocationType:         revType,
				NumberOfRevokedEntries: 1,
				S3Bucket:               bucket,
				S3Key:                  key,
				S3ObjectVersion:        r.FormValue(base + ".S3ObjectVersion"),
			}
			ts.NextRevocationID++
			ts.Revocations = append(ts.Revocations, rev)
			added = append(added, rev)
		}
	})
	var b strings.Builder
	b.WriteString("<TrustStoreRevocations>")
	for _, rev := range added {
		fmt.Fprintf(&b, "<member><TrustStoreArn>%s</TrustStoreArn><RevocationId>%d</RevocationId><RevocationType>%s</RevocationType><NumberOfRevokedEntries>%d</NumberOfRevokedEntries></member>",
			xmlEscape(arn), rev.RevocationID, xmlEscape(rev.RevocationType), rev.NumberOfRevokedEntries)
	}
	b.WriteString("</TrustStoreRevocations>")
	elbv2XMLResponse(w, "AddTrustStoreRevocations", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2RemoveTrustStoreRevocations(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	if _, ok := elbv2TrustStores.Get(arn); !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	removeIDs := map[int64]bool{}
	for _, idStr := range queryList(r, "RevocationIds") {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			removeIDs[id] = true
		}
	}
	elbv2TrustStores.Update(arn, func(ts *ELBv2TrustStore) {
		kept := ts.Revocations[:0]
		for _, rev := range ts.Revocations {
			if !removeIDs[rev.RevocationID] {
				kept = append(kept, rev)
			}
		}
		ts.Revocations = kept
	})
	elbv2XMLResponse(w, "RemoveTrustStoreRevocations", "", sim.RequestID(r.Context()))
}

func handleELBv2DescribeTrustStoreRevocations(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	ts, ok := elbv2TrustStores.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	wanted := map[int64]bool{}
	for _, idStr := range queryList(r, "RevocationIds") {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			wanted[id] = true
		}
	}
	var b strings.Builder
	b.WriteString("<TrustStoreRevocations>")
	for _, rev := range ts.Revocations {
		if len(wanted) > 0 && !wanted[rev.RevocationID] {
			continue
		}
		fmt.Fprintf(&b, "<member><TrustStoreArn>%s</TrustStoreArn><RevocationId>%d</RevocationId><RevocationType>%s</RevocationType><NumberOfRevokedEntries>%d</NumberOfRevokedEntries></member>",
			xmlEscape(arn), rev.RevocationID, xmlEscape(rev.RevocationType), rev.NumberOfRevokedEntries)
	}
	b.WriteString("</TrustStoreRevocations>")
	elbv2XMLResponse(w, "DescribeTrustStoreRevocations", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2GetTrustStoreRevocationContent(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TrustStoreArn")
	ts, ok := elbv2TrustStores.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "TrustStoreNotFound", "Trust store not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	idStr := r.FormValue("RevocationId")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		elbv2ErrorXML(w, "ValidationError", "RevocationId must be a number", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	for _, rev := range ts.Revocations {
		if rev.RevocationID == id {
			loc := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", rev.S3Bucket, awsRegion(), rev.S3Key)
			elbv2XMLResponse(w, "GetTrustStoreRevocationContent", fmt.Sprintf("<Location>%s</Location>", xmlEscape(loc)), sim.RequestID(r.Context()))
			return
		}
	}
	elbv2ErrorXML(w, "RevocationIdNotFound", fmt.Sprintf("Revocation '%d' not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
}

func handleELBv2DescribeSSLPolicies(w http.ResponseWriter, r *http.Request) {
	names := queryList(r, "Names")
	lbType := r.FormValue("LoadBalancerType")
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	var b strings.Builder
	b.WriteString("<SslPolicies>")
	for _, p := range elbv2PredefinedSSLPolicies() {
		if len(wanted) > 0 && !wanted[p.Name] {
			continue
		}
		if lbType != "" && !containsString(p.SupportedLoadBalancerTypes, lbType) {
			continue
		}
		b.WriteString(elbv2SSLPolicyXML(p))
	}
	b.WriteString("</SslPolicies>")
	elbv2XMLResponse(w, "DescribeSSLPolicies", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2GetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceArn")
	if _, ok := elbv2TrustStores.Get(arn); !ok {
		elbv2ErrorXML(w, "ResourceNotFound", fmt.Sprintf("Resource '%s' not found", arn), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"elasticloadbalancing:DescribeTrustStoreAssociations","Resource":"%s"}]}`, awsAccountID(), arn)
	elbv2XMLResponse(w, "GetResourcePolicy", fmt.Sprintf("<Policy>%s</Policy>", xmlEscape(policy)), sim.RequestID(r.Context()))
}

func handleELBv2ModifyCapacityReservation(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	if _, ok := elbv2LoadBalancers.Get(arn); !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	reset := r.FormValue("ResetCapacityReservation") == "true"
	capUnits := r.FormValue("MinimumLoadBalancerCapacity.CapacityUnits")
	elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		if reset {
			lb.MinimumCapacityUnits = ""
		} else if capUnits != "" {
			lb.MinimumCapacityUnits = capUnits
		}
	})
	lb, _ := elbv2LoadBalancers.Get(arn)
	var states strings.Builder
	states.WriteString("<CapacityReservationState>")
	for _, subnet := range lb.Subnets {
		az := awsAvailabilityZone()
		if s, ok := ec2Subnets.Get(subnet); ok && s.AvailabilityZone != "" {
			az = s.AvailabilityZone
		}
		fmt.Fprintf(&states, "<member><State><Code>provisioned</Code></State><AvailabilityZone>%s</AvailabilityZone><EffectiveCapacityUnits>0</EffectiveCapacityUnits></member>", xmlEscape(az))
	}
	states.WriteString("</CapacityReservationState>")
	minCap := ""
	if lb.MinimumCapacityUnits != "" {
		minCap = fmt.Sprintf("<MinimumLoadBalancerCapacity><CapacityUnits>%s</CapacityUnits></MinimumLoadBalancerCapacity>", xmlEscape(lb.MinimumCapacityUnits))
	}
	body := fmt.Sprintf("<LastModifiedTime>%s</LastModifiedTime><DecreaseRequestsRemaining>10</DecreaseRequestsRemaining>%s%s",
		xmlEscape(time.Now().UTC().Format(time.RFC3339)), minCap, states.String())
	elbv2XMLResponse(w, "ModifyCapacityReservation", body, sim.RequestID(r.Context()))
}

func handleELBv2ModifyIpPools(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	if _, ok := elbv2LoadBalancers.Get(arn); !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	remove := r.FormValue("RemoveIpamPools.member.1") != ""
	pool := r.FormValue("IpamPools.Ipv4IpamPoolId")
	elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		if remove {
			lb.Ipv4IpamPoolId = ""
		} else if pool != "" {
			lb.Ipv4IpamPoolId = pool
		}
	})
	lb, _ := elbv2LoadBalancers.Get(arn)
	body := ""
	if lb.Ipv4IpamPoolId != "" {
		body = fmt.Sprintf("<IpamPools><Ipv4IpamPoolId>%s</Ipv4IpamPoolId></IpamPools>", xmlEscape(lb.Ipv4IpamPoolId))
	}
	elbv2XMLResponse(w, "ModifyIpPools", body, sim.RequestID(r.Context()))
}

func elbv2TrustStoreXML(ts ELBv2TrustStore) string {
	totalRevoked := 0
	for _, rev := range ts.Revocations {
		totalRevoked += rev.NumberOfRevokedEntries
	}
	return fmt.Sprintf("<member><Name>%s</Name><TrustStoreArn>%s</TrustStoreArn><Status>%s</Status><NumberOfCaCertificates>%d</NumberOfCaCertificates><TotalRevokedEntries>%d</TotalRevokedEntries></member>",
		xmlEscape(ts.Name), xmlEscape(ts.Arn), xmlEscape(ts.Status), ts.NumberOfCaCertificates, totalRevoked)
}

func elbv2TrustStoreAssociationKey(a ELBv2TrustStoreAssociation) string {
	return a.TrustStoreArn + "|" + a.ResourceArn
}

func filterELBv2TrustStores(r *http.Request) ([]ELBv2TrustStore, *elbv2NotFound) {
	if arns := queryList(r, "TrustStoreArns"); len(arns) > 0 {
		var out []ELBv2TrustStore
		for _, arn := range arns {
			ts, ok := elbv2TrustStores.Get(arn)
			if !ok {
				return nil, &elbv2NotFound{"TrustStoreNotFound", fmt.Sprintf("Trust stores '[%s]' not found", arn)}
			}
			out = append(out, ts)
		}
		return out, nil
	}
	if names := queryList(r, "Names"); len(names) > 0 {
		byName := make(map[string]ELBv2TrustStore)
		for _, ts := range elbv2TrustStores.List() {
			byName[ts.Name] = ts
		}
		var out []ELBv2TrustStore
		for _, n := range names {
			ts, ok := byName[n]
			if !ok {
				return nil, &elbv2NotFound{"TrustStoreNotFound", fmt.Sprintf("Trust stores '[%s]' not found", n)}
			}
			out = append(out, ts)
		}
		return out, nil
	}
	return elbv2TrustStores.List(), nil
}

// elbv2SSLPolicy is one predefined ELB security policy.
type elbv2SSLPolicy struct {
	Name                       string
	SslProtocols               []string
	Ciphers                    []elbv2SSLCipher
	SupportedLoadBalancerTypes []string
}

type elbv2SSLCipher struct {
	Name     string
	Priority int
}

func elbv2SSLPolicyXML(p elbv2SSLPolicy) string {
	var b strings.Builder
	b.WriteString("<member>")
	b.WriteString("<SslProtocols>")
	for _, proto := range p.SslProtocols {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(proto))
	}
	b.WriteString("</SslProtocols>")
	b.WriteString("<Ciphers>")
	for _, c := range p.Ciphers {
		fmt.Fprintf(&b, "<member><Name>%s</Name><Priority>%d</Priority></member>", xmlEscape(c.Name), c.Priority)
	}
	b.WriteString("</Ciphers>")
	fmt.Fprintf(&b, "<Name>%s</Name>", xmlEscape(p.Name))
	b.WriteString("<SupportedLoadBalancerTypes>")
	for _, t := range p.SupportedLoadBalancerTypes {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(t))
	}
	b.WriteString("</SupportedLoadBalancerTypes>")
	b.WriteString("</member>")
	return b.String()
}

func cipherList(names ...string) []elbv2SSLCipher {
	out := make([]elbv2SSLCipher, len(names))
	for i, n := range names {
		out[i] = elbv2SSLCipher{Name: n, Priority: i + 1}
	}
	return out
}

// elbv2PredefinedSSLPolicies returns a faithful subset of the AWS Elastic Load
// Balancing predefined SSL security policies, with their real protocol and
// cipher sets as published in the ELB security-policy reference.
func elbv2PredefinedSSLPolicies() []elbv2SSLPolicy {
	allLBTypes := []string{"application", "network"}
	policies := []elbv2SSLPolicy{
		{
			Name:         "ELBSecurityPolicy-2016-08",
			SslProtocols: []string{"TLSv1", "TLSv1.1", "TLSv1.2"},
			Ciphers: cipherList(
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES128-SHA256", "ECDHE-RSA-AES128-SHA256",
				"ECDHE-ECDSA-AES128-SHA", "ECDHE-RSA-AES128-SHA",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
				"ECDHE-ECDSA-AES256-SHA384", "ECDHE-RSA-AES256-SHA384",
				"ECDHE-RSA-AES256-SHA", "ECDHE-ECDSA-AES256-SHA",
				"AES128-GCM-SHA256", "AES128-SHA256", "AES128-SHA",
				"AES256-GCM-SHA384", "AES256-SHA256", "AES256-SHA",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-TLS-1-2-2017-01",
			SslProtocols: []string{"TLSv1.2"},
			Ciphers: cipherList(
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES128-SHA256", "ECDHE-RSA-AES128-SHA256",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
				"ECDHE-ECDSA-AES256-SHA384", "ECDHE-RSA-AES256-SHA384",
				"AES128-GCM-SHA256", "AES128-SHA256",
				"AES256-GCM-SHA384", "AES256-SHA256",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-TLS-1-2-Ext-2018-06",
			SslProtocols: []string{"TLSv1.2"},
			Ciphers: cipherList(
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES128-SHA256", "ECDHE-RSA-AES128-SHA256",
				"ECDHE-ECDSA-AES128-SHA", "ECDHE-RSA-AES128-SHA",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
				"ECDHE-ECDSA-AES256-SHA384", "ECDHE-RSA-AES256-SHA384",
				"ECDHE-RSA-AES256-SHA", "ECDHE-ECDSA-AES256-SHA",
				"AES128-GCM-SHA256", "AES128-SHA256", "AES128-SHA",
				"AES256-GCM-SHA384", "AES256-SHA256", "AES256-SHA",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-FS-2018-06",
			SslProtocols: []string{"TLSv1", "TLSv1.1", "TLSv1.2"},
			Ciphers: cipherList(
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES128-SHA256", "ECDHE-RSA-AES128-SHA256",
				"ECDHE-ECDSA-AES128-SHA", "ECDHE-RSA-AES128-SHA",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
				"ECDHE-ECDSA-AES256-SHA384", "ECDHE-RSA-AES256-SHA384",
				"ECDHE-RSA-AES256-SHA", "ECDHE-ECDSA-AES256-SHA",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-FS-1-2-Res-2020-10",
			SslProtocols: []string{"TLSv1.2"},
			Ciphers: cipherList(
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-TLS13-1-2-2021-06",
			SslProtocols: []string{"TLSv1.2", "TLSv1.3"},
			Ciphers: cipherList(
				"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES128-SHA256", "ECDHE-RSA-AES128-SHA256",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
				"ECDHE-ECDSA-AES256-SHA384", "ECDHE-RSA-AES256-SHA384",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-TLS13-1-2-Res-2021-06",
			SslProtocols: []string{"TLSv1.2", "TLSv1.3"},
			Ciphers: cipherList(
				"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
				"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-ECDSA-AES256-GCM-SHA384", "ECDHE-RSA-AES256-GCM-SHA384",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
		{
			Name:         "ELBSecurityPolicy-TLS13-1-3-2021-06",
			SslProtocols: []string{"TLSv1.3"},
			Ciphers: cipherList(
				"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
			),
			SupportedLoadBalancerTypes: allLBTypes,
		},
	}
	sort.SliceStable(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies
}
