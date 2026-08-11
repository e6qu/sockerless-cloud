package main

import (
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
	hosts := make([]string, 0)
	if mxRecords, err := net.LookupMX(domain); err == nil {
		sort.Slice(mxRecords, func(i, j int) bool { return mxRecords[i].Pref < mxRecords[j].Pref })
		for _, record := range mxRecords {
			hosts = append(hosts, strings.TrimSuffix(record.Host, "."))
		}
	}
	if len(hosts) == 0 {
		hosts = append(hosts, domain)
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
