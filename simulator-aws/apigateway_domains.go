package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// apigwRegionalHostedZones maps a Region to the Route 53 hosted zone of its
// execute-api Regional endpoints, as specs/cloud-api/aws/api-gateway-endpoints
// lists them.
var apigwRegionalHostedZones = map[string]string{
	"us-east-2":      "ZOJJZC49E0EPZ",
	"us-east-1":      "Z1UJRXOUMOOFQ8",
	"us-west-1":      "Z2MUQ32089INYE",
	"us-west-2":      "Z2OJLYMUO9EFXC",
	"af-south-1":     "Z2DHW2332DAMTN",
	"ap-east-1":      "Z3FD1VL90ND7K5",
	"ap-south-2":     "Z0853509Q1135NJ66RUH",
	"ap-southeast-3": "Z10132843TYUYSLUG4HA3",
	"ap-southeast-5": "Z0314042F0KBUTZ3X5HF",
	"ap-southeast-4": "Z092189423Y7RJK61311D",
	"ap-south-1":     "Z3VO1THU9YC4UR",
	"ap-northeast-3": "Z22ILHG95FLSZ2",
	"ap-northeast-2": "Z20JF4UZKIW1U8",
	"ap-southeast-1": "ZL327KTPIQFUL",
	"ap-southeast-2": "Z2RPCDW04V8134",
	"ap-east-2":      "Z02909591O7FG9Q56HWB1",
	"ap-southeast-7": "Z048508712PZLK5NKG8R0",
	"ap-northeast-1": "Z1YSHQZHG15GKL",
	"ca-central-1":   "Z19DQILCV0OWEC",
	"ca-west-1":      "Z04745493436AWVTG1OQY",
	"eu-central-1":   "Z1U9ULNL0V5AJ3",
	"eu-west-1":      "ZLY8HYME6SFDD",
	"eu-west-2":      "ZJ5UAJN8Y3Z2Q",
	"eu-south-1":     "Z3BT4WSQ9TDYZV",
	"eu-west-3":      "Z3KY65QIEKYHQQ",
	"eu-south-2":     "Z02499852UI5HEQ5JVWX3",
	"eu-north-1":     "Z3UWIKFBOOGXPP",
	"eu-central-2":   "Z09222482MK253X48U76H",
	"il-central-1":   "Z07264553HBI44N5X2CKP",
	"mx-central-1":   "Z00020171WIGL5M88SHRM",
	"me-south-1":     "Z20ZBPC0SS8806",
	"me-central-1":   "Z08780021BKYYY8U0YHTV",
	"sa-east-1":      "ZCMLWB8V5SYIT",
	"us-gov-east-1":  "Z3SE9ATJYCRCZJ",
	"us-gov-west-1":  "Z1K6XKP9SAGWDV",
}

type APIGWEndpointConfiguration struct {
	Types          []string `json:"types,omitempty"`
	IpAddressType  string   `json:"ipAddressType,omitempty"`
	VpcEndpointIds []string `json:"vpcEndpointIds,omitempty"`
}

// Edge-optimized endpoints answer from Amazon CloudFront's single hosted zone.
const apigwEdgeHostedZone = "Z2FDTNDATAQYW2"

func registerAPIGatewayDomains(srv *sim.Server) {
	const src = "apigateway.amazonaws.com"
	domainResource := cloudTrailRESTResource("AWS::ApiGateway::DomainName", "domainName")
	srv.HandleFunc("POST /domainnames", cloudTrailRecordedREST("CreateDomainName", src, nil, handleAPIGWCreateDomainName))
	srv.HandleFunc("GET /domainnames", cloudTrailRecordedREST("GetDomainNames", src, nil, handleAPIGWGetDomainNames))
	srv.HandleFunc("GET /domainnames/{domainName}", cloudTrailRecordedREST("GetDomainName", src, domainResource, handleAPIGWGetDomainName))
	srv.HandleFunc("DELETE /domainnames/{domainName}", cloudTrailRecordedREST("DeleteDomainName", src, domainResource, handleAPIGWDeleteDomainName))
	srv.HandleFunc("POST /vpclinks", cloudTrailRecordedREST("CreateVpcLink", src, nil, handleAPIGWCreateVpcLink))
	srv.HandleFunc("GET /vpclinks", cloudTrailRecordedREST("GetVpcLinks", src, nil, handleAPIGWGetVpcLinks))
	srv.HandleFunc("GET /vpclinks/{vpcLinkId}", cloudTrailRecordedREST("GetVpcLink", src, nil, handleAPIGWGetVpcLink))
	srv.HandleFunc("DELETE /vpclinks/{vpcLinkId}", cloudTrailRecordedREST("DeleteVpcLink", src, nil, handleAPIGWDeleteVpcLink))
}

