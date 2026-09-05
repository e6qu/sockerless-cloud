package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 Object Lambda, and the S3 access points it is built on.
//
// An Object Lambda access point sits in front of a standard access point and
// runs a transformation function on every GetObject: S3 invokes the function
// with a route token, the function fetches the original object, transforms it,
// and posts the result back through WriteGetObjectResponse on that token. The
// caller receives what the function wrote, never the stored bytes.
//
// The whole loop runs here, over the simulator's own Lambda and S3: the
// control plane is the s3-control surface a client manages access points
// through, and the data plane is the host-addressed GET a client makes against
// the access point's own endpoint. Serving the callback alone would have
// acknowledged writes nothing could read back, which is why it waited for the
// access points to exist.

// S3AccessPoint is a standard access point over one bucket.
type S3AccessPoint struct {
	Name         string `json:"name"`
	AccountID    string `json:"accountId"`
	Bucket       string `json:"bucket"`
	BucketAcctID string `json:"bucketAccountId,omitempty"`
	VPCID        string `json:"vpcId,omitempty"`
	CreationDate string `json:"creationDate"`
	Policy       string `json:"policy,omitempty"`
	// Scope narrows what the access point admits — the prefixes it reaches and
	// the operations it allows. Nil until one is put, which is the unrestricted
	// access point every create makes.
	Scope *s3AccessPointScope `json:"scope,omitempty"`
}

// S3ObjectLambdaAccessPoint is an access point whose reads run through a
// transformation function.
type S3ObjectLambdaAccessPoint struct {
	Name          string                      `json:"name"`
	AccountID     string                      `json:"accountId"`
	CreationDate  string                      `json:"creationDate"`
	Configuration S3ObjectLambdaConfiguration `json:"configuration"`
	Policy        string                      `json:"policy,omitempty"`
}

// S3ObjectLambdaConfiguration mirrors ObjectLambdaConfiguration: the standard
// access point the reads are served from, and the function that transforms
// them.
type S3ObjectLambdaConfiguration struct {
	SupportingAccessPoint        string                         `xml:"SupportingAccessPoint" json:"supportingAccessPoint"`
	CloudWatchMetricsEnabled     bool                           `xml:"CloudWatchMetricsEnabled,omitempty" json:"cloudWatchMetricsEnabled,omitempty"`
	AllowedFeatures              []string                       `xml:"AllowedFeatures>AllowedFeature,omitempty" json:"allowedFeatures,omitempty"`
	TransformationConfigurations []S3ObjectLambdaTransformation `xml:"TransformationConfigurations>TransformationConfiguration" json:"transformationConfigurations"`
}

// S3ObjectLambdaTransformation names the actions a function transforms and the
// function that does it.
type S3ObjectLambdaTransformation struct {
	Actions []string                            `xml:"Actions>Action" json:"actions"`
	Content S3ObjectLambdaTransformationContent `xml:"ContentTransformation" json:"contentTransformation"`
}

// S3ObjectLambdaTransformationContent carries the AWS Lambda transformation.
type S3ObjectLambdaTransformationContent struct {
	AwsLambda S3ObjectLambdaFunctionRef `xml:"AwsLambda" json:"awsLambda"`
}

// S3ObjectLambdaFunctionRef is the function ARN and its opaque payload.
type S3ObjectLambdaFunctionRef struct {
	FunctionArn     string `xml:"FunctionArn" json:"functionArn"`
	FunctionPayload string `xml:"FunctionPayload,omitempty" json:"functionPayload,omitempty"`
}

var (
	s3AccessPoints             sim.Store[S3AccessPoint]
	s3ObjectLambdaAccessPoints sim.Store[S3ObjectLambdaAccessPoint]

	// s3ObjectLambdaRoutes holds the channel each in-flight GetObject waits on
	// while its transformation function runs. The function answers by posting
	// to WriteGetObjectResponse with the route it was given, which is how the
	// bytes reach the caller: nothing else connects the two requests.
	s3ObjectLambdaRoutes   = map[string]chan s3ObjectLambdaResult{}
	s3ObjectLambdaRoutesMu sync.Mutex
)

// s3ObjectLambdaResult is what a transformation function wrote back.
type s3ObjectLambdaResult struct {
	status  int
	headers http.Header
	body    []byte
}

