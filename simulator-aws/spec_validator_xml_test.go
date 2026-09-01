package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/rs/zerolog"
)

// The XML wire-shape engine's own regression net: known-good documents
// pass, invented elements and type mismatches flag, the protocol
// envelopes (awsQuery Result wrapper, flat ec2Query, restXml payload
// roots) parse per the vendored Smithy models.

var (
	specSetOnce sync.Once
	specSetVal  *smithySpecSet
	specSetErr  error
)

func testSpecState(t *testing.T) *specValidatorState {
	t.Helper()
	specSetOnce.Do(func() {
		specSetVal, specSetErr = loadSmithySpecSet("../specs/cloud-api/aws")
	})
	if specSetErr != nil {
		t.Fatalf("load vendored Smithy models: %v", specSetErr)
	}
	mux := http.NewServeMux()
	noop := func(w http.ResponseWriter, r *http.Request) {}
	// The restXml identification path resolves the mux pattern that
	// served the request; mirror the simulator's S3/CloudFront/Route 53
	// registrations.
	mux.HandleFunc("GET /{$}", noop)
	mux.HandleFunc("GET /{bucket}", noop)
	mux.HandleFunc("GET /{bucket}/{key...}", noop)
	mux.HandleFunc("PUT /{bucket}/{key...}", noop)
	mux.HandleFunc("POST /{bucket}", noop)
	mux.HandleFunc("GET /2020-05-31/distribution/{id}", noop)
	mux.HandleFunc("GET /2013-04-01/hostedzone", noop)
	return &specValidatorState{spec: specSetVal, mux: mux, logger: zerolog.Nop()}
}

func violationKeys(violations []sim.SpecViolation) []string {
	var out []string
	for _, v := range violations {
		out = append(out, v.Kind+" "+v.Field)
	}
	return out
}

func assertViolations(t *testing.T, got []sim.SpecViolation, want []string) {
	t.Helper()
	keys := violationKeys(got)
	if len(keys) != len(want) {
		t.Fatalf("got %d violation(s) %v, want %v\nfull: %+v", len(keys), keys, want, got)
	}
	for i, w := range want {
		if keys[i] != w {
			t.Fatalf("violation %d = %q, want %q\nfull: %+v", i, keys[i], w, got)
		}
	}
}