// A private custom domain name shares its name with public ones and is told
// apart by domainNameId, which its ARN carries after a plus sign.
func apigwDomainKey(r *http.Request) string {
	name := sim.PathParam(r, "domainName")
	if id := r.URL.Query().Get("domainNameId"); id != "" {
		return name + "+" + id
	}
	return name
}

// apigwCheckCertificate explains why it refuses an AWS Certificate Manager certificate that
// does not exist, is not ISSUED, or lives outside the Region the endpoint
// type requires, or returns "".
func apigwCheckCertificate(arn, region string) string {
	if !strings.HasPrefix(arn, "arn:aws:acm:"+region+":") {
		return fmt.Sprintf("Certificate %s must be in Region %s", arn, region)
	}
	stored, ok := acmCertificates.Get(acmARNToID(arn))
	if !ok {
		return fmt.Sprintf("Certificate %s not found", arn)
	}
	if stored.Cert.Status != "ISSUED" {
		return fmt.Sprintf("Certificate %s is in status %s, not ISSUED", arn, stored.Cert.Status)
	}
	return ""
}

func apigwRandomLower(n int) string {
	return strings.ToLower(strings.ReplaceAll(sim.NewUUID(), "-", ""))[:n]
}

func handleAPIGWCreateDomainName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainName              string                      `json:"domainName"`
		CertificateName         string                      `json:"certificateName"`
		CertificateBody         string                      `json:"certificateBody"`
		CertificatePrivateKey   string                      `json:"certificatePrivateKey"`
		CertificateChain        string                      `json:"certificateChain"`
		CertificateArn          string                      `json:"certificateArn"`
		RegionalCertificateName string                      `json:"regionalCertificateName"`
		RegionalCertificateArn  string                      `json:"regionalCertificateArn"`
		EndpointConfiguration   *APIGWEndpointConfiguration `json:"endpointConfiguration"`
		Tags                    map[string]string           `json:"tags"`
		SecurityPolicy          string                      `json:"securityPolicy"`
		EndpointAccessMode      string                      `json:"endpointAccessMode"`
		Policy                  string                      `json:"policy"`
		RoutingMode             string                      `json:"routingMode"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DomainName == "" {
		AWSError(w, "BadRequestException", "domainName is required", http.StatusBadRequest)
		return
	}
	endpoint := "EDGE"
	cfg := APIGWEndpointConfiguration{Types: []string{endpoint}}
	if req.EndpointConfiguration != nil && len(req.EndpointConfiguration.Types) > 0 {
		cfg = *req.EndpointConfiguration
		if len(cfg.Types) != 1 {
			AWSError(w, "BadRequestException", "endpointConfiguration.types must name exactly one endpoint type", http.StatusBadRequest)
			return
		}
		endpoint = cfg.Types[0]
	}
	if cfg.IpAddressType == "" {
		cfg.IpAddressType = "ipv4"
	}
	now := time.Now().Unix()
	d := APIGWv1DomainName{
		DomainName:            req.DomainName,
		EndpointConfiguration: &cfg,
		SecurityPolicy:        req.SecurityPolicy,
		EndpointAccessMode:    req.EndpointAccessMode,
		RoutingMode:           req.RoutingMode,
		DomainNameStatus:      "AVAILABLE",
		Tags:                  req.Tags,
	}
	if d.SecurityPolicy == "" {
		d.SecurityPolicy = "TLS_1_2"
	}
	if d.RoutingMode == "" {
		d.RoutingMode = "BASE_PATH_MAPPING_ONLY"
	}
	key := req.DomainName
	switch endpoint {
	case "EDGE":
		switch {
		case req.CertificateArn != "":
			// Amazon CloudFront serves edge-optimized endpoints and reads its
			// certificates from US East (N. Virginia).
			if msg := apigwCheckCertificate(req.CertificateArn, "us-east-1"); msg != "" {
				AWSError(w, "BadRequestException", msg, http.StatusBadRequest)
				return
			}
			d.CertificateArn = req.CertificateArn
		case req.CertificateBody != "":
			if _, err := tls.X509KeyPair([]byte(req.CertificateBody+"\n"+req.CertificateChain), []byte(req.CertificatePrivateKey)); err != nil {
				AWSError(w, "BadRequestException", "Invalid certificate body or private key: "+err.Error(), http.StatusBadRequest)
				return
			}
		default:
			AWSError(w, "BadRequestException", "An edge-optimized domain name requires certificateArn or certificateBody", http.StatusBadRequest)
			return
		}
		d.CertificateName = req.CertificateName
		d.CertificateUploadDate = now
		d.DistributionDomainName = "d" + apigwRandomLower(13) + ".cloudfront.net"
		d.DistributionHostedZoneId = apigwEdgeHostedZone
		d.DomainNameArn = apigwDomainNameARN(req.DomainName)
	case "REGIONAL", "PRIVATE":
		if req.RegionalCertificateArn == "" {
			AWSError(w, "BadRequestException", "A "+strings.ToLower(endpoint)+" domain name requires regionalCertificateArn", http.StatusBadRequest)
			return
		}
		if msg := apigwCheckCertificate(req.RegionalCertificateArn, awsRegion()); msg != "" {
			AWSError(w, "BadRequestException", msg, http.StatusBadRequest)
			return
		}
		d.RegionalCertificateArn = req.RegionalCertificateArn
		d.RegionalCertificateName = req.RegionalCertificateName
		if endpoint == "REGIONAL" {
			d.RegionalDomainName = fmt.Sprintf("d-%s.execute-api.%s.amazonaws.com", apigwRandomLower(10), awsRegion())
			d.RegionalHostedZoneId = apigwRegionalHostedZones[awsRegion()]
			d.DomainNameArn = apigwDomainNameARN(req.DomainName)
		} else {
			if req.Policy == "" {
				AWSError(w, "BadRequestException", "A private domain name requires a policy", http.StatusBadRequest)
				return
			}
			d.DomainNameId = apigwRandomLower(10)
			d.Policy = req.Policy
			key = req.DomainName + "+" + d.DomainNameId
			d.DomainNameArn = fmt.Sprintf("arn:aws:apigateway:%s:%s:/domainnames/%s", awsRegion(), awsAccountID(), key)
		}
	default:
		AWSError(w, "BadRequestException", "Invalid endpoint type "+endpoint, http.StatusBadRequest)
		return
	}
	if endpoint != "PRIVATE" {
		if _, exists := apigwV1DomainNames.Get(key); exists {
			AWSErrorf(w, "ConflictException", http.StatusConflict, "The domain name you provided already exists.")
			return
		}
	}
	apigwV1DomainNames.Put(key, d)
	sim.WriteJSON(w, http.StatusCreated, d)
}

func handleAPIGWGetDomainNames(w http.ResponseWriter, r *http.Request) {
	all := apigwV1DomainNames.List()
	if all == nil {
		all = []APIGWv1DomainName{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWGetDomainName(w http.ResponseWriter, r *http.Request) {
	d, ok := apigwV1DomainNames.Get(apigwDomainKey(r))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleAPIGWDeleteDomainName(w http.ResponseWriter, r *http.Request) {
	key := apigwDomainKey(r)
	if _, ok := apigwV1DomainNames.Get(key); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	apigwDeleteDomain(key)
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateVpcLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		TargetArns  []string          `json:"targetArns"`
		Tags        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.TargetArns) == 0 {
		AWSError(w, "BadRequestException", "name and targetArns are required", http.StatusBadRequest)
		return
	}
	v := APIGWv1VpcLink{
		Id:          apigwRandomLower(6),
		Name:        req.Name,
		Description: req.Description,
		TargetArns:  req.TargetArns,
		Status:      "PENDING",
		Tags:        req.Tags,
	}
	apigwV1VpcLinks.Put(v.Id, v)
	go apigwProvisionVpcLink(v.Id, req.TargetArns)
	sim.WriteJSON(w, http.StatusAccepted, v)
}

// apigwProvisionVpcLink attaches the link to its Network Load Balancers: it
// turns AVAILABLE when every target is an active Network Load Balancer, and
// FAILED otherwise.
func apigwProvisionVpcLink(id string, targets []string) {
	status, message := "AVAILABLE", ""
	for _, arn := range targets {
		lb, ok := elbv2LoadBalancers.Get(arn)
		switch {
		case !ok:
			status, message = "FAILED", "NLB ARN is malformed or does not exist: "+arn
		case lb.Type != "network":
			status, message = "FAILED", "Target "+arn+" is not a Network Load Balancer"
		}
		if status == "FAILED" {
			break
		}
	}
	apigwV1VpcLinks.Update(id, func(v *APIGWv1VpcLink) {
		if v.Status == "PENDING" {
			v.Status, v.StatusMessage = status, message
		}
	})
}

func handleAPIGWGetVpcLinks(w http.ResponseWriter, r *http.Request) {
	all := apigwV1VpcLinks.List()
	if all == nil {
		all = []APIGWv1VpcLink{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWGetVpcLink(w http.ResponseWriter, r *http.Request) {
	v, ok := apigwV1VpcLinks.Get(sim.PathParam(r, "vpcLinkId"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid VPC link identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleAPIGWDeleteVpcLink(w http.ResponseWriter, r *http.Request) {
	if !apigwV1VpcLinks.Delete(sim.PathParam(r, "vpcLinkId")) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid VPC link identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
