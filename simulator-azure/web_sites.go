package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerWebSites(srv *sim.Server, armBase string, sites sim.Store[Site], functionConfigs sim.Store[FunctionEnvelope]) {
	// PUT - Create or update function app
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		var req Site
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)

		// The resource provider takes a plan in the site's own resource group
		// by name, as az functionapp create names it, and records its ID.
		if plan := req.Properties.ServerFarmID; plan != "" && !strings.Contains(plan, "/") {
			req.Properties.ServerFarmID = armResourceID(sub, rg, "Microsoft.Web", "serverfarms", plan)
		}

		// A site placed in an App Service Environment is placed in one that
		// exists; the resource provider refuses an unresolvable reference.
		hostingEnvironment, err := webResolveHostingEnvironmentProfile(req.Properties.HostingEnvironmentProfile)
		if err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}

		kind := req.Kind
		if kind == "" {
			kind = webDefaultSiteKind(req.Properties.Reserved, req.Properties.ServerFarmID)
		}

		// Real Azure assigns a per-site hostname `<site>.azurewebsites.net`
		// — invocations route to the right function app by HTTP Host header.
		// The sim hosts every site on a single port, so callers connect to
		// the sim's TCP address but set Host = `<name>.azurewebsites.net`;
		// the invoke handler matches that against DefaultHostName.
		defaultHostName := name + ".azurewebsites.net"
		scmHostName := azureAppServiceScmHost(r, name)

		// Default the ARM-computed site properties the provider reads back
		// when the request omits them, so a post-apply GET echoes the same
		// values terraform expects (no idempotency drift). These mirror the
		// real Microsoft.Web/sites defaults.
		clientCertMode := req.Properties.ClientCertMode
		if clientCertMode == "" {
			clientCertMode = "Optional"
		}

		siteConfig := req.Properties.SiteConfig
		if siteConfig == nil {
			siteConfig = &SiteConfig{}
		}
		if siteConfig.LoadBalancing == "" {
			siteConfig.LoadBalancing = "LeastRequests"
		}
		if siteConfig.ManagedPipelineMode == "" {
			siteConfig.ManagedPipelineMode = "Integrated"
		}
		if siteConfig.IPSecurityRestrictionsDefaultAction == "" {
			siteConfig.IPSecurityRestrictionsDefaultAction = "Allow"
		}
		if siteConfig.MinTLSVersion == "" {
			siteConfig.MinTLSVersion = "1.2"
		}
		if siteConfig.ScmMinTLSVersion == "" {
			siteConfig.ScmMinTLSVersion = "1.2"
		}
		if siteConfig.ScmIPSecurityRestrictionsDefaultAction == "" {
			siteConfig.ScmIPSecurityRestrictionsDefaultAction = "Allow"
		}

		site := Site{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.Web/sites",
			Kind:     kind,
			Location: req.Location,
			Tags:     req.Tags,
			Properties: SiteProperties{
				State:                     "Running",
				DefaultHostName:           defaultHostName,
				HostNames:                 []string{defaultHostName},
				Enabled:                   true,
				EnabledHostNames:          []string{defaultHostName, scmHostName},
				HostNameSslStates:         siteHostNameSslStates(defaultHostName, scmHostName),
				ServerFarmID:              req.Properties.ServerFarmID,
				SKU:                       webPlanSKUFor(req.Properties.ServerFarmID),
				Reserved:                  req.Properties.Reserved,
				SiteConfig:                siteConfig,
				ResourceGroup:             rg,
				LastModifiedTime:          time.Now().UTC().Format(time.RFC3339),
				HTTPSOnly:                 req.Properties.HTTPSOnly,
				ClientCertEnabled:         req.Properties.ClientCertEnabled,
				ClientCertMode:            clientCertMode,
				ClientCertExclusionPaths:  req.Properties.ClientCertExclusionPaths,
				HostingEnvironmentProfile: hostingEnvironment,
			},
		}

		prev, existed := sites.Get(resourceID)
		site.Properties.SlotSwapStatus = prev.Properties.SlotSwapStatus
		connStrings, hasConnStrings, err := siteConfigConnStrings(siteConfig)
		if err != nil {
			AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		var prevRow *Site
		if existed {
			prevRow = &prev
		}
		webKeepScmType(siteConfig, prevRow)
		if err := webApplySiteIdentity(&site, req.Identity, prevRow); err != nil {
			AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		site.Properties.KeyVaultReferenceIdentity = webRequestedKVRefIdentity(req.Properties.KeyVaultReferenceIdentity, prevRow)
		webSyncSiteIdentityPrincipal(prevRow, &site)
		if existed {
			// state is read-only: an update leaves a stopped app stopped.
			site.Properties.State = prev.Properties.State
		} else {
			// A new app starts on an empty /home.
			removeSiteHome(name)
		}
		sites.Put(resourceID, site)
		if hasConnStrings {
			cfg, _ := siteConfigStore.Get(resourceID)
			cfg.ConnectionStrings = connStrings
			siteConfigStore.Put(resourceID, cfg)
		}
		// Real Azure provisions the Functions host key set (master key +
		// "default" host function key) with the new site.
		ensureWebHostKeys(resourceID)

		// terraform-provider-azurerm sends app settings inside the site PUT's
		// siteConfig.appSettings, then reads them back via POST
		// /config/appsettings/list (a separate store). Mirror them into that
		// store so the read recovers FUNCTIONS_EXTENSION_VERSION /
		// AzureWebJobsStorage / AzureWebJobsDashboard — the provider derives
		// functions_extension_version / storage_account_name /
		// builtin_logging_enabled from those, and drops them on drift otherwise.
		if len(siteConfig.AppSettings) > 0 {
			cfg, _ := siteConfigStore.Get(resourceID)
			settings := make(map[string]string, len(siteConfig.AppSettings))
			for _, kv := range siteConfig.AppSettings {
				settings[kv.Name] = kv.Value
			}
			cfg.AppSettings = settings
			siteConfigStore.Put(resourceID, cfg)
		}

		// virtualNetworkSubnetId on the envelope is regional VNet integration:
		// join the subnet's VNet exactly as a swift PUT would. A PUT that
		// omits it keeps an existing integration (the swift-connection flow
		// re-PUTs the site without it); the stored row still reflects the
		// current integration either way.
		if req.Properties.VirtualNetworkSubnetID != "" {
			if err := applySiteVirtualNetworkSubnetID(r, site, req.Properties.VirtualNetworkSubnetID); err != nil {
				writeVnetIntegrationError(w, name, err)
				return
			}
		} else {
			syncSiteVnetSubnetProperty(r)
		}
		site, _ = sites.Get(resourceID)
		if existed {
			restartAzureFunctionInstance(site)
		} else {
			startAlwaysOnSite(site)
		}

		// Always return 200 OK so the ARM SDK's BeginCreateOrUpdate poller
		// treats this as an immediately completed operation.
		sim.WriteJSON(w, http.StatusOK, site)
	})

	// GET - Get function app
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)

		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, site)
	})

	// GET - List function apps by resource group
	srv.HandleFunc("GET "+armBase+"/sites", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := armResourceID(sub, rg, "Microsoft.Web", "sites") + "/"

		filtered := sites.Filter(func(s Site) bool {
			return strings.HasPrefix(s.ID, prefix)
		})
		out := make([]Site, 0, len(filtered))
		out = append(out, filtered...)

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": out,
		})
	})

	// DELETE - Delete function app
	srv.HandleFunc("DELETE "+armBase+"/sites/{siteName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)

		deleted, existed := sites.Get(resourceID)
		if sites.Delete(resourceID) {
			if existed {
				// Retain the app so RestoreFromDeletedApp and the deletedSites
				// reads can reach it, before its content is cleaned up.
				webRecordDeletedSite(resourceID, deleted)
			}
			webSyncSiteIdentityPrincipal(&deleted, nil)
			stopAzureFunctionInstance(name)
			cleanupSiteContainers(resourceID, name)
			webCleanupSiteResources(resourceID)
			// Clean up associated functions
			funcs := functionConfigs.Filter(func(f FunctionEnvelope) bool {
				return strings.HasPrefix(f.ID, resourceID+"/functions/")
			})
			for _, f := range funcs {
				functionConfigs.Delete(f.ID)
			}

			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET - List functions
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/functions", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)

		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": siteFunctions(&site),
		})
	})

	// GET - Get function
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/functions/{functionName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		siteName := sim.PathParam(r, "siteName")
		funcName := sim.PathParam(r, "functionName")

		funcID := armResourceID(sub, rg, "Microsoft.Web", "sites", siteName, "functions", funcName)

		siteID := armResourceID(sub, rg, "Microsoft.Web", "sites", siteName)
		fn, ok := functionConfigs.Get(funcID)
		if site, isSite := sites.Get(siteID); isSite && !ok {
			fn, ok = siteFunction(&site, funcName)
		}
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The function '%s' in site '%s' was not found.", funcName, siteName)
			return
		}

		sim.WriteJSON(w, http.StatusOK, fn)
	})
}