func s3AccessPointKey(account, name string) string { return account + "/" + name }

// s3AccessPointAlias is the DNS label a client addresses an access point by.
// Real S3 appends a random suffix; the simulator uses the name, which is what
// makes the alias resolvable back to the access point it names.
func s3AccessPointAlias(name, account string) string { return name + "-" + account }

func s3AccessPointARN(account, name string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:accesspoint/%s", awsRegion(), account, name)
}

func s3ObjectLambdaARN(account, name string) string {
	return fmt.Sprintf("arn:aws:s3-object-lambda:%s:%s:accesspoint/%s", awsRegion(), account, name)
}

// s3ControlAccountID reads the account the s3-control request is for. Every
// operation carries it in x-amz-account-id, which is how the service knows
// whose access points a request is about.
func s3ControlAccountID(r *http.Request) string {
	if id := r.Header.Get("x-amz-account-id"); id != "" {
		return id
	}
	return awsAccountID()
}

func registerS3ObjectLambda(srv *sim.Server) {
	s3AccessPoints = sim.MakeStore[S3AccessPoint](srv.DB(), "s3_access_points")
	s3ObjectLambdaAccessPoints = sim.MakeStore[S3ObjectLambdaAccessPoint](srv.DB(), "s3_object_lambda_access_points")

	// ── Standard access points ───────────────────────────────────────────
	srv.HandleFunc("PUT /v20180820/accesspoint/{name}", handleS3CreateAccessPoint)
	srv.HandleFunc("GET /v20180820/accesspoint/{name}", handleS3GetAccessPoint)
	srv.HandleFunc("DELETE /v20180820/accesspoint/{name}", handleS3DeleteAccessPoint)
	srv.HandleFunc("GET /v20180820/accesspoint", handleS3ListAccessPoints)
	srv.HandleFunc("PUT /v20180820/accesspoint/{name}/policy", handleS3PutAccessPointPolicy)
	srv.HandleFunc("GET /v20180820/accesspoint/{name}/policy", handleS3GetAccessPointPolicy)
	srv.HandleFunc("DELETE /v20180820/accesspoint/{name}/policy", handleS3DeleteAccessPointPolicy)
	srv.HandleFunc("GET /v20180820/accesspoint/{name}/policyStatus", handleS3GetAccessPointPolicyStatus)

	// ── Object Lambda access points ──────────────────────────────────────
	srv.HandleFunc("PUT /v20180820/accesspointforobjectlambda/{name}", handleS3CreateAccessPointForObjectLambda)
	srv.HandleFunc("GET /v20180820/accesspointforobjectlambda/{name}", handleS3GetAccessPointForObjectLambda)
	srv.HandleFunc("DELETE /v20180820/accesspointforobjectlambda/{name}", handleS3DeleteAccessPointForObjectLambda)
	srv.HandleFunc("GET /v20180820/accesspointforobjectlambda", handleS3ListAccessPointsForObjectLambda)
	srv.HandleFunc("GET /v20180820/accesspointforobjectlambda/{name}/configuration", handleS3GetAccessPointConfigurationForObjectLambda)
	srv.HandleFunc("PUT /v20180820/accesspointforobjectlambda/{name}/configuration", handleS3PutAccessPointConfigurationForObjectLambda)
	srv.HandleFunc("PUT /v20180820/accesspointforobjectlambda/{name}/policy", handleS3PutAccessPointPolicyForObjectLambda)
	srv.HandleFunc("GET /v20180820/accesspointforobjectlambda/{name}/policy", handleS3GetAccessPointPolicyForObjectLambda)
	srv.HandleFunc("DELETE /v20180820/accesspointforobjectlambda/{name}/policy", handleS3DeleteAccessPointPolicyForObjectLambda)
	srv.HandleFunc("GET /v20180820/accesspointforobjectlambda/{name}/policyStatus", handleS3GetAccessPointPolicyStatusForObjectLambda)

	// ── The transformation callback ──────────────────────────────────────
	srv.HandleFunc("POST /WriteGetObjectResponse", handleS3WriteGetObjectResponse)
}

