package aws_sdk_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// dnsAddr is the host:port of the simulator's Route 53 DNS server, which
// TestMain sets.
func dnsAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", dnsPort)
}

// dnsQuery sends one UDP DNS query, over the wire protocol any resolver uses,
// and parses the response.
func dnsQuery(t *testing.T, name string, qtype dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	qbuf, err := buildDNSQuery(name, qtype)
	require.NoError(t, err)

	conn, err := net.Dial("udp", dnsAddr())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	_, err = conn.Write(qbuf)
	require.NoError(t, err)

	resp := make([]byte, 4096)
	n, err := conn.Read(resp)
	require.NoError(t, err)

	var msg dnsmessage.Message
	require.NoError(t, msg.Unpack(resp[:n]))
	return msg
}

func buildDNSQuery(name string, qtype dnsmessage.Type) ([]byte, error) {
	full := name
	if !strings.HasSuffix(full, ".") {
		full += "."
	}
	dn, err := dnsmessage.NewName(full)
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               0x1234,
		OpCode:           0,
		RecursionDesired: false,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: dn, Type: qtype, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

func TestRoute53DNSServesARecord(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-a-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	host := "web." + zone
	ip := "192.0.2.42"
	_, err := c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionCreate,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:            aws.String(host),
					Type:            r53types.RRTypeA,
					TTL:             aws.Int64(60),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(ip)}},
				},
			}},
		},
	})
	require.NoError(t, err)

	resp := dnsQuery(t, host, dnsmessage.TypeA)
	require.NotZero(t, resp.ID)
	require.True(t, resp.Response)
	require.True(t, resp.Authoritative, "should be AA=1")
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.Len(t, resp.Answers, 1)
	a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok, "expected AResource, got %T", resp.Answers[0].Body)
	assert.Equal(t, net.IP(a.A[:]).String(), ip)
}

func TestRoute53DNSServesCNAME(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-cname-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	alias := "alias." + zone
	target := "target." + zone
	_, err := c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{
				{
					Action: r53types.ChangeActionCreate,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name: aws.String(target), Type: r53types.RRTypeA, TTL: aws.Int64(60),
						ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("203.0.113.7")}},
					},
				},
				{
					Action: r53types.ChangeActionCreate,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name: aws.String(alias), Type: r53types.RRTypeCname, TTL: aws.Int64(60),
						ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(target)}},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	resp := dnsQuery(t, alias, dnsmessage.TypeCNAME)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.Len(t, resp.Answers, 1)
	cn, ok := resp.Answers[0].Body.(*dnsmessage.CNAMEResource)
	require.True(t, ok, "expected CNAMEResource, got %T", resp.Answers[0].Body)
	assert.Equal(t, strings.TrimSuffix(target, "")+".", cn.CNAME.String())

	// An address query for the alias follows the CNAME to its target in the
	// same answer, as an authoritative server does.
	chased := dnsQuery(t, alias, dnsmessage.TypeA)
	require.Equal(t, dnsmessage.RCodeSuccess, chased.RCode)
	require.Len(t, chased.Answers, 2)
	_, ok = chased.Answers[0].Body.(*dnsmessage.CNAMEResource)
	require.True(t, ok, "the chain starts with the CNAME, got %T", chased.Answers[0].Body)
	a, ok := chased.Answers[1].Body.(*dnsmessage.AResource)
	require.True(t, ok, "the chain ends with the target's address, got %T", chased.Answers[1].Body)
	assert.Equal(t, "203.0.113.7", net.IP(a.A[:]).String())

	// A name that exists without the asked type is NODATA: NOERROR, no
	// answer, and the zone's SOA for a resolver to cache the absence by.
	nodata := dnsQuery(t, target, dnsmessage.TypeMX)
	require.Equal(t, dnsmessage.RCodeSuccess, nodata.RCode)
	require.Empty(t, nodata.Answers)
	requireNegativeSOA(t, nodata)
}

