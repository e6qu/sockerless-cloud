package main

import (
	"fmt"
	"net"
	"net/smtp"
	"sort"
	"strings"
)

// awsDeliverSMTP sends a message through the destination domain's real SMTP
// servers. AWS services use their own carrier infrastructure in production;
// the simulator follows the public mail contract by resolving MX records and
// speaking SMTP, with the domain host itself used when it has no MX record.
func awsDeliverSMTP(domain, from string, recipients []string, message []byte) error {
	domain = strings.TrimSuffix(domain, ".")
	mxRecords, _ := net.LookupMX(domain)
	hosts, err := smtpRelayHosts(domain, mxRecords)
	if err != nil {
		return err
	}
	var lastErr error
	for _, host := range hosts {
		lastErr = smtp.SendMail(net.JoinHostPort(host, "25"), nil, from, recipients, message)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func smtpRelayHosts(domain string, mxRecords []*net.MX) ([]string, error) {
	if len(mxRecords) == 0 {
		return []string{domain}, nil
	}
	sort.Slice(mxRecords, func(i, j int) bool { return mxRecords[i].Pref < mxRecords[j].Pref })
	hosts := make([]string, 0, len(mxRecords))
	for _, record := range mxRecords {
		// RFC 7505: an MX of "." declares that the domain accepts no mail.
		if host := strings.TrimSuffix(record.Host, "."); host != "" {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%s publishes a null MX record and accepts no mail", domain)
	}
	return hosts, nil
}