// s3ControlError writes the ErrorResponse envelope the s3-control surface
// wraps its errors in. The S3 data plane uses a bare Error document instead,
// which is what s3ObjectLambdaError writes.
func s3ControlError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Code    string   `xml:"Error>Code"`
		Message string   `xml:"Error>Message"`
	}{Code: code, Message: message})
}

// ── Standard access points ───────────────────────────────────────────────

func handleS3CreateAccessPoint(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	var req struct {
		XMLName          xml.Name `xml:"CreateAccessPointRequest"`
		Bucket           string   `xml:"Bucket"`
		BucketAccountID  string   `xml:"BucketAccountId"`
		VpcConfiguration *struct {
			VpcID string `xml:"VpcId"`
		} `xml:"VpcConfiguration"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		s3ControlError(w, "MalformedXML", "the request body is not a CreateAccessPointRequest", http.StatusBadRequest)
		return
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		s3ControlError(w, "MalformedXML", "could not parse the request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Bucket == "" {
		s3ControlError(w, "InvalidRequest", "Bucket is required", http.StatusBadRequest)
		return
	}
	// An access point is over a bucket, so the bucket has to be there: one
	// over a bucket that does not exist would serve reads of nothing.
	if _, ok := s3Buckets_.Get(req.Bucket); !ok {
		s3ControlError(w, "NoSuchBucket", "The specified bucket does not exist", http.StatusNotFound)
		return
	}
	if _, exists := s3AccessPoints.Get(s3AccessPointKey(account, name)); exists {
		s3ControlError(w, "AccessPointAlreadyOwnedByYou",
			"An access point with the same name already exists in this account", http.StatusConflict)
		return
	}
	ap := S3AccessPoint{
		Name: name, AccountID: account, Bucket: req.Bucket,
		BucketAcctID: req.BucketAccountID,
		CreationDate: time.Now().UTC().Format(time.RFC3339),
	}
	if req.VpcConfiguration != nil {
		ap.VPCID = req.VpcConfiguration.VpcID
	}
	s3AccessPoints.Put(s3AccessPointKey(account, name), ap)
	WriteXML(w, http.StatusOK, struct {
		XMLName        xml.Name `xml:"CreateAccessPointResult"`
		AccessPointArn string   `xml:"AccessPointArn"`
		Alias          string   `xml:"Alias"`
	}{AccessPointArn: s3AccessPointARN(account, name), Alias: s3AccessPointAlias(name, account)})
}

func handleS3GetAccessPoint(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	ap, ok := s3AccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName        xml.Name `xml:"GetAccessPointResult"`
		Name           string   `xml:"Name"`
		Bucket         string   `xml:"Bucket"`
		NetworkOrigin  string   `xml:"NetworkOrigin"`
		CreationDate   string   `xml:"CreationDate"`
		Alias          string   `xml:"Alias"`
		AccessPointArn string   `xml:"AccessPointArn"`
	}{
		Name: ap.Name, Bucket: ap.Bucket,
		NetworkOrigin:  s3AccessPointNetworkOrigin(ap),
		CreationDate:   ap.CreationDate,
		Alias:          s3AccessPointAlias(ap.Name, ap.AccountID),
		AccessPointArn: s3AccessPointARN(ap.AccountID, ap.Name),
	})
}

// s3AccessPointNetworkOrigin is VPC for an access point restricted to one and
// Internet otherwise — the two values the enum declares.
func s3AccessPointNetworkOrigin(ap S3AccessPoint) string {
	if ap.VPCID != "" {
		return "VPC"
	}
	return "Internet"
}

func handleS3DeleteAccessPoint(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	if !s3AccessPoints.Delete(s3AccessPointKey(account, name)) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListAccessPoints(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	bucket := r.URL.Query().Get("bucket")
	type entry struct {
		Name           string `xml:"Name"`
		NetworkOrigin  string `xml:"NetworkOrigin"`
		Bucket         string `xml:"Bucket"`
		AccessPointArn string `xml:"AccessPointArn"`
		Alias          string `xml:"Alias"`
	}
	var items []entry
	for _, ap := range s3AccessPoints.List() {
		if ap.AccountID != account {
			continue
		}
		if bucket != "" && ap.Bucket != bucket {
			continue
		}
		items = append(items, entry{
			Name: ap.Name, NetworkOrigin: s3AccessPointNetworkOrigin(ap), Bucket: ap.Bucket,
			AccessPointArn: s3AccessPointARN(ap.AccountID, ap.Name),
			Alias:          s3AccessPointAlias(ap.Name, ap.AccountID),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	WriteXML(w, http.StatusOK, struct {
		XMLName      xml.Name `xml:"ListAccessPointsResult"`
		AccessPoints []entry  `xml:"AccessPointList>AccessPoint"`
	}{AccessPoints: items})
}

func handleS3PutAccessPointPolicy(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	policy, ok := s3ReadPolicyBody(w, r)
	if !ok {
		return
	}
	if !s3AccessPoints.Update(s3AccessPointKey(account, name), func(ap *S3AccessPoint) { ap.Policy = policy }) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3GetAccessPointPolicy(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	ap, ok := s3AccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	if ap.Policy == "" {
		s3ControlError(w, "NoSuchAccessPointPolicy", "The specified accesspoint policy does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"GetAccessPointPolicyResult"`
		Policy  string   `xml:"Policy"`
	}{Policy: ap.Policy})
}