func requireNegativeSOA(t *testing.T, msg dnsmessage.Message) {
	t.Helper()
	require.Len(t, msg.Authorities, 1, "a negative answer carries the zone's SOA")
	_, ok := msg.Authorities[0].Body.(*dnsmessage.SOAResource)
	require.True(t, ok, "expected SOAResource, got %T", msg.Authorities[0].Body)
}

func TestRoute53DNSServesTXT(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-txt-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	name := "info." + zone
	txt := `"v=spf1 -all"`
	_, err := c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionCreate,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name: aws.String(name), Type: r53types.RRTypeTxt, TTL: aws.Int64(60),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(txt)}},
				},
			}},
		},
	})
	require.NoError(t, err)

	resp := dnsQuery(t, name, dnsmessage.TypeTXT)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.Len(t, resp.Answers, 1)
	tx, ok := resp.Answers[0].Body.(*dnsmessage.TXTResource)
	require.True(t, ok, "expected TXTResource, got %T", resp.Answers[0].Body)
	joined := strings.Join(tx.TXT, "")
	assert.Contains(t, joined, "v=spf1")
}

func TestRoute53DNSApexSOA(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-soa-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	resp := dnsQuery(t, zone, dnsmessage.TypeSOA)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.NotEmpty(t, resp.Answers)
	_, ok := resp.Answers[0].Body.(*dnsmessage.SOAResource)
	require.True(t, ok, "expected SOAResource, got %T", resp.Answers[0].Body)
}

func TestRoute53DNSNS(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-ns-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	resp := dnsQuery(t, zone, dnsmessage.TypeNS)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.NotEmpty(t, resp.Answers, "apex must return NS records")
	_, ok := resp.Answers[0].Body.(*dnsmessage.NSResource)
	require.True(t, ok, "expected NSResource, got %T", resp.Answers[0].Body)
}

func TestRoute53DNSNXDOMAIN(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-nx-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	resp := dnsQuery(t, "does-not-exist."+zone, dnsmessage.TypeA)
	assert.Equal(t, dnsmessage.RCodeNameError, resp.RCode)
	requireNegativeSOA(t, resp)
}

func TestRoute53DNSWildcardARecord(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-wildcard-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	_, err := c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionCreate,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:            aws.String("*." + zone),
					Type:            r53types.RRTypeA,
					TTL:             aws.Int64(60),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("5.6.7.8")}},
				},
			}},
		},
	})
	require.NoError(t, err)

	resp := dnsQuery(t, "foo."+zone, dnsmessage.TypeA)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.Len(t, resp.Answers, 1)
	a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok, "expected AResource, got %T", resp.Answers[0].Body)
	assert.Equal(t, "5.6.7.8", net.IP(a.A[:]).String())
}

func TestRoute53DNSWildcardExactBeatsWildcard(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-wildcard-exact-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	_, err := c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{
				{
					Action: r53types.ChangeActionCreate,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name:            aws.String("*." + zone),
						Type:            r53types.RRTypeA,
						TTL:             aws.Int64(60),
						ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("5.6.7.8")}},
					},
				},
				{
					Action: r53types.ChangeActionCreate,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name:            aws.String("exact." + zone),
						Type:            r53types.RRTypeA,
						TTL:             aws.Int64(60),
						ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("1.2.3.4")}},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	resp := dnsQuery(t, "exact."+zone, dnsmessage.TypeA)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode)
	require.Len(t, resp.Answers, 1)
	a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok, "expected AResource, got %T", resp.Answers[0].Body)
	assert.Equal(t, "1.2.3.4", net.IP(a.A[:]).String())
}

