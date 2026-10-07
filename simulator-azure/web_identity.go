package main

import (
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// SiteIdentity is an App Service app's or slot's ManagedServiceIdentity.
type SiteIdentity struct {
	Type                   string                               `json:"type"`
	TenantID               string                               `json:"tenantId,omitempty"`
	PrincipalID            string                               `json:"principalId,omitempty"`
	UserAssignedIdentities map[string]*SiteUserAssignedIdentity `json:"userAssignedIdentities,omitempty"`
}

// SiteUserAssignedIdentity is one user-assigned identity attached to a site.
type SiteUserAssignedIdentity struct {
	PrincipalID string `json:"principalId,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
}

func (id *SiteIdentity) systemAssigned() bool {
	return id != nil && strings.Contains(strings.ToLower(id.Type), "systemassigned")
}

func (id *SiteIdentity) userAssigned() bool {
	return id != nil && strings.Contains(strings.ToLower(id.Type), "userassigned")
}

// siteIdentityType spells a requested identity type as ManagedServiceIdentityType
// does, and reports whether it is one.
func siteIdentityType(t string) (string, bool) {
	norm := strings.ToLower(strings.ReplaceAll(t, " ", ""))
	for _, name := range []string{"SystemAssigned", "UserAssigned", "SystemAssigned, UserAssigned", "None"} {
		if norm == strings.ToLower(strings.ReplaceAll(name, " ", "")) {
			return name, true
		}
	}
	return "", false
}

// webApplySiteIdentity settles the identity a site PUT or PATCH asks for onto
// site, given what the site had before. A system-assigned identity keeps its
// principal for the life of the site and gets a new one when it is enabled
// again; each user-assigned identity must exist and carries its own principal
// and client IDs. A request that names no identity keeps the site's.
func webApplySiteIdentity(site *Site, requested *SiteIdentity, prev *Site) error {
	if requested == nil {
		if prev != nil {
			site.Identity = prev.Identity
			site.SystemIdentityClientID = prev.SystemIdentityClientID
		}
		return nil
	}
	typ, ok := siteIdentityType(requested.Type)
	if !ok {
		return fmt.Errorf("the identity type %q is not one of SystemAssigned, UserAssigned, 'SystemAssigned, UserAssigned' or None", requested.Type)
	}
	if typ == "None" {
		site.Identity = nil
		site.SystemIdentityClientID = ""
		return nil
	}
	out := &SiteIdentity{Type: typ}
	if out.systemAssigned() {
		out.TenantID = simTenantID
		if prev != nil && prev.Identity.systemAssigned() {
			out.PrincipalID = prev.Identity.PrincipalID
			site.SystemIdentityClientID = prev.SystemIdentityClientID
		} else {
			out.PrincipalID = sim.NewUUID()
			site.SystemIdentityClientID = sim.NewUUID()
		}
	} else {
		site.SystemIdentityClientID = ""
	}
	if out.userAssigned() {
		if len(requested.UserAssignedIdentities) == 0 {
			return fmt.Errorf("the identity type %s names no userAssignedIdentities", typ)
		}
		out.UserAssignedIdentities = map[string]*SiteUserAssignedIdentity{}
		for rid := range requested.UserAssignedIdentities {
			uai, found := webUserAssignedIdentity(rid)
			if !found {
				return fmt.Errorf("the user assigned identity '%s' was not found", rid)
			}
			out.UserAssignedIdentities[rid] = &SiteUserAssignedIdentity{
				PrincipalID: uai.Properties.PrincipalId,
				ClientID:    uai.Properties.ClientId,
			}
		}
	}
	site.Identity = out
	return nil
}

// webUserAssignedIdentity finds a user-assigned identity by its ARM ID,
// matching case-insensitively as ARM does.
func webUserAssignedIdentity(rid string) (UserAssignedIdentity, bool) {
	if uai, ok := azureManagedIdentities.Get(rid); ok {
		return uai, true
	}
	for _, uai := range azureManagedIdentities.List() {
		if strings.EqualFold(uai.ID, rid) {
			return uai, true
		}
	}
	return UserAssignedIdentity{}, false
}

// webSyncSiteIdentityPrincipal keeps the directory in step with a site's
// system-assigned identity: its service principal exists while the identity
// does, so role assignments and Graph reads resolve it.
func webSyncSiteIdentityPrincipal(prev *Site, site *Site) {
	if prev != nil && prev.Identity.systemAssigned() &&
		(site == nil || !site.Identity.systemAssigned() || site.Identity.PrincipalID != prev.Identity.PrincipalID) {
		entraUnregisterServicePrincipal(prev.Identity.PrincipalID)
	}
	if site != nil && site.Identity.systemAssigned() {
		entraRegisterServicePrincipal(site.Identity.PrincipalID, site.SystemIdentityClientID, site.Name, "ManagedIdentity")
	}
}

// webKeyVaultReferenceIdentity is the identity a site resolves Key Vault
// references with: SystemAssigned, or a user-assigned identity's resource ID.
func webKeyVaultReferenceIdentity(site *Site) string {
	if v := strings.TrimSpace(site.Properties.KeyVaultReferenceIdentity); v != "" {
		return v
	}
	return "SystemAssigned"
}

// webRequestedKVRefIdentity is the keyVaultReferenceIdentity a site PUT
// settles on: the one it names, else the site's current one, else
// SystemAssigned.
func webRequestedKVRefIdentity(requested string, prev *Site) string {
	if v := strings.TrimSpace(requested); v != "" {
		return v
	}
	if prev != nil && prev.Properties.KeyVaultReferenceIdentity != "" {
		return prev.Properties.KeyVaultReferenceIdentity
	}
	return "SystemAssigned"
}