func handleS3DeleteAccessPointPolicy(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	if !s3AccessPoints.Update(s3AccessPointKey(account, name), func(ap *S3AccessPoint) { ap.Policy = "" }) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3GetAccessPointPolicyStatus(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	ap, ok := s3AccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName  xml.Name `xml:"GetAccessPointPolicyStatusResult"`
		IsPublic bool     `xml:"PolicyStatus>IsPublic"`
	}{IsPublic: s3PolicyIsPublic(ap.Policy)})
}

// s3PolicyIsPublic reports whether a resource policy grants to everyone, which
// is what PolicyStatus answers. A policy naming a principal is not public.
func s3PolicyIsPublic(policy string) bool {
	if policy == "" {
		return false
	}
	var doc struct {
		Statement []struct {
			Principal any `json:"Principal"`
		} `json:"Statement"`
	}
	if json.Unmarshal([]byte(policy), &doc) != nil {
		return false
	}
	for _, st := range doc.Statement {
		switch p := st.Principal.(type) {
		case string:
			if p == "*" {
				return true
			}
		case map[string]any:
			if aws, ok := p["AWS"].(string); ok && aws == "*" {
				return true
			}
		}
	}
	return false
}

// s3ReadPolicyBody reads the policy document a Put*Policy request carries.
func s3ReadPolicyBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3ControlError(w, "MalformedXML", "could not read the request body", http.StatusBadRequest)
		return "", false
	}
	var req struct {
		Policy string `xml:"Policy"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		s3ControlError(w, "MalformedXML", "could not parse the request: "+err.Error(), http.StatusBadRequest)
		return "", false
	}
	if req.Policy == "" {
		s3ControlError(w, "InvalidRequest", "Policy is required", http.StatusBadRequest)
		return "", false
	}
	return req.Policy, true
}

// ── Object Lambda access points ──────────────────────────────────────────

func handleS3CreateAccessPointForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	var req struct {
		XMLName       xml.Name                    `xml:"CreateAccessPointForObjectLambdaRequest"`
		Configuration S3ObjectLambdaConfiguration `xml:"Configuration"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		s3ControlError(w, "MalformedXML", "the request body is not a CreateAccessPointForObjectLambdaRequest", http.StatusBadRequest)
		return
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		s3ControlError(w, "MalformedXML", "could not parse the request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg, ok := s3ValidateOLAPConfiguration(account, req.Configuration); !ok {
		s3ControlError(w, "InvalidRequest", msg, http.StatusBadRequest)
		return
	}
	if _, exists := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name)); exists {
		s3ControlError(w, "AccessPointAlreadyOwnedByYou",
			"An Object Lambda access point with the same name already exists in this account", http.StatusConflict)
		return
	}
	s3ObjectLambdaAccessPoints.Put(s3AccessPointKey(account, name), S3ObjectLambdaAccessPoint{
		Name: name, AccountID: account,
		CreationDate:  time.Now().UTC().Format(time.RFC3339),
		Configuration: req.Configuration,
	})
	WriteXML(w, http.StatusOK, struct {
		XMLName                    xml.Name `xml:"CreateAccessPointForObjectLambdaResult"`
		ObjectLambdaAccessPointArn string   `xml:"ObjectLambdaAccessPointArn"`
		Alias                      struct {
			Value  string `xml:"Value"`
			Status string `xml:"Status"`
		} `xml:"Alias"`
	}{
		ObjectLambdaAccessPointArn: s3ObjectLambdaARN(account, name),
		Alias: struct {
			Value  string `xml:"Value"`
			Status string `xml:"Status"`
		}{Value: s3AccessPointAlias(name, account), Status: "READY"},
	})
}