func TestXMLValidatorQueryProtocols(t *testing.T) {
	xmlHeader := http.Header{"Content-Type": []string{"text/xml"}}

	cases := []struct {
		name     string
		form     string
		respBody string
		want     []string // "kind field" in emission order
	}{
		{
			name: "iam CreateRole known-good",
			form: "Action=CreateRole&Version=2010-05-08",
			respBody: `<CreateRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <CreateRoleResult>
    <Role>
      <Path>/</Path>
      <RoleName>test-role</RoleName>
      <RoleId>AROA123</RoleId>
      <Arn>arn:aws:iam::123456789012:role/test-role</Arn>
      <CreateDate>2026-06-11T00:00:00Z</CreateDate>
      <AssumeRolePolicyDocument>%7B%7D</AssumeRolePolicyDocument>
      <MaxSessionDuration>3600</MaxSessionDuration>
      <Tags><member><Key>env</Key><Value>test</Value></member></Tags>
    </Role>
  </CreateRoleResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</CreateRoleResponse>`,
		},
		{
			name: "iam CreateRole invented element flags",
			form: "Action=CreateRole&Version=2010-05-08",
			respBody: `<CreateRoleResponse>
  <CreateRoleResult>
    <Role>
      <RoleName>test-role</RoleName>
      <FavoriteColor>blue</FavoriteColor>
    </Role>
  </CreateRoleResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</CreateRoleResponse>`,
			want: []string{"unknown-field $.Role.FavoriteColor"},
		},
		{
			name: "iam CreateRole numeric type mismatch flags",
			form: "Action=CreateRole&Version=2010-05-08",
			respBody: `<CreateRoleResponse>
  <CreateRoleResult>
    <Role>
      <RoleName>test-role</RoleName>
      <MaxSessionDuration>one-hour</MaxSessionDuration>
    </Role>
  </CreateRoleResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</CreateRoleResponse>`,
			want: []string{"type-mismatch $.Role.MaxSessionDuration"},
		},
		{
			name: "awsQuery envelope rejects stray sibling of Result",
			form: "Action=GetRole&Version=2010-05-08",
			respBody: `<GetRoleResponse>
  <GetRoleResult><Role><RoleName>r</RoleName></Role></GetRoleResult>
  <Extra>1</Extra>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</GetRoleResponse>`,
			want: []string{"unknown-field $.Extra"},
		},
		{
			name:     "awsQuery wrong root element flags envelope",
			form:     "Action=GetRole&Version=2010-05-08",
			respBody: `<GetRoleResult><Role><RoleName>r</RoleName></Role></GetRoleResult>`,
			want:     []string{"envelope $"},
		},
		{
			name: "ec2Query flat envelope with xmlName members and item list",
			form: "Action=DescribeInstances&Version=2016-11-15",
			respBody: `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
  <requestId>req-1</requestId>
  <reservationSet>
    <item>
      <reservationId>r-1</reservationId>
      <ownerId>123456789012</ownerId>
      <instancesSet>
        <item>
          <instanceId>i-1</instanceId>
          <imageId>ami-1</imageId>
          <instanceState><code>16</code><name>running</name></instanceState>
        </item>
      </instancesSet>
    </item>
  </reservationSet>
</DescribeInstancesResponse>`,
		},
		{
			name: "ec2Query list items must use the model's wrapper name",
			form: "Action=DescribeInstances&Version=2016-11-15",
			respBody: `<DescribeInstancesResponse>
  <requestId>req-1</requestId>
  <reservationSet>
    <member><reservationId>r-1</reservationId></member>
  </reservationSet>
</DescribeInstancesResponse>`,
			want: []string{"unknown-field $.Reservations.member"},
		},
		{
			name: "ec2Query PascalCase member name flags (wire uses xmlName)",
			form: "Action=DescribeInstances&Version=2016-11-15",
			respBody: `<DescribeInstancesResponse>
  <requestId>req-1</requestId>
  <Reservations><item><reservationId>r-1</reservationId></item></Reservations>
</DescribeInstancesResponse>`,
			want: []string{"unknown-field $.Reservations"},
		},
		{
			name: "ec2Query unit output accepts requestId and return ack",
			form: "Action=DeleteVpc&Version=2016-11-15",
			respBody: `<DeleteVpcResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
  <requestId>req-1</requestId>
  <return>true</return>
</DeleteVpcResponse>`,
		},
		{
			name: "ec2Query unit output rejects invented members",
			form: "Action=DeleteVpc&Version=2016-11-15",
			respBody: `<DeleteVpcResponse>
  <requestId>req-1</requestId>
  <vpcId>vpc-1</vpcId>
</DeleteVpcResponse>`,
			want: []string{"unknown-field $.vpcId"},
		},
		{
			name: "versioned action resolves the right model (RDS vs ElastiCache)",
			form: "Action=ListTagsForResource&Version=2014-10-31",
			respBody: `<ListTagsForResourceResponse>
  <ListTagsForResourceResult>
    <TagList><Tag><Key>k</Key><Value>v</Value></Tag></TagList>
  </ListTagsForResourceResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</ListTagsForResourceResponse>`,
		},
		{
			name: "sns map members serialize as entry/key/value",
			form: "Action=GetTopicAttributes&Version=2010-03-31",
			respBody: `<GetTopicAttributesResponse>
  <GetTopicAttributesResult>
    <Attributes>
      <entry><key>TopicArn</key><value>arn:aws:sns:us-east-1:123456789012:t</value></entry>
      <pair><key>DisplayName</key><value>t</value></pair>
    </Attributes>
  </GetTopicAttributesResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</GetTopicAttributesResponse>`,
			want: []string{"unknown-field $.Attributes.pair"},
		},
		{
			name:     "non-XML success body flags",
			form:     "Action=GetRole&Version=2010-05-08",
			respBody: `{"Role":{}}`,
			want:     []string{"malformed-xml $"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := testSpecState(t)
			header := xmlHeader
			if strings.HasPrefix(tc.respBody, "{") {
				header = http.Header{"Content-Type": []string{"application/json"}}
			}
			got := st.validateQueryAction([]byte(tc.form), header, []byte(tc.respBody))
			assertViolations(t, got, tc.want)
		})
	}
}

