package main

import (
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_hybrid.go serves the two App Service hybrid connection families on
// production sites and their deployment-slot twins:
//
//   - hybridConnectionNamespaces/{ns}/relays/{relay} — the Service Bus relay
//     ("V2") hybrid connection, plus the hybridConnectionRelays view;
//   - hybridconnection/{entityName} — the classic (BizTalk) relay service
//     connection.
//
// They are distinct resources in real App Service (networkFeatures reports
// them side by side as hybridConnections and hybridConnectionsV2); each keeps
// one store keyed by the canonical ARM resource id. The App Service plan-level
// hybrid connection views (web_plan_network.go) assemble from these same
// site-level stores.

// WebHybridConnection mirrors the swagger HybridConnection resource.
type WebHybridConnection struct {
	ID         string                        `json:"id,omitempty"`
	Name       string                        `json:"name,omitempty"`
	Type       string                        `json:"type,omitempty"`
	Properties WebHybridConnectionProperties `json:"properties"`
}

// WebHybridConnectionProperties mirrors the swagger HybridConnectionProperties.
// sendKeyValue is carried as state but — exactly as the contract documents —
// never returned on ARM reads; POST …/listKeys (plan level) is the read path.
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

// WebRelayServiceConnection mirrors the swagger RelayServiceConnectionEntity.
type WebRelayServiceConnection struct {
	ID         string                              `json:"id,omitempty"`
	Name       string                              `json:"name,omitempty"`
	Type       string                              `json:"type,omitempty"`
	Properties WebRelayServiceConnectionProperties `json:"properties"`
}

// WebRelayServiceConnectionProperties mirrors the swagger
// RelayServiceConnectionEntityProperties (wire spellings included: biztalkUri).
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := relayID(r)
		hc := req
		if existing, ok := webHybridConnections.Get(id); ok {
			// An update keeps the members the request omitted — most
			// importantly the send key, which clients rarely re-send.
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// hybridConnectionRelays — the contract declares a single HybridConnection
	// (not a collection), and the armappservice SDK unmarshals exactly that, so
	// the wire can carry at most one: the first connection in id order, or an
	// empty envelope when the site has none.
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

	// The list spelling shares the single-resource contract quirk: the swagger
	// declares one RelayServiceConnectionEntity, so the wire carries the first.
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Relay service connection '%s' not found.", sim.PathParam(r, "entityName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// stripSendKeyValue renders a hybrid connection for an ARM read: the send key
// value never rides a GET/PUT response, only the listKeys action.
func stripSendKeyValue(hc WebHybridConnection) WebHybridConnection {
	hc.Properties.SendKeyValue = ""
	return hc
}

// siteHybridConnections lists one site/slot's Service Bus relay hybrid
// connections, sorted by id.
func siteHybridConnections(resID string) []WebHybridConnection {
	prefix := resID + "/hybridConnectionNamespaces/"
	out := webHybridConnections.Filter(func(c WebHybridConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// siteRelayServiceConnections lists one site/slot's classic relay service
// connections, sorted by id.
func siteRelayServiceConnections(resID string) []WebRelayServiceConnection {
	prefix := resID + "/hybridconnection/"
	out := webRelayServiceConns.Filter(func(c WebRelayServiceConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
