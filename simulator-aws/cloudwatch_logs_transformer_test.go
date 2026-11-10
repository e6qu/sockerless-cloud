package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cwtCompileJSON(t *testing.T, config string) (*cwTransformer, error) {
	t.Helper()
	var procs []cwtProcessor
	require.NoError(t, json.Unmarshal([]byte(config), &procs), "config %s", config)
	return cwCompileTransformer(procs)
}

func cwtRun(t *testing.T, config, message string, meta map[string]string) string {
	t.Helper()
	tr, err := cwtCompileJSON(t, config)
	require.NoError(t, err, "config %s", config)
	res := tr.Transform(message, meta)
	require.NoError(t, res.Err, "message %q", message)
	return res.Transformed
}

// The examples of the CloudWatch Logs guide's transformation pages
// (specs/cloud-api/aws/logs-cloudwatch-logs-transformation-*.md.gz), each
// input with its documented output.
func TestCWTransformerGuideExamples(t *testing.T) {
	cases := []struct {
		name, config, input, want string
	}{
		{"parseJSON destination",
			`[{"parseJSON":{"destination":"new_key"}}]`,
			`{"outer_key":{"inner_key":"inner_value"}}`,
			`{"new_key":{"outer_key":{"inner_key":"inner_value"}}}`},
		{"lowerCaseString",
			`[{"parseJSON":{}},{"lowerCaseString":{"withKeys":["outer_key.inner_key"]}}]`,
			`{"outer_key":{"inner_key":"INNER_VALUE"}}`,
			`{"outer_key":{"inner_key":"inner_value"}}`},
		{"upperCaseString",
			`[{"parseJSON":{}},{"upperCaseString":{"withKeys":["outer_key.inner_key"]}}]`,
			`{"outer_key":{"inner_key":"inner_value"}}`,
			`{"outer_key":{"inner_key":"INNER_VALUE"}}`},
		{"splitString",
			`[{"parseJSON":{}},{"splitString":{"entries":[{"source":"outer_key.inner_key","delimiter":"_"}]}}]`,
			`{"outer_key":{"inner_key":"inner_value"}}`,
			`{"outer_key":{"inner_key":["inner","value"]}}`},
		{"splitString multi-character delimiter",
			`[{"parseJSON":{}},{"splitString":{"entries":[{"source":"outer_key.inner_key","delimiter":", "}]}}]`,
			`{"outer_key":{"inner_key":"item1, item2, item3"}}`,
			`{"outer_key":{"inner_key":["item1","item2","item3"]}}`},
		{"substituteString",
			`[{"parseJSON":{}},{"substituteString":{"entries":[
				{"source":"outer_key.inner_key1","from":"\\[\\]","to":"value1"},
				{"source":"outer_key.inner_key2","from":"[0-9]{3}-[0-9]{3}-[0-9]{3}","to":"xxx-xxx-xxx"},
				{"source":"outer_key.inner_key3","from":"cat","to":"dog"}]}}]`,
			`{"outer_key":{"inner_key1":"[]","inner_key2":"123-345-567","inner_key3":"A cat takes a catnap."}}`,
			`{"outer_key":{"inner_key1":"value1","inner_key2":"xxx-xxx-xxx","inner_key3":"A dog takes a dognap."}}`},
		{"substituteString groups",
			`[{"parseJSON":{}},{"substituteString":{"entries":[
				{"source":"outer_key.inner_key1","from":"(\\w+), (\\w+), and (\\w+)","to":"$1 and $3"},
				{"source":"outer_key.inner_key2","from":"^arn:aws:sts::(?P<account_id>\\d{12}):assumed-role/(?P<role_name>[\\w+=,.@-]+)/(?P<role_session_name>[\\w+=,.@-]+)$","to":"${account_id}:${role_name}:${role_session_name}"}]}}]`,
			`{"outer_key":{"inner_key1":"Tom, Dick, and Harry","inner_key2":"arn:aws:sts::123456789012:assumed-role/MyImportantRole/MySession"}}`,
			`{"outer_key":{"inner_key1":"Tom and Harry","inner_key2":"123456789012:MyImportantRole:MySession"}}`},
		{"trimString",
			`[{"parseJSON":{}},{"trimString":{"withKeys":["outer_key.inner_key"]}}]`,
			`{"outer_key":{"inner_key":"   inner_value  "}}`,
			`{"outer_key":{"inner_key":"inner_value"}}`},
		{"addKeys",
			`[{"parseJSON":{}},{"addKeys":{"entries":[{"key":"outer_key.new_key","value":"new_value"}]}}]`,
			`{"outer_key":{"inner_key":"inner_value"}}`,
			`{"outer_key":{"inner_key":"inner_value","new_key":"new_value"}}`},
		{"deleteKeys",
			`[{"parseJSON":{}},{"deleteKeys":{"withKeys":["outer_key.inner_key"]}}]`,
			`{"outer_key":{"inner_key":"inner_value"}}`,
			`{"outer_key":{}}`},
		{"moveKeys",
			`[{"parseJSON":{}},{"moveKeys":{"entries":[{"source":"outer_key1.inner_key1","target":"outer_key2"}]}}]`,
			`{"outer_key1":{"inner_key1":"inner_value1"},"outer_key2":{"inner_key2":"inner_value2"}}`,
			`{"outer_key1":{},"outer_key2":{"inner_key2":"inner_value2","inner_key1":"inner_value1"}}`},
		{"renameKeys",
			`[{"parseJSON":{}},{"renameKeys":{"entries":[{"key":"outer_key","renameTo":"new_key"}]}}]`,
			`{"outer_key":{"inner_key":"inner_value"}}`,
			`{"new_key":{"inner_key":"inner_value"}}`},
		{"listToMap",
			`[{"parseJSON":{}},{"listToMap":{"source":"outer_key","key":"inner_key","valueKey":"inner_value","flatten":false}}]`,
			cwtListToMapInput,
			`{"outer_key":` + cwtListToMapList + `,"a":["val-a"],"b":["val-b1","val-b2"],"c":["val-c"]}`},
		{"listToMap flatten first",
			`[{"parseJSON":{}},{"listToMap":{"source":"outer_key","key":"inner_key","valueKey":"inner_value","flatten":true,"flattenedElement":"first"}}]`,
			cwtListToMapInput,
			`{"outer_key":` + cwtListToMapList + `,"a":"val-a","b":"val-b1","c":"val-c"}`},
		{"listToMap flatten last",
			`[{"parseJSON":{}},{"listToMap":{"source":"outer_key","key":"inner_key","valueKey":"inner_value","flatten":true,"flattenedElement":"last"}}]`,
			cwtListToMapInput,
			`{"outer_key":` + cwtListToMapList + `,"a":"val-a","b":"val-b2","c":"val-c"}`},
		{"typeConverter",
			`[{"parseJSON":{}},{"typeConverter":{"entries":[{"key":"status","type":"integer"}]}}]`,
			`{"name":"value","status":"200"}`,
			`{"name":"value","status":200}`},
		{"grok example 1",
			`[{"grok":{"match":"%{NUMBER:version} %{HOSTNAME:hostname} %{NOTSPACE:status} %{QUOTEDSTRING:logMsg}"}}]`,
			`293750 server-01.internal-network.local OK "[Thread-000] token generated"`,
			`{"version":"293750","hostname":"server-01.internal-network.local","status":"OK","logMsg":"[Thread-000] token generated"}`},
		{"grok example 1 HTTPDATE",
			`[{"grok":{"match":"%{HTTPDATE:timestamp} %{IPORHOST:clientip} %{NUMBER:response_status}"}}]`,
			`23/Nov/2024:10:25:15 -0900 172.16.0.1 200`,
			`{"timestamp":"23/Nov/2024:10:25:15 -0900","clientip":"172.16.0.1","response_status":"200"}`},
		{"grok example 2 after parseJSON",
			`[{"parseJSON":{}},{"grok":{"source":"logMsg","match":"%{WORD:http_method} %{NOTSPACE:request} HTTP/%{NUMBER:http_version}"}}]`,
			`{"timestamp":"2024-11-23T16:03:12Z","level":"ERROR","logMsg":"GET /page.html HTTP/1.1"}`,
			`{"timestamp":"2024-11-23T16:03:12Z","level":"ERROR","logMsg":"GET /page.html HTTP/1.1","http_method":"GET","request":"/page.html","http_version":"1.1"}`},
		{"grok example 3 dotted field names",
			`[{"grok":{"match":"%{IP:client.ip} %{WORD:method} %{URIPATHPARAM:request.uri} %{NUMBER:response.status} %{NUMBER:response.bytes}"}}]`,
			`192.168.1.1 GET /index.html?param=value 200 1234`,
			`{"client":{"ip":"192.168.1.1"},"method":"GET","request":{"uri":"/index.html?param=value"},"response":{"status":"200","bytes":"1234"}}`},
		{"grok Redis slow log",
			`[{"grok":{"match":"%{NUMBER:timestamp} [%{NUMBER:db} %{IP:client_ip}:%{NUMBER:client_port}] %{GREEDYDATA:data}"}}]`,
			`1629860738.123456 [0 127.0.0.1:6379] "SET" "key1" "value1"`,
			`{"timestamp":"1629860738.123456","db":"0","client_ip":"127.0.0.1","client_port":"6379","data":"\"SET\" \"key1\" \"value1\""}`},
		{"grok APACHE_ACCESS_LOG",
			`[{"grok":{"match":"%{APACHE_ACCESS_LOG}"}}]`,
			`127.0.0.1 - - [03/Aug/2023:12:34:56 +0000] "GET /page.html HTTP/1.1" 200 1234`,
			`{"request":"/page.html","http_method":"GET","status_code":200,"http_version":"1.1","response_size":1234,"remote_host":"127.0.0.1","timestamp":"2023-08-03T12:34:56Z"}`},
		{"grok NGINX_ACCESS_LOG",
			`[{"grok":{"match":"%{NGINX_ACCESS_LOG}"}}]`,
			`192.168.1.100 - Foo [03/Aug/2023:12:34:56 +0000] "GET /account/login.html HTTP/1.1" 200 42 "https://www.amazon.com/" "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/92.0.4515.131 Safari/537.36"`,
			`{"request":"/account/login.html","referrer":"https://www.amazon.com/","agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/92.0.4515.131 Safari/537.36","http_method":"GET","status_code":200,"auth_user":"Foo","http_version":"1.1","response_size":42,"remote_host":"192.168.1.100","timestamp":"2023-08-03T12:34:56Z"}`},
		{"grok SYSLOG5424",
			`[{"grok":{"match":"%{SYSLOG5424}"}}]`,
			`<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3" eventSource= "Application" eventID="1011"][examplePriority@32473 class="high"]`,
			`{"pri":165,"version":1,"timestamp":"2003-10-11T22:14:15.003Z","hostname":"mymachine.example.com","app":"evntslog","msg_id":"ID47","structured_data":"exampleSDID@32473 iut=\"3\" eventSource= \"Application\" eventID=\"1011\"","message":"[examplePriority@32473 class=\"high\"]"}`},
		{"csv",
			`[{"csv":{"delimiter":":","quoteCharacter":"'"}}]`,
			`'Akua Mansa':28:'New York: USA'`,
			`{"column_1":"Akua Mansa","column_2":"28","column_3":"New York: USA"}`},
		{"csv columns and destination",
			`[{"parseJSON":{}},{"csv":{"source":"logMsg","delimiter":":","quoteCharacter":"'","columns":["name","age","location"],"destination":"msg"}}]`,
			`{"timestamp":"2024-11-23T16:03:12Z","type":"user_data","logMsg":"'Akua Mansa':28:'New York: USA'"}`,
			`{"timestamp":"2024-11-23T16:03:12Z","logMsg":"'Akua Mansa':28:'New York: USA'","type":"user_data","msg":{"name":"Akua Mansa","age":"28","location":"New York: USA"}}`},
		{"parseKeyValue",
			`[{"parseKeyValue":{"destination":"new_key","fieldDelimiter":"!","keyValueDelimiter":":","nonMatchValue":"defaultValue","keyPrefix":"parsed_"}}]`,
			`key1:value1!key2:value2!key3:value3!key4`,
			`{"new_key":{"parsed_key1":"value1","parsed_key2":"value2","parsed_key3":"value3","parsed_key4":"defaultValue"}}`},
		{"parseWAF",
			`[{"parseWAF":{}}]`,
			cwtWAFInput,
			cwtWAFOutput},
		{"parsePostgres",
			`[{"parsePostgres":{}}]`,
			`2019-03-10 03:54:59 UTC:10.0.0.123(52834):postgres@logtestdb:[20175]:ERROR: column "wrong_column_name" does not exist at character 8`,
			`{"logTime":"2019-03-10 03:54:59 UTC","srcIp":"10.0.0.123(52834)","userName":"postgres","dbName":"logtestdb","processId":"20175","logLevel":"ERROR"}`},
		{"parseCloudfront",
			`[{"parseCloudfront":{}}]`,
			"2019-12-04  21:02:31   LAX1   392    192.0.2.24    GET    d111111abcdef8.cloudfront.net  /index.html    200    -  Mozilla/5.0%20(Windows%20NT%2010.0;%20Win64;%20x64)%20AppleWebKit/537.36%20(KHTML,%20like%20Gecko)%20Chrome/78.0.3904.108%20Safari/537.36  -  -  Hit    SOX4xwn4XV6Q4rgb7XiVGOHms_BGlTAC4KyHmureZmBNrjGdRLiNIQ==   d111111abcdef8.cloudfront.net  https  23 0.001  -  TLSv1.2    ECDHE-RSA-AES128-GCM-SHA256    Hit    HTTP/2.0   -  -  11040  0.001  Hit    text/html  78 -  -",
			cwtCloudfrontOutput},
		{"parseRoute53",
			`[{"parseRoute53":{}}]`,
			`1.0 2017-12-13T08:15:50.235Z Z123412341234 example.com AAAA NOERROR TCP IAD12 192.0.2.0 198.51.100.0/24`,
			`{"version":1.0,"queryTimestamp":"2017-12-13T08:15:50.235Z","hostZoneId":"Z123412341234","queryName":"example.com","queryType":"AAAA","responseCode":"NOERROR","protocol":"TCP","edgeLocation":"IAD12","resolverIp":"192.0.2.0","ednsClientSubnet":"198.51.100.0/24"}`},
		{"parseVPC",
			`[{"parseVPC":{}}]`,
			`2 123456789010 eni-abc123de 192.0.2.0 192.0.2.24 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK`,
			`{"version":2,"accountId":"123456789010","interfaceId":"eni-abc123de","srcAddr":"192.0.2.0","dstAddr":"192.0.2.24","srcPort":20641,"dstPort":22,"protocol":6,"packets":20,"bytes":4249,"start":1418530010,"end":1418530070,"action":"ACCEPT","logStatus":"OK"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.JSONEq(t, c.want, cwtRun(t, c.config, c.input, nil))
		})
	}
}

const cwtListToMapList = `[{"inner_key":"a","inner_value":"val-a"},{"inner_key":"b","inner_value":"val-b1"},{"inner_key":"b","inner_value":"val-b2"},{"inner_key":"c","inner_value":"val-c"}]`

const cwtListToMapInput = `{"outer_key":` + cwtListToMapList + `}`

const cwtWAFInput = `{"timestamp":1576280412771,"formatVersion":1,"webaclId":"arn:aws:wafv2:ap-southeast-2:111122223333:regional/webacl/STMTest/1EXAMPLE-2ARN-3ARN-4ARN-123456EXAMPLE","terminatingRuleId":"STMTest_SQLi_XSS","terminatingRuleType":"REGULAR","action":"BLOCK","terminatingRuleMatchDetails":[{"conditionType":"SQL_INJECTION","sensitivityLevel":"HIGH","location":"HEADER","matchedData":["10","AND","1"]}],"httpSourceName":"-","httpSourceId":"-","ruleGroupList":[],"rateBasedRuleList":[],"nonTerminatingMatchingRules":[],"httpRequest":{"clientIp":"1.1.1.1","country":"AU","headers":[{"name":"Host","value":"localhost:1989"},{"name":"User-Agent","value":"curl/7.61.1"},{"name":"Accept","value":"*/*"},{"name":"x-stm-test","value":"10 AND 1=1"}],"uri":"/myUri","args":"","httpVersion":"HTTP/1.1","httpMethod":"GET","requestId":"rid"},"labels":[{"name":"value"}]}`

const cwtWAFOutput = `{"httpRequest":{"headers":{"Host":"localhost:1989","User-Agent":"curl/7.61.1","Accept":"*/*","x-stm-test":"10 AND 1=1"},"clientIp":"1.1.1.1","country":"AU","uri":"/myUri","args":"","httpVersion":"HTTP/1.1","httpMethod":"GET","requestId":"rid"},"labels":{"name":"value"},"timestamp":1576280412771,"formatVersion":1,"webaclId":"arn:aws:wafv2:ap-southeast-2:111122223333:regional/webacl/STMTest/1EXAMPLE-2ARN-3ARN-4ARN-123456EXAMPLE","terminatingRuleId":"STMTest_SQLi_XSS","terminatingRuleType":"REGULAR","action":"BLOCK","terminatingRuleMatchDetails":[{"conditionType":"SQL_INJECTION","sensitivityLevel":"HIGH","location":"HEADER","matchedData":["10","AND","1"]}],"httpSourceName":"-","httpSourceId":"-","ruleGroupList":[],"rateBasedRuleList":[],"nonTerminatingMatchingRules":[]}`

const cwtCloudfrontOutput = `{"date":"2019-12-04","time":"21:02:31","x-edge-location":"LAX1","sc-bytes":392,"c-ip":"192.0.2.24","cs-method":"GET","cs(Host)":"d111111abcdef8.cloudfront.net","cs-uri-stem":"/index.html","sc-status":200,"cs(Referer)":"-","cs(User-Agent)":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/78.0.3904.108 Safari/537.36","cs-uri-query":"-","cs(Cookie)":"-","x-edge-result-type":"Hit","x-edge-request-id":"SOX4xwn4XV6Q4rgb7XiVGOHms_BGlTAC4KyHmureZmBNrjGdRLiNIQ==","x-host-header":"d111111abcdef8.cloudfront.net","cs-protocol":"https","cs-bytes":23,"time-taken":0.001,"x-forwarded-for":"-","ssl-protocol":"TLSv1.2","ssl-cipher":"ECDHE-RSA-AES128-GCM-SHA256","x-edge-response-result-type":"Hit","cs-protocol-version":"HTTP/2.0","fle-status":"-","fle-encrypted-fields":"-","c-port":11040,"time-to-first-byte":0.001,"x-edge-detailed-result-type":"Hit","sc-content-type":"text/html","sc-content-len":78,"sc-range-start":"-","sc-range-end":"-"}`

func TestCWTransformerKeepsNumberText(t *testing.T) {
	out := cwtRun(t, `[{"parseRoute53":{}}]`,
		`1.0 2017-12-13T08:15:50.235Z Z123412341234 example.com AAAA NOERROR TCP IAD12 192.0.2.0 198.51.100.0/24`, nil)
	assert.True(t, strings.HasPrefix(out, `{"version":1.0,`), out)
}

// The supported-pattern table of the grok section, each example input with its
// output.
func TestCWTransformerGrokPatternTable(t *testing.T) {
	cases := []struct{ match, input, want string }{
		{"%{USERNAME:name}", "user123.name-TEST", `{"name":"user123.name-TEST"}`},
		{"%{USER:name}", "user123.name-TEST", `{"name":"user123.name-TEST"}`},
		{"%{INT:num}", "-456", `{"num":"-456"}`},
		{"%{BASE10NUM:num}", "-0.67", `{"num":"-0.67"}`},
		{"%{BASE16NUM:num}", "+0xA1B2", `{"num":"+0xA1B2"}`},
		{"%{POSINT:num}", "123", `{"num":"123"}`},
		{"%{NONNEGINT:num}", "007", `{"num":"007"}`},
		{"%{WORD:user}", "user_123", `{"user":"user_123"}`},
		{"%{NOTSPACE:msg}", "hello_world123", `{"msg":"hello_world123"}`},
		{"%{SPACE:extra}", " ", `{"extra":" "}`},
		{"%{DATA:x} %{DATA:y}", "abc def ghi", `{"x":"abc","y":"def ghi"}`},
		{"%{GREEDYDATA:x} %{GREEDYDATA:y}", "abc def ghi", `{"x":"abc def","y":"ghi"}`},
		{"%{GREEDYDATA_MULTILINE:data}", "abc\ndef\nghi", `{"data":"abc\ndef\nghi"}`},
		{"%{QUOTEDSTRING:msg}", `"Hello, world!"`, `{"msg":"Hello, world!"}`},
		{"%{UUID:id}", "550e8400-e29b-41d4-a716-446655440000", `{"id":"550e8400-e29b-41d4-a716-446655440000"}`},
		{"%{URN:urn}", "urn:isbn:0451450523", `{"urn":"urn:isbn:0451450523"}`},
		{"%{ARN:arn}", "arn:aws:iam:us-east-1:123456789012:user/johndoe", `{"arn":"arn:aws:iam:us-east-1:123456789012:user/johndoe"}`},
		{"%{CISCOMAC:MacAddress}", "0123.4567.89AB", `{"MacAddress":"0123.4567.89AB"}`},
		{"%{WINDOWSMAC:MacAddress}", "01-23-45-67-89-AB", `{"MacAddress":"01-23-45-67-89-AB"}`},
		{"%{COMMONMAC:MacAddress}", "01:23:45:67:89:AB", `{"MacAddress":"01:23:45:67:89:AB"}`},
		{"%{MAC:m1}", "01:23:45:67:89:AB", `{"m1":"01:23:45:67:89:AB"}`},
		{"%{IPV6:ip}", "2001:db8:3333:4444:5555:6666:7777:8888", `{"ip":"2001:db8:3333:4444:5555:6666:7777:8888"}`},
		{"%{IPV4:ip}", "192.168.0.1", `{"ip":"192.168.0.1"}`},
		{"%{IP:ip}", "192.168.0.1", `{"ip":"192.168.0.1"}`},
		{"%{HOST:host}", "server-01.internal-network.local", `{"host":"server-01.internal-network.local"}`},
		{"%{IPORHOST:ip}", "2001:db8:3333:4444:5555:6666:7777:8888", `{"ip":"2001:db8:3333:4444:5555:6666:7777:8888"}`},
		{"%{HOSTPORT:ip}", "192.168.1.1:8080", `{"ip":"192.168.1.1:8080","PORT":"8080"}`},
		{"%{URIHOST:host} %{URIHOST:ip}", "example.com:443 10.0.0.1", `{"host":"example.com:443","port":"443","ip":"10.0.0.1"}`},
		{"%{UNIXPATH:path}", "/search?q=regex", `{"path":"/search?q=regex"}`},
		{"%{WINPATH:path}", `C:\Users\John\Documents\file.txt`, `{"path":"C:\\Users\\John\\Documents\\file.txt"}`},
		{"%{PATH:path}", "/search?q=regex", `{"path":"/search?q=regex"}`},
		{"%{TTY:path}", "/dev/tty1", `{"path":"/dev/tty1"}`},
		{"%{URIPROTO:protocol}", "web+transformer", `{"protocol":"web+transformer"}`},
		{"%{URIPATH:path}", "/category/sub-category/product_name", `{"path":"/category/sub-category/product_name"}`},
		{"%{URIPARAM:url}", "?param1=value1&param2=value2", `{"url":"?param1=value1&param2=value2"}`},
		{"%{URIPATHPARAM:path}", "/category/sub-category/product?id=12345&color=red", `{"path":"/category/sub-category/product?id=12345&color=red"}`},
		{"%{URI:uri}", "https://user:password@example.com/path/to/resource?param1=value1&param2=value2", `{"uri":"https://user:password@example.com/path/to/resource?param1=value1&param2=value2"}`},
		{"%{MONTH:month}", "Jan", `{"month":"Jan"}`},
		{"%{MONTH:month}", "January", `{"month":"January"}`},
		{"%{MONTHNUM:month}", "5", `{"month":"5"}`},
		{"%{MONTHNUM:month}", "05", `{"month":"05"}`},
		{"%{MONTHNUM2:month}", "05", `{"month":"05"}`},
		{"%{MONTHDAY:monthDay}", "31", `{"monthDay":"31"}`},
		{"%{YEAR:year}", "2024", `{"year":"2024"}`},
		{"%{YEAR:year}", "24", `{"year":"24"}`},
		{"%{DAY:day}", "Tuesday", `{"day":"Tuesday"}`},
		{"%{HOUR:hour}", "22", `{"hour":"22"}`},
		{"%{MINUTE:min}", "59", `{"min":"59"}`},
		{"%{SECOND:second}", "3", `{"second":"3"}`},
		{"%{SECOND:minSec}", "30.5", `{"minSec":"30.5"}`},
		{"%{SECOND:minSec}", "30:5", `{"minSec":"30:5"}`},
		{"%{TIME:time}", "09:45:32", `{"time":"09:45:32"}`},
		{"%{DATE_US:date}", "11/23/2024", `{"date":"11/23/2024"}`},
		{"%{DATE_US:date}", "1-01-24", `{"date":"1-01-24"}`},
		{"%{DATE_EU:date}", "23/11/2024", `{"date":"23/11/2024"}`},
		{"%{DATE_EU:date}", "1.01.24", `{"date":"1.01.24"}`},
		{"%{ISO8601_TIMEZONE:tz}", "+05:30", `{"tz":"+05:30"}`},
		{"%{ISO8601_TIMEZONE:tz}", "-530", `{"tz":"-530"}`},
		{"%{ISO8601_TIMEZONE:tz}", "Z", `{"tz":"Z"}`},
		{"%{ISO8601_SECOND:second}", "60", `{"second":"60"}`},
		{"%{TIMESTAMP_ISO8601:timestamp}", "2023-05-15T14:30:00+05:30", `{"timestamp":"2023-05-15T14:30:00+05:30"}`},
		{"%{TIMESTAMP_ISO8601:timestamp}", "23-5-1T1:25+5:30", `{"timestamp":"23-5-1T1:25+5:30"}`},
		{"%{TIMESTAMP_ISO8601:timestamp}", "23-5-1T1:25Z", `{"timestamp":"23-5-1T1:25Z"}`},
		{"%{DATE:date}", "11/29/2024", `{"date":"11/29/2024"}`},
		{"%{DATE:date}", "29.11.2024", `{"date":"29.11.2024"}`},
		{"%{DATESTAMP:dateTime}", "29-11-2024 14:30:00", `{"dateTime":"29-11-2024 14:30:00"}`},
		{"%{TZ:tz}", "PDT", `{"tz":"PDT"}`},
		{"%{DATESTAMP_RFC822:dateTime}", "Monday Jan 5 23 1:30:00 CDT", `{"dateTime":"Monday Jan 5 23 1:30:00 CDT"}`},
		{"%{DATESTAMP_RFC822:dateTime}", "Mon January 15 2023 14:30:00 PST", `{"dateTime":"Mon January 15 2023 14:30:00 PST"}`},
		{"%{DATESTAMP_RFC2822:dateTime}", "Mon, 15 May 2023 14:30:00 +0530", `{"dateTime":"Mon, 15 May 2023 14:30:00 +0530"}`},
		{"%{DATESTAMP_RFC2822:dateTime}", "Monday, 15 Jan 23 14:30:00 Z", `{"dateTime":"Monday, 15 Jan 23 14:30:00 Z"}`},
		{"%{DATESTAMP_OTHER:dateTime}", "Mon May 15 14:30:00 PST 2023", `{"dateTime":"Mon May 15 14:30:00 PST 2023"}`},
		{"%{DATESTAMP_EVENTLOG:dateTime}", "20230515143000", `{"dateTime":"20230515143000"}`},
		{"%{LOGLEVEL:logLevel}", "INFO", `{"logLevel":"INFO"}`},
		{"%{HTTPDATE:date}", "23/Nov/2024:14:30:00 +0640", `{"date":"23/Nov/2024:14:30:00 +0640"}`},
		{"%{SYSLOGTIMESTAMP:dateTime}", "Nov 29 14:30:00", `{"dateTime":"Nov 29 14:30:00"}`},
		{"%{PROG:program}", "user.profile/settings-page", `{"program":"user.profile/settings-page"}`},
		{"%{SYSLOGPROG:programWithId}", "user.profile/settings-page[1234]", `{"programWithId":"user.profile/settings-page[1234]","program":"user.profile/settings-page","pid":"1234"}`},
		{"%{SYSLOGHOST:ip}", "2001:db8:3333:4444:5555:6666:7777:8888", `{"ip":"2001:db8:3333:4444:5555:6666:7777:8888"}`},
		{"%{SYSLOGFACILITY:syslog}", "<13.6>", `{"syslog":"<13.6>","facility":"13","priority":"6"}`},
	}
	for _, c := range cases {
		t.Run(c.match+" "+c.input, func(t *testing.T) {
			cfg, err := json.Marshal([]cwtProcessor{{Grok: &cwtGrok{Match: c.match}}})
			require.NoError(t, err)
			assert.JSONEq(t, c.want, cwtRun(t, string(cfg), c.input, nil))
		})
	}
}

func TestCWTransformerCopyValueMetadata(t *testing.T) {
	out := cwtRun(t, `[{"parseJSON":{}},{"copyValue":{"entries":[
		{"source":"outer_key.inner_key","target":"new_key"},
		{"source":"@logGroupName","target":"log_group_name"},
		{"source":"@logGroupStream","target":"log_group_stream"},
		{"source":"@accountId","target":"account_id"},
		{"source":"@regionName","target":"region_name"}]}}]`,
		`{"outer_key":{"inner_key":"inner_value"}}`,
		map[string]string{"@logGroupName": "myLogGroupName", "@logGroupStream": "myLogStreamName", "@accountId": "012345678912", "@regionName": "us-east-1"})
	assert.JSONEq(t, `{"outer_key":{"inner_key":"inner_value"},"new_key":"inner_value","log_group_name":"myLogGroupName","log_group_stream":"myLogStreamName","account_id":"012345678912","region_name":"us-east-1"}`, out)
}

// The guide's dateTimeConverter example parses a German date in Berlin time
// and converts it to New York time; 11:00 in Berlin is 05:00 in New York.
func TestCWTransformerDateTimeConverter(t *testing.T) {
	cases := []struct {
		name, converter, want string
	}{
		{"German locale, offset target",
			`{"source":"german_datetime","target":"target_1","locale":"de","matchPatterns":["EEEE dd. MMMM yyyy HH:mm:ss"],"sourceTimezone":"Europe/Berlin","targetTimezone":"America/New_York","targetFormat":"yyyy-MM-dd'T'HH:mm:ssXXX"}`,
			"1998-12-05T05:00:00-05:00"},
		{"default target format and zone",
			`{"source":"german_datetime","target":"target_1","locale":"de-DE","matchPatterns":["dd.MM.yyyy","EEEE dd. MMMM yyyy HH:mm:ss"],"sourceTimezone":"Europe/Berlin"}`,
			"1998-12-05T10:00:00.000Z"},
		{"abbreviated German names",
			`{"source":"german_datetime","target":"target_1","locale":"de","matchPatterns":["EEEE dd. MMMM yyyy HH:mm:ss"],"sourceTimezone":"+01:00","targetFormat":"EEE d. MMM yy, h:mm a"}`,
			"Sa. 5. Dez. 98, 10:00 AM"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := cwtRun(t, `[{"parseJSON":{}},{"dateTimeConverter":`+c.converter+`}]`,
				`{"german_datetime": "Samstag 05. Dezember 1998 11:00:00"}`, nil)
			var got map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			assert.Equal(t, c.want, got["target_1"])
		})
	}
	out := cwtRun(t, `[{"parseJSON":{}},{"dateTimeConverter":{"source":"t","target":"t2","locale":"en","matchPatterns":["dd/MMM/yyyy:HH:mm:ss Z"],"targetTimezone":"America/New_York","targetFormat":"yyyy-MM-dd'T'HH:mm:ss z"}}]`,
		`{"t":"03/Aug/2023:12:34:56 +0000"}`, nil)
	assert.JSONEq(t, `{"t":"03/Aug/2023:12:34:56 +0000","t2":"2023-08-03T08:34:56 EDT"}`, out)

	tr, err := cwtCompileJSON(t, `[{"parseJSON":{}},{"dateTimeConverter":{"source":"t","target":"t2","matchPatterns":["yyyy-MM-dd"],"locale":"en"}}]`)
	require.NoError(t, err)
	assert.ErrorContains(t, tr.Transform(`{"t":"05.12.1998"}`, nil).Err, "matches none of the match patterns")
	assert.ErrorContains(t, tr.Transform(`{"t":"1998-02-30x"}`, nil).Err, "matches none of the match patterns")
}

func TestCWTransformerFailures(t *testing.T) {
	tr, err := cwtCompileJSON(t, `[{"parseJSON":{}}]`)
	require.NoError(t, err)
	assert.ErrorContains(t, tr.Transform("not json", nil).Err, "is not valid JSON")
	assert.ErrorContains(t, tr.Transform(`{"big":"`+strings.Repeat("x", cwtMaxEventBytes)+`"}`, nil).Err,
		"exceeds the 524288-byte transformation limit")

	tr, err = cwtCompileJSON(t, `[{"grok":{"match":"%{INT:n}"}}]`)
	require.NoError(t, err)
	assert.ErrorContains(t, tr.Transform("abc", nil).Err, "does not match the grok pattern")

	tr, err = cwtCompileJSON(t, `[{"parseJSON":{}},{"typeConverter":{"entries":[{"key":"n","type":"integer"}]}}]`)
	require.NoError(t, err)
	assert.ErrorContains(t, tr.Transform(`{"n":"twelve"}`, nil).Err, `"twelve" is not an integer`)
}

func TestCWTransformerTypeConverter(t *testing.T) {
	out := cwtRun(t, `[{"parseJSON":{}},{"typeConverter":{"entries":[
		{"key":"i","type":"integer"},{"key":"d","type":"double"},{"key":"b","type":"boolean"},{"key":"s","type":"string"}]}}]`,
		`{"i":"-12","d":"3","b":"TRUE","s":42}`, nil)
	assert.Equal(t, `{"i":-12,"d":3.0,"b":true,"s":"42"}`, out)
}

// Configurations the model, the guide or DateTimeFormatter forbids answer
// InvalidParameterException.
func TestCWTransformerValidation(t *testing.T) {
	many := strings.Repeat(`{"trimString":{"withKeys":["a"]}},`, 20)
	cases := []struct{ name, config, code string }{
		{"no processors", `[]`, "InvalidParameterException"},
		{"more than 20 processors", `[{"parseJSON":{}},` + strings.TrimSuffix(many, ",") + `]`, "InvalidParameterException"},
		{"first processor not a parser", `[{"trimString":{"withKeys":["a"]}}]`, "InvalidParameterException"},
		{"two processor types in one processor", `[{"parseJSON":{},"csv":{}}]`, "InvalidParameterException"},
		{"vended parser not first", `[{"parseJSON":{}},{"parseVPC":{}}]`, "InvalidParameterException"},
		{"vended parser with a source", `[{"parseVPC":{"source":"other"}}]`, "InvalidParameterException"},
		{"two grok processors", `[{"grok":{"match":"%{INT:a}"}},{"grok":{"match":"%{INT:b}"}}]`, "InvalidParameterException"},
		{"two addKeys processors", `[{"parseJSON":{}},{"addKeys":{"entries":[{"key":"a","value":"1"}]}},{"addKeys":{"entries":[{"key":"b","value":"1"}]}}]`, "InvalidParameterException"},
		{"six parsers", `[{"parseJSON":{}},{"csv":{}},{"csv":{}},{"csv":{}},{"csv":{}},{"csv":{}}]`, "InvalidParameterException"},
		{"first parseJSON not on @message", `[{"parseJSON":{"source":"a"}}]`, "InvalidParameterException"},
		{"nested key deeper than 3", `[{"parseJSON":{}},{"addKeys":{"entries":[{"key":"a.b.c.d","value":"1"}]}}]`, "InvalidParameterException"},
		{"addKeys value over 256 characters", `[{"parseJSON":{}},{"addKeys":{"entries":[{"key":"a","value":"` + strings.Repeat("v", 257) + `"}]}}]`, "InvalidParameterException"},
		{"csv delimiter of two characters", `[{"csv":{"delimiter":";;"}}]`, "InvalidParameterException"},
		{"listToMap flatten without flattenedElement", `[{"parseJSON":{}},{"listToMap":{"source":"a","key":"k","flatten":true}}]`, "InvalidParameterException"},
		{"typeConverter unknown type", `[{"parseJSON":{}},{"typeConverter":{"entries":[{"key":"a","type":"long"}]}}]`, "InvalidParameterException"},
		{"substituteString bad group", `[{"parseJSON":{}},{"substituteString":{"entries":[{"source":"a","from":"(x)","to":"$2"}]}}]`, "InvalidParameterException"},
		{"substituteString repeats a backreference three times", `[{"parseJSON":{}},{"substituteString":{"entries":[{"source":"a","from":"(x)","to":"$1$1$1"}]}}]`, "InvalidParameterException"},
		{"grok unknown pattern", `[{"grok":{"match":"%{NOPE:a}"}}]`, "InvalidParameterException"},
		{"grok type conversion", `[{"grok":{"match":"%{NUMBER:a:int}"}}]`, "InvalidParameterException"},
		{"grok two common log patterns", `[{"grok":{"match":"%{APACHE_ACCESS_LOG} %{SYSLOG5424}"}}]`, "InvalidParameterException"},
		{"grok NUMBER after a common log pattern", `[{"grok":{"match":"%{NGINX_ACCESS_LOG} %{NUMBER:num}"}}]`, "InvalidParameterException"},
		{"grok common log pattern after GREEDYDATA", `[{"grok":{"match":"%{GREEDYDATA:logMsg} %{SYSLOG5424}"}}]`, "InvalidParameterException"},
		{"grok GREEDYDATA_MULTILINE twice", `[{"grok":{"match":"%{GREEDYDATA_MULTILINE:a} %{GREEDYDATA_MULTILINE:b}"}}]`, "InvalidParameterException"},
		{"grok six shared-limit patterns", `[{"grok":{"match":"%{DATA:a} %{DATA:b} %{SPACE:c}%{URI:d} %{GREEDYDATA:e} %{URIPARAM:f}"}}]`, "InvalidParameterException"},
		{"grok over 512 characters", `[{"grok":{"match":"%{INT:a}` + strings.Repeat("x", 510) + `"}}]`, "InvalidParameterException"},
		{"dateTimeConverter unknown letter", `[{"parseJSON":{}},{"dateTimeConverter":{"source":"a","target":"b","matchPatterns":["yyyy-bb"]}}]`, "InvalidParameterException"},
		{"dateTimeConverter unknown zone", `[{"parseJSON":{}},{"dateTimeConverter":{"source":"a","target":"b","matchPatterns":["yyyy"],"sourceTimezone":"Mars/Olympus"}}]`, "InvalidParameterException"},
		{"parseToOCSF mappingVersion with V1.1", `[{"parseToOCSF":{"eventSource":"VPCFlow","ocsfVersion":"V1.1","mappingVersion":"1.5.0"}}]`, "InvalidParameterException"},
		{"parseToOCSF mapping version that does not exist", `[{"parseToOCSF":{"eventSource":"CloudTrail","ocsfVersion":"V1.5","mappingVersion":"1.4.0"}}]`, "InvalidParameterException"},
		{"dateTimeConverter ] without [", `[{"parseJSON":{}},{"dateTimeConverter":{"source":"a","target":"b","matchPatterns":["yyyy]"]}}]`, "InvalidParameterException"},
		{"dateTimeConverter five z", `[{"parseJSON":{}},{"dateTimeConverter":{"source":"a","target":"b","matchPatterns":["zzzzz"]}}]`, "InvalidParameterException"},
		{"dateTimeConverter cc", `[{"parseJSON":{}},{"dateTimeConverter":{"source":"a","target":"b","matchPatterns":["cc"]}}]`, "InvalidParameterException"},
		{"dateTimeConverter BB", `[{"parseJSON":{}},{"dateTimeConverter":{"source":"a","target":"b","matchPatterns":["BB"]}}]`, "InvalidParameterException"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := cwtCompileJSON(t, c.config)
			var ce *cwtConfigError
			require.True(t, errors.As(err, &ce), "want a refusal, got %v", err)
			assert.Equal(t, c.code, ce.Code, ce.Message)
		})
	}
	for _, valid := range []string{
		`[{"grok":{"match":"%{NGINX_ACCESS_LOG} %{DATA}"}}]`,
		`[{"grok":{"match":"%{SYSLOG5424}%{DATA:logMsg}"}}]`,
		`[{"grok":{"match":"%{APACHE_ACCESS_LOG} %{GREEDYDATA:logMsg}"}}]`,
		`[{"csv":{"delimiter":"\\t"}}]`,
		`[{"parseToOCSF":{"eventSource":"CloudTrail","ocsfVersion":"V1.5","mappingVersion":"1.5.0"}}]`,
		`[{"parseToOCSF":{"eventSource":"VPCFlow","ocsfVersion":"V1.1"}}]`,
	} {
		_, err := cwtCompileJSON(t, valid)
		assert.NoError(t, err, valid)
	}
}

func TestCWTransformerCSVDelimiters(t *testing.T) {
	assert.JSONEq(t, `{"column_1":"a","column_2":"b c"}`, cwtRun(t, `[{"csv":{"delimiter":"\\t"}}]`, "a\tb c", nil))
	assert.JSONEq(t, `{"column_1":"a","column_2":"b"}`, cwtRun(t, `[{"csv":{"delimiter":"\\s"}}]`, "a b", nil))
	assert.JSONEq(t, `{"column_1":"say \"hi\"","column_2":"x"}`, cwtRun(t, `[{"csv":{}}]`, `"say ""hi""",x`, nil))
}

func TestCWTransformerSyslogFollowedByData(t *testing.T) {
	out := cwtRun(t, `[{"grok":{"match":"%{SYSLOG5424}%{DATA:logMsg}"}}]`,
		`<34>1 2003-10-11T22:14:15.003Z host app 42 ID1 - hello there`, nil)
	assert.JSONEq(t, `{"pri":34,"version":1,"timestamp":"2003-10-11T22:14:15.003Z","hostname":"host","app":"app","proc_id":"42","msg_id":"ID1","logMsg":"hello there"}`, out)
}