// s3ValidateOLAPConfiguration holds the configuration to what the service will
// serve reads through: a supporting access point that exists, and a
// transformation naming a function that exists. Accepting either dangling
// would create an access point whose every read fails.
func s3ValidateOLAPConfiguration(account string, cfg S3ObjectLambdaConfiguration) (string, bool) {
	if cfg.SupportingAccessPoint == "" {
		return "SupportingAccessPoint is required", false
	}
	supporting := cfg.SupportingAccessPoint
	if i := strings.LastIndex(supporting, "accesspoint/"); i >= 0 {
		supporting = supporting[i+len("accesspoint/"):]
	}
	if _, ok := s3AccessPoints.Get(s3AccessPointKey(account, supporting)); !ok {
		return "The supporting access point " + cfg.SupportingAccessPoint + " does not exist", false
	}
	if len(cfg.TransformationConfigurations) == 0 {
		return "TransformationConfigurations is required", false
	}
	for _, tc := range cfg.TransformationConfigurations {
		if len(tc.Actions) == 0 {
			return "each TransformationConfiguration must name at least one Action", false
		}
		arn := tc.Content.AwsLambda.FunctionArn
		if arn == "" {
			return "ContentTransformation must name an AwsLambda FunctionArn", false
		}
		if _, ok := lambdaFunctions.Get(ebLambdaNameFromARN(arn)); !ok {
			return "The transformation function " + arn + " does not exist", false
		}
	}
	return "", true
}

func handleS3GetAccessPointForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	olap, ok := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName                        xml.Name `xml:"GetAccessPointForObjectLambdaResult"`
		Name                           string   `xml:"Name"`
		PublicAccessBlockConfiguration struct {
			BlockPublicAcls       bool `xml:"BlockPublicAcls"`
			IgnorePublicAcls      bool `xml:"IgnorePublicAcls"`
			BlockPublicPolicy     bool `xml:"BlockPublicPolicy"`
			RestrictPublicBuckets bool `xml:"RestrictPublicBuckets"`
		} `xml:"PublicAccessBlockConfiguration"`
		CreationDate string `xml:"CreationDate"`
		Alias        struct {
			Value  string `xml:"Value"`
			Status string `xml:"Status"`
		} `xml:"Alias"`
	}{
		Name: olap.Name, CreationDate: olap.CreationDate,
		PublicAccessBlockConfiguration: struct {
			BlockPublicAcls       bool `xml:"BlockPublicAcls"`
			IgnorePublicAcls      bool `xml:"IgnorePublicAcls"`
			BlockPublicPolicy     bool `xml:"BlockPublicPolicy"`
			RestrictPublicBuckets bool `xml:"RestrictPublicBuckets"`
		}{true, true, true, true},
		Alias: struct {
			Value  string `xml:"Value"`
			Status string `xml:"Status"`
		}{Value: s3AccessPointAlias(olap.Name, olap.AccountID), Status: "READY"},
	})
}

func handleS3DeleteAccessPointForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	if !s3ObjectLambdaAccessPoints.Delete(s3AccessPointKey(account, name)) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListAccessPointsForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	type alias struct {
		Value  string `xml:"Value"`
		Status string `xml:"Status"`
	}
	type entry struct {
		Name                       string `xml:"Name"`
		ObjectLambdaAccessPointArn string `xml:"ObjectLambdaAccessPointArn"`
		Alias                      alias  `xml:"Alias"`
	}
	var items []entry
	for _, olap := range s3ObjectLambdaAccessPoints.List() {
		if olap.AccountID != account {
			continue
		}
		items = append(items, entry{
			Name:                       olap.Name,
			ObjectLambdaAccessPointArn: s3ObjectLambdaARN(olap.AccountID, olap.Name),
			Alias:                      alias{Value: s3AccessPointAlias(olap.Name, olap.AccountID), Status: "READY"},
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	WriteXML(w, http.StatusOK, struct {
		XMLName      xml.Name `xml:"ListAccessPointsForObjectLambdaResult"`
		AccessPoints []entry  `xml:"ObjectLambdaAccessPointList>ObjectLambdaAccessPoint"`
	}{AccessPoints: items})
}

func handleS3GetAccessPointConfigurationForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	olap, ok := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName       xml.Name                    `xml:"GetAccessPointConfigurationForObjectLambdaResult"`
		Configuration S3ObjectLambdaConfiguration `xml:"Configuration"`
	}{Configuration: olap.Configuration})
}

func handleS3PutAccessPointConfigurationForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	var req struct {
		XMLName       xml.Name                    `xml:"PutAccessPointConfigurationForObjectLambdaRequest"`
		Configuration S3ObjectLambdaConfiguration `xml:"Configuration"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		s3ControlError(w, "MalformedXML", "the request body is not a PutAccessPointConfigurationForObjectLambdaRequest", http.StatusBadRequest)
		return
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		s3ControlError(w, "MalformedXML", "could not parse the request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg, ok := s3ValidateOLAPConfiguration(account, req.Configuration); !ok {
		s3ControlError(w, "InvalidRequest", msg, http.StatusBadRequest)
		return
	}
	if !s3ObjectLambdaAccessPoints.Update(s3AccessPointKey(account, name), func(o *S3ObjectLambdaAccessPoint) {
		o.Configuration = req.Configuration
	}) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3PutAccessPointPolicyForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	policy, ok := s3ReadPolicyBody(w, r)
	if !ok {
		return
	}
	if !s3ObjectLambdaAccessPoints.Update(s3AccessPointKey(account, name), func(o *S3ObjectLambdaAccessPoint) { o.Policy = policy }) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3GetAccessPointPolicyForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	olap, ok := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	if olap.Policy == "" {
		s3ControlError(w, "NoSuchAccessPointPolicy", "The specified accesspoint policy does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"GetAccessPointPolicyForObjectLambdaResult"`
		Policy  string   `xml:"Policy"`
	}{Policy: olap.Policy})
}

func handleS3DeleteAccessPointPolicyForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	if !s3ObjectLambdaAccessPoints.Update(s3AccessPointKey(account, name), func(o *S3ObjectLambdaAccessPoint) { o.Policy = "" }) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3GetAccessPointPolicyStatusForObjectLambda(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	olap, ok := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName  xml.Name `xml:"GetAccessPointPolicyStatusForObjectLambdaResult"`
		IsPublic bool     `xml:"PolicyStatus>IsPublic"`
	}{IsPublic: s3PolicyIsPublic(olap.Policy)})
}

// ── The Object Lambda data path ──────────────────────────────────────────

// s3ObjectLambdaError writes an error on the S3 data plane, where the error
// document is a bare Error element — the shape an S3 client parses the code
// out of.
func s3ObjectLambdaError(w http.ResponseWriter, r *http.Request, code, message string, status int) {
	S3ErrorXML(w, code, message, r.URL.Path, sim.RequestID(r.Context()), status)
}

// s3ObjectLambdaHostAccessPoint reads the Object Lambda access point a request
// is addressed to. A client reaches one at
// <alias>.s3-object-lambda.<region>.amazonaws.com, and the alias carries the
// name and account, so the host is what identifies the access point.
func s3ObjectLambdaHostAccessPoint(host string) (S3ObjectLambdaAccessPoint, bool) {
	label := host
	if i := strings.Index(label, ":"); i >= 0 {
		label = label[:i]
	}
	i := strings.Index(label, ".")
	if i < 0 {
		return S3ObjectLambdaAccessPoint{}, false
	}
	alias := label[:i]
	j := strings.LastIndex(alias, "-")
	if j < 0 {
		return S3ObjectLambdaAccessPoint{}, false
	}
	name, account := alias[:j], alias[j+1:]
	olap, ok := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name))
	return olap, ok
}