func TestXMLValidatorRestXML(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		url      string
		respBody string
		want     []string
	}{
		{
			name:   "s3 ListObjectsV2 flattened Contents passes",
			method: "GET", url: "/my-bucket?list-type=2",
			respBody: `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>my-bucket</Name>
  <Prefix></Prefix>
  <KeyCount>1</KeyCount>
  <MaxKeys>1000</MaxKeys>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>a.txt</Key>
    <LastModified>2026-06-11T00:00:00.000Z</LastModified>
    <ETag>&quot;abc&quot;</ETag>
    <Size>3</Size>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
</ListBucketResult>`,
		},
		{
			name:   "s3 ListObjectsV2 invented member flags",
			method: "GET", url: "/my-bucket?list-type=2",
			respBody: `<ListBucketResult>
  <Name>my-bucket</Name>
  <ObjectCount>1</ObjectCount>
</ListBucketResult>`,
			want: []string{"unknown-field $.ObjectCount"},
		},
		{
			name:   "s3 ListObjectsV2 numeric member with text flags",
			method: "GET", url: "/my-bucket?list-type=2",
			respBody: `<ListBucketResult>
  <Name>my-bucket</Name>
  <KeyCount>lots</KeyCount>
</ListBucketResult>`,
			want: []string{"type-mismatch $.KeyCount"},
		},
		{
			name:   "s3 ListBuckets wrapped list passes (output shape xmlName root)",
			method: "GET", url: "/",
			respBody: `<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Owner><ID>owner</ID><DisplayName>owner</DisplayName></Owner>
  <Buckets>
    <Bucket><Name>b1</Name><CreationDate>2026-06-11T00:00:00Z</CreationDate></Bucket>
  </Buckets>
</ListAllMyBucketsResult>`,
		},
		{
			name:   "s3 GetBucketLocation member fusion accepted",
			method: "GET", url: "/my-bucket?location",
			respBody: `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">eu-west-1</LocationConstraint>`,
		},
		{
			name:   "s3 GetBucketTagging root takes the output shape xmlName",
			method: "GET", url: "/my-bucket?tagging",
			respBody: `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`,
		},
		{
			name:   "s3 GetBucketTagging wrong root flags envelope",
			method: "GET", url: "/my-bucket?tagging",
			respBody: `<GetBucketTaggingOutput><TagSet/></GetBucketTaggingOutput>`,
			want:     []string{"envelope $"},
		},
		{
			name:   "cloudfront GetDistribution httpPayload root, header members excluded",
			method: "GET", url: "/2020-05-31/distribution/E123",
			respBody: `<Distribution xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Id>E123</Id>
  <ARN>arn:aws:cloudfront::123456789012:distribution/E123</ARN>
  <Status>Deployed</Status>
  <LastModifiedTime>2026-06-11T00:00:00Z</LastModifiedTime>
  <InProgressInvalidationBatches>0</InProgressInvalidationBatches>
  <DomainName>d123.cloudfront.net</DomainName>
</Distribution>`,
		},
		{
			name:   "cloudfront GetDistribution ETag is header-bound, body ETag flags",
			method: "GET", url: "/2020-05-31/distribution/E123",
			respBody: `<Distribution>
  <Id>E123</Id>
  <ETag>E2QWRUHAPOMQZL</ETag>
</Distribution>`,
			want: []string{"unknown-field $.ETag"},
		},
		{
			name:   "route53 ListHostedZones wrapped HostedZone items pass",
			method: "GET", url: "/2013-04-01/hostedzone",
			respBody: `<ListHostedZonesResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <HostedZones>
    <HostedZone>
      <Id>/hostedzone/Z1</Id>
      <Name>example.com.</Name>
      <CallerReference>ref-1</CallerReference>
    </HostedZone>
  </HostedZones>
  <IsTruncated>false</IsTruncated>
  <MaxItems>100</MaxItems>
</ListHostedZonesResponse>`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := testSpecState(t)
			req := httptest.NewRequest(tc.method, tc.url, nil)
			header := http.Header{"Content-Type": []string{"application/xml"}}
			got := st.validateRestXML(req, http.StatusOK, header, []byte(tc.respBody))
			assertViolations(t, got, tc.want)
		})
	}
}

