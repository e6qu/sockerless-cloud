package aws_sdk_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// dnsAddr is the host:port of the simulator's Route 53 DNS server, set by
// TestMain (helpers_test.go) before tests run.
func dnsAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", dnsPort)
}

// dnsQuery sends a single UDP DNS query to the simulator's DNS server and
// returns the parsed response message. Uses the same wire protocol any DNS
// resolver uses — no client-side shortcuts.
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
}

func TestRoute53DNSServesTXT(t *testing.T) {
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
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zone := fmt.Sprintf("dns-nx-%s.local", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, c, zone)
	t.Cleanup(func() { _, _ = c.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)}) })

	resp := dnsQuery(t, "does-not-exist."+zone, dnsmessage.TypeA)
	assert.Equal(t, dnsmessage.RCodeNameError, resp.RCode)
}

func TestRoute53DNSWildcardARecord(t *testing.T) {
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