// s3ObjectLambdaGetObject serves a GetObject addressed to an Object Lambda
// access point. It hands the transformation function a route token and the
// URL it should read the original object from, then waits for the function to
// answer on that token through WriteGetObjectResponse. What the function wrote
// is what the caller receives; the stored object is never returned directly.
func s3ObjectLambdaGetObject(w http.ResponseWriter, r *http.Request, olap S3ObjectLambdaAccessPoint) {
	fnARN, ok := s3ObjectLambdaTransformFor(olap, "GetObject")
	if !ok {
		s3ObjectLambdaError(w, r, "InvalidRequest",
			"The Object Lambda access point has no GetObject transformation", http.StatusBadRequest)
		return
	}
	fn, ok := lambdaFunctions.Get(ebLambdaNameFromARN(fnARN))
	if !ok {
		s3ObjectLambdaError(w, r, "NoSuchLambdaFunction",
			"The transformation function "+fnARN+" does not exist", http.StatusNotFound)
		return
	}

	route := s3ObjectLambdaID()
	token := s3ObjectLambdaID()
	reply := make(chan s3ObjectLambdaResult, 1)
	s3ObjectLambdaRoutesMu.Lock()
	s3ObjectLambdaRoutes[route] = reply
	s3ObjectLambdaRoutesMu.Unlock()
	defer func() {
		s3ObjectLambdaRoutesMu.Lock()
		delete(s3ObjectLambdaRoutes, route)
		s3ObjectLambdaRoutesMu.Unlock()
	}()

	payload, err := json.Marshal(s3ObjectLambdaEvent(r, olap, route, token))
	if err != nil {
		s3ObjectLambdaError(w, r, "InternalError", "could not build the transformation event: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// The function answers out of band, on the route, so the invoke runs
	// alongside the wait rather than before it.
	invokeErr := make(chan error, 1)
	go func() {
		out, handled, status := invokeLambdaViaRuntimeAPI(fn, payload)
		if !handled {
			invokeErr <- fmt.Errorf("the transformation function did not run")
			return
		}
		if status >= 300 {
			invokeErr <- fmt.Errorf("the transformation function failed: %s", strings.TrimSpace(string(out)))
			return
		}
		invokeErr <- nil
	}()

	select {
	case res := <-reply:
		for k, vs := range res.headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(res.status)
		_, _ = w.Write(res.body)
	case err := <-invokeErr:
		// The function returned without writing a response. Whatever it did,
		// there are no bytes to serve, so the read fails rather than falling
		// back to the untransformed object.
		if err == nil {
			err = fmt.Errorf("the transformation function returned without calling WriteGetObjectResponse")
		}
		s3ObjectLambdaError(w, r, "LambdaResponseNotReceived", err.Error(), http.StatusBadGateway)
	case <-r.Context().Done():
	}
}

// s3ObjectLambdaTransformFor finds the function that transforms one action.
func s3ObjectLambdaTransformFor(olap S3ObjectLambdaAccessPoint, action string) (string, bool) {
	for _, tc := range olap.Configuration.TransformationConfigurations {
		for _, a := range tc.Actions {
			if a == action {
				return tc.Content.AwsLambda.FunctionArn, true
			}
		}
	}
	return "", false
}

// s3ObjectLambdaEvent builds the s3-object-lambda event the transformation
// function receives: the route and token it answers on, and a presigned-style
// URL for the original object on the supporting access point's bucket.
func s3ObjectLambdaEvent(r *http.Request, olap S3ObjectLambdaAccessPoint, route, token string) map[string]any {
	supporting := olap.Configuration.SupportingAccessPoint
	if i := strings.LastIndex(supporting, "accesspoint/"); i >= 0 {
		supporting = supporting[i+len("accesspoint/"):]
	}
	bucket := ""
	if ap, ok := s3AccessPoints.Get(s3AccessPointKey(olap.AccountID, supporting)); ok {
		bucket = ap.Bucket
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	return map[string]any{
		"xAmzRequestId": s3ObjectLambdaID(),
		"getObjectContext": map[string]any{
			"inputS3Url":  s3ObjectLambdaInputURL(bucket, key),
			"outputRoute": route,
			"outputToken": token,
		},
		"configuration": map[string]any{
			"accessPointArn":           s3ObjectLambdaARN(olap.AccountID, olap.Name),
			"supportingAccessPointArn": s3AccessPointARN(olap.AccountID, supporting),
			"payload":                  s3ObjectLambdaPayload(olap),
		},
		"userRequest": map[string]any{
			"url":     r.URL.String(),
			"headers": s3ObjectLambdaHeaderMap(r.Header),
		},
		"userIdentity": map[string]any{
			"type":        "AssumedRole",
			"accountId":   olap.AccountID,
			"accessKeyId": "",
		},
		"protocolVersion": "1.00",
	}
}

// s3ObjectLambdaInputURL is the URL the transformation function reads the
// original object from. Real S3 hands the function a presigned URL for the
// object on the supporting access point's bucket; here that object lives on
// this simulator's own S3 surface, so the URL differs from AWS's only in the
// coordinate it points at — and it uses the address a function container can
// actually reach the simulator on, the same one its Runtime API arrives on.
func s3ObjectLambdaInputURL(bucket, key string) string {
	host, err := workloadCallbackHost()
	if err != nil {
		return ""
	}
	port, err := simHostMetadataPort()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("http://%s/%s/%s", net.JoinHostPort(host, strconv.Itoa(port)), bucket, key)
}

func s3ObjectLambdaPayload(olap S3ObjectLambdaAccessPoint) string {
	for _, tc := range olap.Configuration.TransformationConfigurations {
		if tc.Content.AwsLambda.FunctionPayload != "" {
			return tc.Content.AwsLambda.FunctionPayload
		}
	}
	return ""
}

func s3ObjectLambdaHeaderMap(h http.Header) map[string]string {
	out := map[string]string{}
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}

// handleS3WriteGetObjectResponse receives what a transformation function
// produced and hands it to the GetObject still waiting on that route. A write
// on a route nobody is waiting on is rejected: the token is what proves the
// caller is the function this request was routed to.
func handleS3WriteGetObjectResponse(w http.ResponseWriter, r *http.Request) {
	route := r.Header.Get("x-amz-request-route")
	token := r.Header.Get("x-amz-request-token")
	if route == "" || token == "" {
		s3ObjectLambdaError(w, r, "InvalidRequest",
			"x-amz-request-route and x-amz-request-token are required", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3ObjectLambdaError(w, r, "IncompleteBody", "could not read the response body", http.StatusBadRequest)
		return
	}

	s3ObjectLambdaRoutesMu.Lock()
	reply, ok := s3ObjectLambdaRoutes[route]
	s3ObjectLambdaRoutesMu.Unlock()
	if !ok {
		s3ObjectLambdaError(w, r, "NoSuchRequestRoute",
			"The request route is not associated with an in-flight request", http.StatusNotFound)
		return
	}

	status := http.StatusOK
	if s := r.Header.Get("x-amz-fwd-status"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			status = n
		}
	}
	headers := http.Header{}
	for k, vs := range r.Header {
		// The function forwards response headers by prefixing them, which is
		// how it sets Content-Type and the rest on the caller's response.
		if name, cut := strings.CutPrefix(strings.ToLower(k), "x-amz-fwd-header-"); cut {
			for _, v := range vs {
				headers.Add(name, v)
			}
		}
	}
	if headers.Get("Content-Length") == "" {
		headers.Set("Content-Length", strconv.Itoa(len(body)))
	}

	select {
	case reply <- s3ObjectLambdaResult{status: status, headers: headers, body: body}:
	default:
		s3ObjectLambdaError(w, r, "NoSuchRequestRoute",
			"The request route has already been written to", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// s3ObjectLambdaID mints one of the opaque identifiers the Object Lambda flow
// hands out — a route, a token, a request id. Each has to be unguessable: the
// token is what proves a WriteGetObjectResponse comes from the function the
// read was routed to.
func s3ObjectLambdaID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// s3ServeObjectLambdaRead claims a read addressed to an Object Lambda access
// point. A client reaches one by its own hostname, so the host — not the path
// — is what says this read runs through a transformation. The whole path is
// the object key: the access point's configuration supplies the bucket.
func s3ServeObjectLambdaRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	olap, ok := s3ObjectLambdaHostAccessPoint(r.Host)
	if !ok {
		return false
	}
	s3ObjectLambdaGetObject(w, r, olap)
	return true
}
