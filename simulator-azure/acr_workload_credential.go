package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// acrWorkloadRegistry is a registry credential a workload declares: a Container
// Apps `registries` entry, or an App Service site's managed identity or
// `DOCKER_REGISTRY_SERVER_*` settings.
type acrWorkloadRegistry struct {
	Server   string
	Username string
	Password string
	Identity string
}

// acrWorkloadRegistryAuth returns, for a registry reached with a managed
// identity, an identity token the engine exchanges through the refresh-token
// grant; for one reached with a password, that credential; and nothing for an
// undeclared registry, which the host reaches anonymously, as the platform
// does.
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

// acaAppWorkloadRegistries resolves password references against the app's
// secrets.
func acaAppWorkloadRegistries(app ContainerApp, secrets map[string]string) []acrWorkloadRegistry {
	if app.Properties.Configuration == nil {
		return nil
	}
	var out []acrWorkloadRegistry
	for _, r := range app.Properties.Configuration.Registries {
		out = append(out, acrWorkloadRegistry{Server: r.Server, Username: r.Username, Password: secrets[r.PasswordSecretRef], Identity: r.Identity})
	}
	return out
}

// acaJobWorkloadRegistries resolves password references against the job's
// secrets.
func acaJobWorkloadRegistries(cfg *JobConfiguration, secrets map[string]string) []acrWorkloadRegistry {
	if cfg == nil {
		return nil
	}
	var out []acrWorkloadRegistry
	for _, r := range cfg.Registries {
		out = append(out, acrWorkloadRegistry{Server: r.Server, Username: r.Username, Password: secrets[r.PasswordSecretRef], Identity: r.Identity})
	}
	return out
}

// siteWorkloadRegistries uses the site's managed identity when the site config
// asks for it, else the settings' username and password.
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
