package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// acrWorkloadRegistry is a registry credential a workload declares: an Azure
// Container Apps `registries` entry, or an App Service site's Azure
// Container Registry managed identity or `DOCKER_REGISTRY_SERVER_*` settings.
type acrWorkloadRegistry struct {
	Server   string
	Username string
	Password string
	Identity string
}

// acrWorkloadRegistryAuth is the credential an Azure workload host presents
// when it pulls image for a workload that declared registries: for a registry
// the workload reaches with a managed identity, an identity token of that
// identity, which the engine exchanges through the registry's refresh-token
// grant; for one it reaches with a username and password, that credential;
// and nothing for a registry the workload did not declare, which the host
// reaches anonymously, as the platform does.
func acrWorkloadRegistryAuth(image string, registries []acrWorkloadRegistry) string {
	host, _, found := strings.Cut(image, "/")
	if !found {
		return ""
	}
	for _, r := range registries {
		if r.Server == "" || !strings.EqualFold(acrBareHost(r.Server), acrBareHost(host)) {
			continue
		}
		if r.Identity != "" {
			reg, ok := acrRegistryForHost(r.Server)
			if !ok {
				return ""
			}
			token, err := acrMintRefreshToken(reg, r.Identity)
			if err != nil {
				return ""
			}
			return sim.RegistryIdentityToken(token)
		}
		if r.Username != "" {
			return sim.RegistryCredential(r.Username, r.Password)
		}
	}
	return ""
}

// acaAppWorkloadRegistries is what a Container App declares, its password
// references resolved against its secrets.
func acaAppWorkloadRegistries(app ContainerApp) []acrWorkloadRegistry {
	if app.Properties.Configuration == nil {
		return nil
	}
	secrets := map[string]string{}
	for _, s := range app.Properties.Configuration.Secrets {
		secrets[s.Name] = s.Value
	}
	var out []acrWorkloadRegistry
	for _, r := range app.Properties.Configuration.Registries {
		out = append(out, acrWorkloadRegistry{Server: r.Server, Username: r.Username, Password: secrets[r.PasswordSecretRef], Identity: r.Identity})
	}
	return out
}

// acaJobWorkloadRegistries is what a Container Apps Job declares.
func acaJobWorkloadRegistries(cfg *JobConfiguration) []acrWorkloadRegistry {
	if cfg == nil {
		return nil
	}
	secrets := map[string]string{}
	for _, s := range cfg.Secrets {
		secrets[s.Name] = s.Value
	}
	var out []acrWorkloadRegistry
	for _, r := range cfg.Registries {
		out = append(out, acrWorkloadRegistry{Server: r.Server, Username: r.Username, Password: secrets[r.PasswordSecretRef], Identity: r.Identity})
	}
	return out
}

// siteWorkloadRegistries is what an App Service site declares for its
// container image: the registry its `DOCKER_REGISTRY_SERVER_*` settings name,
// reached with the site's Azure Container Registry managed identity when the
// site config asks for it, else with the settings' username and password.
func siteWorkloadRegistries(site *Site, image string) []acrWorkloadRegistry {
	settings := siteAppSettings(site)
	server := settings["DOCKER_REGISTRY_SERVER_URL"]
	server = strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	if server == "" {
		if host, _, found := strings.Cut(image, "/"); found {
			server = host
		}
	}
	cfg := site.Properties.SiteConfig
	if cfg != nil && cfg.AcrUseManagedIdentityCreds {
		identity := cfg.AcrUserManagedIdentityID
		if identity == "" {
			identity = "system"
		}
		return []acrWorkloadRegistry{{Server: server, Identity: identity}}
	}
	if user := settings["DOCKER_REGISTRY_SERVER_USERNAME"]; user != "" {
		return []acrWorkloadRegistry{{Server: server, Username: user, Password: settings["DOCKER_REGISTRY_SERVER_PASSWORD"]}}
	}
	return nil
}
