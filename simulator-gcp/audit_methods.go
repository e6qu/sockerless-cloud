package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The methods a service audits, as its audit-logging page lists them:
// scripts/gen-gcp-audit-methods.go compiles the vendored pages under
// specs/cloud-api/gcp/ into audit_methods_vendored.json. A service the table
// covers writes an entry for a call only when the page lists its method, under
// the methodName, audit log category and permission the page states.

//go:embed audit_methods_vendored.json
var auditMethodsVendored []byte

type auditVendoredPermission struct {
	Permission string `json:"permission"`
	Type       string `json:"type"`
}

type auditVendoredMethod struct {
	Service     string                    `json:"service"`
	Method      string                    `json:"method"`
	LogType     string                    `json:"logType"`
	Permissions []auditVendoredPermission `json:"permissions"`
	LongRunning bool                      `json:"longRunning"`
}

// permission is the permission the entry's authorizationInfo records: an
// Admin Activity entry names the ADMIN_WRITE permission the call checked, a
// Data Access entry the first permission the page lists.
func (m auditVendoredMethod) permission() string {
	if len(m.Permissions) == 0 {
		return ""
	}
	if m.LogType == auditAdminWrite {
		for _, p := range m.Permissions {
			if p.Type == auditAdminWrite {
				return p.Permission
			}
		}
	}
	return m.Permissions[0].Permission
}

type auditVendoredTable struct {
	methods   map[string]auditVendoredMethod
	byService map[string][]auditVendoredMethod
}

var auditVendored = sync.OnceValue(func() *auditVendoredTable {
	var payload struct {
		Methods []auditVendoredMethod `json:"methods"`
	}
	if err := json.Unmarshal(auditMethodsVendored, &payload); err != nil {
		panic(fmt.Sprintf("audit_methods_vendored.json: %v", err))
	}
	table := &auditVendoredTable{methods: map[string]auditVendoredMethod{}, byService: map[string][]auditVendoredMethod{}}
	for _, m := range payload.Methods {
		table.methods[m.Service+" "+m.Method] = m
		table.byService[m.Service] = append(table.byService[m.Service], m)
	}
	return table
})

// auditVendoredCovers reports whether a service's audited methods come from
// its vendored page.
func auditVendoredCovers(service string) bool {
	return len(auditVendored().byService[service]) > 0
}

func auditVendoredLookup(service, method string) (auditVendoredMethod, bool) {
	m, ok := auditVendored().methods[service+" "+method]
	return m, ok
}

// auditVendoredRPC finds the audited RPC a Discovery method reaches, named
// <pkg>.<Interface>.<Rpc>: the interface is the collection's own, and the RPC
// the method's verb with or without the resource's name, as InsertDataset
// for datasets.insert, ListServices for services.list and Query for
// jobs.query.
func auditVendoredRPC(service, pkg, iface, verb, collection string) (auditVendoredMethod, bool) {
	rpcs := []string{
		auditTitle(verb),
		auditTitle(verb) + auditTitle(auditSingular(collection)),
		auditTitle(verb) + auditTitle(collection),
	}
	for _, m := range auditVendored().byService[service] {
		rest, ok := strings.CutPrefix(m.Method, pkg+".")
		if !ok {
			continue
		}
		gotIface, gotRPC, ok := strings.Cut(rest, ".")
		if !ok || !strings.EqualFold(gotIface, iface) {
			continue
		}
		for _, rpc := range rpcs {
			if strings.EqualFold(gotRPC, rpc) {
				return m, true
			}
		}
	}
	return auditVendoredMethod{}, false
}