// TestXMLValidatorRestXMLOpSelection pins the query-literal and
// required-header disambiguation: shared mux patterns resolve to distinct
// operations by the smithy URI's query literals (x-id required only when
// sent) and by required header-bound members (x-amz-copy-source separates
// UploadPartCopy from UploadPart).
func TestXMLValidatorRestXMLOpSelection(t *testing.T) {
	st := testSpecState(t)
	cases := []struct {
		method, url, wantOp string
		headers             map[string]string
	}{
		{"GET", "/my-bucket?list-type=2", "ListObjectsV2", nil},
		{"GET", "/my-bucket?list-type=2&x-id=ListObjectsV2", "ListObjectsV2", nil},
		{"GET", "/my-bucket", "ListObjects", nil},
		{"GET", "/my-bucket?location", "GetBucketLocation", nil},
		{"GET", "/my-bucket?tagging", "GetBucketTagging", nil},
		{"POST", "/my-bucket?delete", "DeleteObjects", nil},
		{"GET", "/", "ListBuckets", nil},
		{"GET", "/my-bucket/some/key.txt", "GetObject", nil},
		// UploadPart vs UploadPartCopy share PUT /{bucket}/{key+} and the
		// same required query (uploadId, partNumber); the required
		// x-amz-copy-source header selects the copy variant.
		{"PUT", "/my-bucket/key.txt?uploadId=u&partNumber=1", "UploadPart", nil},
		{"PUT", "/my-bucket/key.txt?uploadId=u&partNumber=1", "UploadPartCopy",
			map[string]string{"x-amz-copy-source": "/src-bucket/src-key"}},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.url, nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		_, pattern := st.mux.Handler(req)
		method, path, _ := strings.Cut(pattern, " ")
		key := method + " " + normalizeAWSPath(strings.TrimSuffix(path, "{$}"))
		cands := st.spec.restXMLOps[key]
		if len(cands) == 0 {
			cands = st.spec.restXMLOps[strings.ReplaceAll(key, "{+}", "{}")]
		}
		op := selectRestXMLOp(cands, req.URL.Query(), req.Header)
		if op == nil {
			t.Errorf("%s %s: no operation selected (pattern %q, key %q)", tc.method, tc.url, pattern, key)
			continue
		}
		if op.name != tc.wantOp {
			t.Errorf("%s %s: selected %s, want %s", tc.method, tc.url, op.name, tc.wantOp)
		}
	}
}

// TestXMLValidatorTimestamps pins the accepted XML timestamp encodings.
func TestXMLValidatorTimestamps(t *testing.T) {
	valid := []string{"2026-06-11T00:00:00Z", "2026-06-11T00:00:00.123Z", "Wed, 11 Jun 2026 00:00:00 GMT", "1760000000", "1760000000.5"}
	invalid := []string{"", "yesterday", "2026-06-11"}
	for _, s := range valid {
		if !validXMLTimestamp(s) {
			t.Errorf("validXMLTimestamp(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if validXMLTimestamp(s) {
			t.Errorf("validXMLTimestamp(%q) = true, want false", s)
		}
	}
}

func TestParseXMLTree(t *testing.T) {
	root, err := parseXMLTree([]byte(`<?xml version="1.0"?><A x="1"><B>text</B><B>more</B><C/></A>`))
	if err != nil {
		t.Fatalf("parseXMLTree: %v", err)
	}
	if root.name != "A" || len(root.children) != 3 {
		t.Fatalf("root = %s with %d children, want A with 3", root.name, len(root.children))
	}
	if root.children[0].text != "text" || root.children[1].text != "more" {
		t.Fatalf("child text = %q/%q", root.children[0].text, root.children[1].text)
	}
	if _, err := parseXMLTree([]byte(`not xml`)); err == nil {
		t.Fatal("parseXMLTree accepted a non-XML body")
	}
	if _, err := parseXMLTree([]byte(`<A></A><B></B>`)); err == nil {
		t.Fatal("parseXMLTree accepted multiple roots")
	}
}