func createZoneForDNS(t *testing.T, ctx context.Context, c *route53.Client, zone string) string {
	t.Helper()
	out, err := c.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String(zone),
		CallerReference: aws.String("dns-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	return strings.TrimPrefix(aws.ToString(out.HostedZone.Id), "/hostedzone/")
}

// A failover name answers its primary while the primary's health check
// passes and its secondary once the checker sees the endpoint fail.
func TestRoute53DNSFailoverFollowsAHealthCheck(t *testing.T) {
	t.Parallel()
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var failing atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(endpoint.Close)
	host, portText, err := net.SplitHostPort(endpoint.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	hc, err := c.CreateHealthCheck(ctx, &route53.CreateHealthCheckInput{
		CallerReference: aws.String("failover-" + time.Now().Format("150405.000000")),
		HealthCheckConfig: &r53types.HealthCheckConfig{
			Type:             r53types.HealthCheckTypeHttp,
			IPAddress:        aws.String(host),
			Port:             aws.Int32(int32(port)),
			ResourcePath:     aws.String("/"),
			RequestInterval:  aws.Int32(10),
			FailureThreshold: aws.Int32(1),
		},
	})
	require.NoError(t, err)
	hcID := aws.ToString(hc.HealthCheck.Id)
	t.Cleanup(func() {
		_, _ = c.DeleteHealthCheck(context.Background(), &route53.DeleteHealthCheckInput{HealthCheckId: aws.String(hcID)})
	})

	zone := fmt.Sprintf("dns-failover-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	name := "app." + zone
	record := func(id string, role r53types.ResourceRecordSetFailover, ip, check string) r53types.Change {
		set := &r53types.ResourceRecordSet{
			Name: aws.String(name), Type: r53types.RRTypeA, TTL: aws.Int64(10),
			SetIdentifier: aws.String(id), Failover: role,
			ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(ip)}},
		}
		if check != "" {
			set.HealthCheckId = aws.String(check)
		}
		return r53types.Change{Action: r53types.ChangeActionCreate, ResourceRecordSet: set}
	}
	changes := []r53types.Change{
		record("primary", r53types.ResourceRecordSetFailoverPrimary, "192.0.2.61", hcID),
		record("secondary", r53types.ResourceRecordSetFailoverSecondary, "192.0.2.62", ""),
	}
	_, err = c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID), ChangeBatch: &r53types.ChangeBatch{Changes: changes},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for i := range changes {
			changes[i].Action = r53types.ChangeActionDelete
		}
		_, _ = c.ChangeResourceRecordSets(context.Background(), &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID), ChangeBatch: &r53types.ChangeBatch{Changes: changes},
		})
		_, _ = c.DeleteHostedZone(context.Background(), &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)})
	})

	answer := func() string {
		resp := dnsQuery(t, name, dnsmessage.TypeA)
		require.Len(t, resp.Answers, 1, "a failover name answers one record set")
		a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
		require.True(t, ok)
		return net.IP(a.A[:]).String()
	}
	// Route 53 offers no waiter for a health check; its status is the field
	// to poll, on the check's own ten-second interval.
	awaitStatus := func(prefix string) {
		for {
			status, err := c.GetHealthCheckStatus(ctx, &route53.GetHealthCheckStatusInput{HealthCheckId: aws.String(hcID)})
			require.NoError(t, err)
			if len(status.HealthCheckObservations) == 1 &&
				strings.HasPrefix(aws.ToString(status.HealthCheckObservations[0].StatusReport.Status), prefix) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("the health check never reported %q: %+v", prefix, status.HealthCheckObservations)
			case <-time.After(time.Second):
			}
		}
	}

	awaitStatus("Success")
	assert.Equal(t, "192.0.2.61", answer())
	failing.Store(true)
	awaitStatus("Failure: HTTP Status Code 503")
	assert.Equal(t, "192.0.2.62", answer())
	reason, err := c.GetHealthCheckLastFailureReason(ctx, &route53.GetHealthCheckLastFailureReasonInput{HealthCheckId: aws.String(hcID)})
	require.NoError(t, err)
	require.Len(t, reason.HealthCheckObservations, 1)
}

