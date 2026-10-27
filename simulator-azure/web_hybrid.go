package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The two hybrid connection families at site and slot level:
//
//   - hybridConnectionNamespaces/{ns}/relays/{relay} — the Service Bus relay
//     ("V2") connection, plus the hybridConnectionRelays view;
//   - hybridconnection/{entityName} — the classic (BizTalk) relay connection.
//
// networkFeatures reports them side by side (hybridConnections,
// hybridConnectionsV2). Each store keys by canonical resource ID, and the plan
// views (web_plan_network.go) read these stores.

type WebHybridConnection struct {
	ID         string                        `json:"id,omitempty"`
	Name       string                        `json:"name,omitempty"`
	Type       string                        `json:"type,omitempty"`
	Properties WebHybridConnectionProperties `json:"properties"`
}

// sendKeyValue is kept but never returned on reads, as the contract documents;
// the plan-level listKeys returns it.
type WebHybridConnectionProperties struct {
	ServiceBusNamespace string `json:"serviceBusNamespace,omitempty"`
	RelayName           string `json:"relayName,omitempty"`
	RelayARMURI         string `json:"relayArmUri,omitempty"`
	Hostname            string `json:"hostname,omitempty"`
	Port                int32  `json:"port,omitempty"`
	SendKeyName         string `json:"sendKeyName,omitempty"`
	SendKeyValue        string `json:"sendKeyValue,omitempty"`
	ServiceBusSuffix    string `json:"serviceBusSuffix,omitempty"`
}

type WebRelayServiceConnection struct {
	ID         string                              `json:"id,omitempty"`
	Name       string                              `json:"name,omitempty"`
	Type       string                              `json:"type,omitempty"`
	Properties WebRelayServiceConnectionProperties `json:"properties"`
}

// Wire spellings included: biztalkUri.
type WebRelayServiceConnectionProperties struct {
	EntityName               string `json:"entityName,omitempty"`
	EntityConnectionString   string `json:"entityConnectionString,omitempty"`
	ResourceType             string `json:"resourceType,omitempty"`
	ResourceConnectionString string `json:"resourceConnectionString,omitempty"`
	Hostname                 string `json:"hostname,omitempty"`
	Port                     int32  `json:"port,omitempty"`
	BiztalkURI               string `json:"biztalkUri,omitempty"`
}

var (
	webHybridConnections sim.Store[WebHybridConnection]
	webRelayServiceConns sim.Store[WebRelayServiceConnection]
)

func registerWebHybridConnections(srv *sim.Server) {
	webHybridConnections = sim.MakeStore[WebHybridConnection](srv.DB(), "web_hybrid_connections")
	webRelayServiceConns = sim.MakeStore[WebRelayServiceConnection](srv.DB(), "web_relay_service_connections")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	relayID := func(r *http.Request) string {
		return webResourceID(r) + "/hybridConnectionNamespaces/" + sim.PathParam(r, "namespaceName") +
			"/relays/" + sim.PathParam(r, "relayName")
	}

	both("GET", "/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		hc, ok := webHybridConnections.Get(relayID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, stripSendKeyValue(hc))
	})

	putRelay := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebHybridConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := relayID(r)
		hc := req
		if existing, ok := webHybridConnections.Get(id); ok {
			// An update keeps omitted members, above all the send key, which clients rarely
			// re-send.
			if hc.Properties.RelayARMURI == "" {
				hc.Properties.RelayARMURI = existing.Properties.RelayARMURI
			}
			if hc.Properties.Hostname == "" {
				hc.Properties.Hostname = existing.Properties.Hostname
			}
			if hc.Properties.Port == 0 {
				hc.Properties.Port = existing.Properties.Port
			}
			if hc.Properties.SendKeyName == "" {
				hc.Properties.SendKeyName = existing.Properties.SendKeyName
			}
			if hc.Properties.SendKeyValue == "" {
				hc.Properties.SendKeyValue = existing.Properties.SendKeyValue
			}
			if hc.Properties.ServiceBusSuffix == "" {
				hc.Properties.ServiceBusSuffix = existing.Properties.ServiceBusSuffix
			}
		}
		hc.ID = id
		hc.Name = sim.PathParam(r, "relayName")
		hc.Type = webChildType(r, "hybridConnectionNamespaces/relays")
		hc.Properties.ServiceBusNamespace = sim.PathParam(r, "namespaceName")
		hc.Properties.RelayName = sim.PathParam(r, "relayName")
		webHybridConnections.Put(id, hc)
		sim.WriteJSON(w, http.StatusOK, stripSendKeyValue(hc))
	}
	both("PUT", "/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}", putRelay)
	both("PATCH", "/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}", putRelay)

	both("DELETE", "/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		if !webHybridConnections.Delete(relayID(r)) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// The contract declares a single HybridConnection, which the armappservice SDK
	// unmarshals, so answer the first by ID, or an empty envelope.
	both("GET", "/hybridConnectionRelays", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		conns := siteHybridConnections(webResourceID(r))
		if len(conns) > 0 {
			sim.WriteJSON(w, http.StatusOK, stripSendKeyValue(conns[0]))
			return
		}
		sim.WriteJSON(w, http.StatusOK, WebHybridConnection{
			ID:   webResourceID(r) + "/hybridConnectionRelays",
			Name: "hybridConnectionRelays",
			Type: webChildType(r, "hybridConnectionRelays"),
		})
	})

	entityID := func(r *http.Request) string {
		return webResourceID(r) + "/hybridconnection/" + sim.PathParam(r, "entityName")
	}

	// The swagger declares one RelayServiceConnectionEntity, so answer the first.
	both("GET", "/hybridconnection", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		conns := siteRelayServiceConnections(webResourceID(r))
		if len(conns) > 0 {
			sim.WriteJSON(w, http.StatusOK, conns[0])
			return
		}
		sim.WriteJSON(w, http.StatusOK, WebRelayServiceConnection{
			ID:   webResourceID(r) + "/hybridconnection",
			Name: "hybridconnection",
			Type: webChildType(r, "hybridconnection"),
		})
	})

	both("GET", "/hybridconnection/{entityName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		conn, ok := webRelayServiceConns.Get(entityID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Relay service connection '%s' not found.", sim.PathParam(r, "entityName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	putEntity := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebRelayServiceConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := entityID(r)
		conn := req
		conn.ID = id
		conn.Name = sim.PathParam(r, "entityName")
		conn.Type = webChildType(r, "hybridconnection")
		if conn.Properties.EntityName == "" {
			conn.Properties.EntityName = sim.PathParam(r, "entityName")
		}
		webRelayServiceConns.Put(id, conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	}
	both("PUT", "/hybridconnection/{entityName}", putEntity)
	both("PATCH", "/hybridconnection/{entityName}", putEntity)

	both("DELETE", "/hybridconnection/{entityName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		if !webRelayServiceConns.Delete(entityID(r)) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Relay service connection '%s' not found.", sim.PathParam(r, "entityName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Only listKeys returns the send key.
func stripSendKeyValue(hc WebHybridConnection) WebHybridConnection {
	hc.Properties.SendKeyValue = ""
	return hc
}

func siteHybridConnections(resID string) []WebHybridConnection {
	prefix := resID + "/hybridConnectionNamespaces/"
	out := webHybridConnections.Filter(func(c WebHybridConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func siteRelayServiceConnections(resID string) []WebRelayServiceConnection {
	prefix := resID + "/hybridconnection/"
	out := webRelayServiceConns.Filter(func(c WebRelayServiceConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
