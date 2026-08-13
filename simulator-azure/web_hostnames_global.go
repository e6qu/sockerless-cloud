package main

import (
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_hostnames_global.go implements the Microsoft.Web hostname-truth
// surfaces: WebApps_AnalyzeCustomHostname[Slot] (a real CNAME/TXT/A analysis
// answered off the simulator's Azure DNS record sets), ListCustomHostNameSites
// and ListSiteIdentifiersAssignedToHostName (both assembled from the real
// host-name-binding store).

// webDNSRecordsFor collects the CNAME target, TXT values and A addresses the
// simulator's Azure DNS zones hold for a fully-qualified hostname — the same
// lookups real App Service performs when it analyzes a custom hostname.
func webDNSRecordsFor(host string) (cnames, txts, as []string) {
	for _, rs := range azurePublicDNSRecordSets.List() {
		if !strings.EqualFold(publicRecordSetFQDN(rs), host) {
			continue
		}
		switch {
		case strings.HasSuffix(rs.Type, "/CNAME") && rs.Properties.CNAMERecord != nil:
			cnames = append(cnames, strings.TrimSuffix(rs.Properties.CNAMERecord.CName, "."))
		case strings.HasSuffix(rs.Type, "/TXT"):
			for _, txt := range rs.Properties.TXTRecords {
				txts = append(txts, strings.Join(txt.Value, ""))
			}
		case strings.HasSuffix(rs.Type, "/A"):
			for _, a := range rs.Properties.ARecords {
				as = append(as, a.IPv4Address)
			}
		}
	}
	return cnames, txts, as
}

// publicRecordSetFQDN derives a public-DNS record set's fully-qualified name
// from its ARM ID (.../dnsZones/{zone}/{TYPE}/{relativeName}); the body's
// fqdn member is optional on writes.
func publicRecordSetFQDN(rs PublicRecordSet) string {
	if f := strings.TrimSuffix(rs.Properties.Fqdn, "."); f != "" {
		return f
	}
	segs := strings.Split(rs.ID, "/")
	if len(segs) < 3 {
		return ""
	}
	rel, zone := segs[len(segs)-1], segs[len(segs)-3]
	if rel == "@" {
		return zone
	}
	return rel + "." + zone
}

// webBindingHostName extracts the bound hostname from a host-name-binding
// resource ID (".../hostNameBindings/{hostName}").
func webBindingHostName(b WebHostNameBinding) string {
	segs := strings.Split(b.ID, "/hostNameBindings/")
	if len(segs) != 2 {
		return ""
	}
	return segs[1]
}

// webBindingSiteID returns the site or slot resource the binding hangs off.
func webBindingSiteID(b WebHostNameBinding) string {
	segs := strings.Split(b.ID, "/hostNameBindings/")
	return segs[0]
}

func registerWebHostnameTruth(srv *sim.Server, both func(string, string, http.HandlerFunc)) {
	// WebApps_AnalyzeCustomHostname[Slot] — analyze a hostname's DNS state
	// against the site: which records exist, whether they prove ownership,
	// and whether the hostname conflicts with a binding elsewhere.
	both("GET", "/analyzeCustomHostname", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		host := r.URL.Query().Get("hostName")

		cnames, txts, as := webDNSRecordsFor(host)
		altCNames, altTxts, _ := webDNSRecordsFor("awverify." + host)

		props := map[string]any{
			"isHostnameAlreadyVerified":     false,
			"hasConflictOnScaleUnit":        false,
			"hasConflictAcrossSubscription": false,
		}
		if cnames != nil {
			props["cNameRecords"] = cnames
		}
		if txts != nil {
			props["txtRecords"] = txts
		}
		if as != nil {
			props["aRecords"] = as
		}
		if altCNames != nil {
			props["alternateCNameRecords"] = altCNames
		}
		if altTxts != nil {
			props["alternateTxtRecords"] = altTxts
		}

		siteID := webResourceID(r)
		subPrefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/"
		for _, b := range webHostNameBindings.List() {
			if !strings.EqualFold(webBindingHostName(b), host) {
				continue
			}
			owner := webBindingSiteID(b)
			if strings.EqualFold(owner, siteID) {
				props["isHostnameAlreadyVerified"] = true
				continue
			}
			props["conflictingAppResourceId"] = owner
			if strings.HasPrefix(owner, subPrefix) {
				props["hasConflictOnScaleUnit"] = true
			} else {
				props["hasConflictAcrossSubscription"] = true
			}
		}

		// Ownership proof: a CNAME (at the hostname or its awverify child)
		// pointing at the site's default hostname, or a TXT record carrying
		// it, passes the verification test — the record state real App
		// Service accepts when a hostname is added.
		defaultHost := site.Properties.DefaultHostName
		verified := false
		for _, c := range append(append([]string{}, cnames...), altCNames...) {
			if strings.EqualFold(c, defaultHost) {
				verified = true
			}
		}
		for _, txt := range append(append([]string{}, txts...), altTxts...) {
			if strings.EqualFold(txt, defaultHost) {
				verified = true
			}
		}
		if verified {
			props["customDomainVerificationTest"] = "Passed"
		} else {
			props["customDomainVerificationTest"] = "Failed"
			props["customDomainVerificationFailureInfo"] = map[string]any{
				"code":    "DnsRecordNotFound",
				"message": "No CNAME or TXT record pointing from '" + host + "' to '" + defaultHost + "' was found in the configured DNS zones.",
			}
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         siteID + "/analyzeCustomHostname",
			"name":       host,
			"type":       "Microsoft.Web/sites/analyzeCustomHostname",
			"properties": props,
		})
	})

	// ListCustomHostNameSites — the subscription's custom hostnames, grouped
	// per hostname with every site resource bound to it, assembled from the
	// real host-name-binding store. The optional ?hostname= filter narrows to
	// one hostname, as the real API does.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/customhostnameSites", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		filter := r.URL.Query().Get("hostname")
		subPrefix := "/subscriptions/" + sub + "/"

		type entry struct {
			region  string
			siteIDs []string
		}
		grouped := map[string]*entry{}
		for _, b := range webHostNameBindings.List() {
			host := webBindingHostName(b)
			if host == "" || !strings.HasPrefix(b.ID, subPrefix) {
				continue
			}
			// The default *.azurewebsites.net hostname is not a custom hostname.
			if strings.HasSuffix(strings.ToLower(host), ".azurewebsites.net") {
				continue
			}
			if filter != "" && !strings.EqualFold(host, filter) {
				continue
			}
			e := grouped[strings.ToLower(host)]
			if e == nil {
				e = &entry{}
				grouped[strings.ToLower(host)] = e
			}
			owner := webBindingSiteID(b)
			e.siteIDs = append(e.siteIDs, owner)
			if site, ok := azfSites.Get(owner); ok {
				e.region = site.Location
			} else if slot, ok := webSlots.Get(owner); ok {
				e.region = slot.Location
			}
		}

		hosts := make([]string, 0, len(grouped))
		for h := range grouped {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		out := make([]map[string]any, 0, len(hosts))
		for _, h := range hosts {
			e := grouped[h]
			sort.Strings(e.siteIDs)
			// siteResourceIds is an array of Identifier resources, each
			// carrying the bound site's ARM resource ID.
			identifiers := make([]map[string]any, 0, len(e.siteIDs))
			for _, siteID := range e.siteIDs {
				segs := strings.Split(siteID, "/")
				identifiers = append(identifiers, map[string]any{
					"id":   siteID,
					"name": segs[len(segs)-1],
				})
			}
			out = append(out, map[string]any{
				"id":   "/subscriptions/" + sub + "/providers/Microsoft.Web/customhostnameSites/" + h,
				"name": h,
				"type": "Microsoft.Web/customhostnameSites",
				"properties": map[string]any{
					"customHostname":  h,
					"region":          e.region,
					"siteResourceIds": identifiers,
				},
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// ListSiteIdentifiersAssignedToHostName — the sites the hostname is
	// assigned to, assembled from the real host-name-binding store.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/listSitesAssignedToHostName", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		subPrefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/"
		out := []map[string]any{}
		seen := map[string]bool{}
		for _, b := range webHostNameBindings.List() {
			if !strings.HasPrefix(b.ID, subPrefix) || !strings.EqualFold(webBindingHostName(b), req.Name) {
				continue
			}
			owner := webBindingSiteID(b)
			if seen[owner] {
				continue
			}
			seen[owner] = true
			segs := strings.Split(owner, "/")
			siteName := segs[len(segs)-1]
			out = append(out, map[string]any{
				"name":       siteName,
				"properties": map[string]any{"id": owner},
			})
		}
		sort.Slice(out, func(i, j int) bool {
			ni, _ := out[i]["name"].(string)
			nj, _ := out[j]["name"].(string)
			return ni < nj
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
}