// A CLOUDWATCH_METRIC health check follows its alarm: while the alarm has
// insufficient data the check takes InsufficientDataHealthStatus, in ALARM it
// is unhealthy and OK healthy, and a failover name answers accordingly.
// GetHealthCheck reports the alarm the check monitors.
func TestRoute53DNSFailoverFollowsACloudWatchAlarm(t *testing.T) {
	t.Parallel()
	c, cw := r53Client(), cloudwatchClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stamp := time.Now().Format("150405.000000")
	alarm, namespace := "r53-hc-alarm-"+stamp, "Sockerless/R53"+strings.ReplaceAll(stamp, ".", "")
	_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName: aws.String(alarm), Namespace: aws.String(namespace), MetricName: aws.String("Errors"),
		Statistic: cwtypes.StatisticMaximum, Period: aws.Int32(60), EvaluationPeriods: aws.Int32(1),
		Threshold: aws.Float64(1), ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(context.Background(), &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarm}})
	})

	hc, err := c.CreateHealthCheck(ctx, &route53.CreateHealthCheckInput{
		CallerReference: aws.String("alarm-" + stamp),
		HealthCheckConfig: &r53types.HealthCheckConfig{
			Type:                         r53types.HealthCheckTypeCloudwatchMetric,
			AlarmIdentifier:              &r53types.AlarmIdentifier{Region: r53types.CloudWatchRegionUsEast1, Name: aws.String(alarm)},
			InsufficientDataHealthStatus: r53types.InsufficientDataHealthStatusUnhealthy,
		},
	})
	require.NoError(t, err)
	hcID := aws.ToString(hc.HealthCheck.Id)
	t.Cleanup(func() {
		_, _ = c.DeleteHealthCheck(context.Background(), &route53.DeleteHealthCheckInput{HealthCheckId: aws.String(hcID)})
	})
	got, err := c.GetHealthCheck(ctx, &route53.GetHealthCheckInput{HealthCheckId: aws.String(hcID)})
	require.NoError(t, err)
	require.NotNil(t, got.HealthCheck.CloudWatchAlarmConfiguration)
	assert.Equal(t, "Errors", aws.ToString(got.HealthCheck.CloudWatchAlarmConfiguration.MetricName))
	assert.Equal(t, r53types.StatisticMaximum, got.HealthCheck.CloudWatchAlarmConfiguration.Statistic)
	assert.Equal(t, alarm, aws.ToString(got.HealthCheck.HealthCheckConfig.AlarmIdentifier.Name))

	zone := fmt.Sprintf("dns-alarm-%s.local", stamp)
	zoneID := createZoneForDNS(t, ctx, c, zone)
	name := "app." + zone
	record := func(id string, role r53types.ResourceRecordSetFailover, ip, check string) r53types.Change {
		set := &r53types.ResourceRecordSet{
			Name: aws.String(name), Type: r53types.RRTypeA, TTL: aws.Int64(10),
			SetIdentifier: aws.String(id), Failover: role,
			ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(ip)}},
		}
		if check != "" {
			set.HealthCheckId = aws.String(check)
		}
		return r53types.Change{Action: r53types.ChangeActionCreate, ResourceRecordSet: set}
	}
	changes := []r53types.Change{
		record("primary", r53types.ResourceRecordSetFailoverPrimary, "192.0.2.71", hcID),
		record("secondary", r53types.ResourceRecordSetFailoverSecondary, "192.0.2.72", ""),
	}
	_, err = c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID), ChangeBatch: &r53types.ChangeBatch{Changes: changes},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for i := range changes {
			changes[i].Action = r53types.ChangeActionDelete
		}
		_, _ = c.ChangeResourceRecordSets(context.Background(), &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID), ChangeBatch: &r53types.ChangeBatch{Changes: changes},
		})
		_, _ = c.DeleteHostedZone(context.Background(), &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)})
	})
	answer := func() string {
		resp := dnsQuery(t, name, dnsmessage.TypeA)
		require.Len(t, resp.Answers, 1)
		a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
		require.True(t, ok)
		return net.IP(a.A[:]).String()
	}
	put := func(value float64) {
		_, err := cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace:  aws.String(namespace),
			MetricData: []cwtypes.MetricDatum{{MetricName: aws.String("Errors"), Value: aws.Float64(value)}},
		})
		require.NoError(t, err)
	}

	assert.Equal(t, "192.0.2.72", answer(), "insufficient data counts as unhealthy here")
	put(0)
	assert.Equal(t, "192.0.2.71", answer(), "an OK alarm keeps the primary healthy")
	put(5)
	assert.Equal(t, "192.0.2.72", answer(), "an alarm in ALARM fails the primary over")
}
