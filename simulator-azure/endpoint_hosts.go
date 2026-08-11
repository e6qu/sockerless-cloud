package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

type azureAdvertisedEndpointConfig struct {
	Storage    map[string]string `json:"storage,omitempty"`
	KeyVault   string            `json:"keyVault,omitempty"`
	ServiceBus string            `json:"serviceBus,omitempty"`
	EventGrid  string            `json:"eventGrid,omitempty"`
	ACR        string            `json:"acr,omitempty"`
}

var (
	azureAdvertisedEndpointsOnce sync.Once
	azureAdvertisedEndpoints     azureAdvertisedEndpointConfig
	azureAdvertisedEndpointsErr  error
)

// azureRequestScheme reports the scheme the CLIENT used, which is what every
// absolute URL the simulator emits (long-running-operation polling targets,
// Key Vault endpoints, advertised data-plane hosts) must carry. Behind a TLS
// terminating gateway r.TLS is nil even though the client spoke HTTPS, so the
// forwarded-proto header the gateway sets is authoritative when present.
func azureRequestScheme(r *http.Request) string {
	if fp := r.Header.Get("X-Forwarded-Proto"); fp != "" {
		if i := strings.IndexByte(fp, ','); i >= 0 {
			fp = fp[:i] // first hop is the client-facing scheme
		}
		return strings.ToLower(strings.TrimSpace(fp))
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func azureRequestHostParts(r *http.Request) (string, string) {
	host := r.Host
	if h, p, err := net.SplitHostPort(host); err == nil {
		return h, ":" + p
	}
	if strings.Count(host, ":") == 1 {
		if i := strings.LastIndex(host, ":"); i >= 0 {
			return host[:i], host[i:]
		}
	}
	return host, ""
}

func azureEndpointHost(r *http.Request, parts ...string) string {
	hostname, portSuffix := azureRequestHostParts(r)
	return strings.Join(append(parts, hostname), ".") + portSuffix
}

func azureEndpointHostname(r *http.Request, parts ...string) string {
	hostname, _ := azureRequestHostParts(r)
	return strings.Join(append(parts, hostname), ".")
}

func azureAdvertisedEndpointConfigFromEnv() (azureAdvertisedEndpointConfig, error) {
	azureAdvertisedEndpointsOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON"))
		if raw == "" {
			return
		}
		if err := json.Unmarshal([]byte(raw), &azureAdvertisedEndpoints); err != nil {
			azureAdvertisedEndpointsErr = fmt.Errorf("parse SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON: %w", err)
			return
		}
	})
	return azureAdvertisedEndpoints, azureAdvertisedEndpointsErr
}

func azureEndpointTemplateVars(r *http.Request, name string, extra map[string]string) map[string]string {
	hostname, portSuffix := azureRequestHostParts(r)
	vars := map[string]string{
		"scheme":    azureRequestScheme(r),
		"host":      r.Host,
		"hostname":  hostname,
		"port":      strings.TrimPrefix(portSuffix, ":"),
		"name":      name,
		"account":   name,
		"vault":     name,
		"namespace": name,
		"topic":     name,
	}
	for k, v := range extra {
		vars[k] = v
	}
	return vars
}

func azureApplyEndpointTemplate(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

func azureEnsureTrailingSlash(endpoint string) string {
	if endpoint == "" || strings.HasSuffix(endpoint, "/") {
		return endpoint
	}
	return endpoint + "/"
}

func azureStorageEndpointURL(r *http.Request, account, service string) string {
	if cfg, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
		panic(err)
	} else if cfg.Storage != nil {
		if tmpl := strings.TrimSpace(cfg.Storage[service]); tmpl != "" {
			return azureEnsureTrailingSlash(azureApplyEndpointTemplate(tmpl, azureEndpointTemplateVars(r, account, map[string]string{
				"service": service,
			})))
		}
	}
	// A request that already arrived at the service's own hostname is
	// addressing the very endpoint being reported, so the endpoint is the host
	// it used. Prefixing the subdomain again would advertise
	// `acct.file.acct.file.<host>`, a name that resolves nowhere.
	if hostname, _ := azureRequestHostParts(r); strings.HasPrefix(hostname, account+"."+service+".") {
		return fmt.Sprintf("%s://%s/", azureRequestScheme(r), r.Host)
	}
	return fmt.Sprintf("%s://%s/", azureRequestScheme(r), azureEndpointHost(r, account, service))
}

func azureKeyVaultEndpointURL(r *http.Request, vault string) string {
	if cfg, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
		panic(err)
	} else if tmpl := strings.TrimSpace(cfg.KeyVault); tmpl != "" {
		return azureEnsureTrailingSlash(azureApplyEndpointTemplate(tmpl, azureEndpointTemplateVars(r, vault, nil)))
	}
	return fmt.Sprintf("https://%s/", azureEndpointHost(r, vault, "vault"))
}

func azureServiceBusEndpointURL(r *http.Request, namespace string) string {
	if cfg, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
		panic(err)
	} else if tmpl := strings.TrimSpace(cfg.ServiceBus); tmpl != "" {
		return azureEnsureTrailingSlash(azureApplyEndpointTemplate(tmpl, azureEndpointTemplateVars(r, namespace, nil)))
	}
	return fmt.Sprintf("%s://%s/", azureRequestScheme(r), azureEndpointHost(r, namespace, "servicebus"))
}

func azureEventGridEndpointURL(r *http.Request, topic string) string {
	if cfg, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
		panic(err)
	} else if tmpl := strings.TrimSpace(cfg.EventGrid); tmpl != "" {
		return azureEnsureTrailingSlash(azureApplyEndpointTemplate(tmpl, azureEndpointTemplateVars(r, topic, nil)))
	}
	return ""
}

// azureACRLoginServer returns the host Azure Container Registry's control
// plane advertises as a registry's `loginServer` (a bare host, never a URL —
// real ACR returns e.g. `myregistry.azurecr.io`, not a scheme-prefixed
// value). Like the other data planes, the host is derived from the request
// so a client reaches the simulator at the coordinate it just used for ARM,
// unless an external gateway coordinate is configured.
func azureACRLoginServer(r *http.Request, name string) string {
	if cfg, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
		panic(err)
	} else if tmpl := strings.TrimSpace(cfg.ACR); tmpl != "" {
		applied := azureApplyEndpointTemplate(tmpl, azureEndpointTemplateVars(r, name, nil))
		if u, err := url.Parse(applied); err == nil && u.Host != "" {
			return u.Host
		}
		return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(applied, "https://"), "http://"), "/")
	}
	return azureEndpointHost(r, name, "azurecr")
}

func azureServiceBusConnectionEndpoint(r *http.Request, namespace string) string {
	endpoint := azureServiceBusEndpointURL(r, namespace)
	u, err := url.Parse(endpoint)
	if err == nil && u.Host != "" {
		u.Scheme = "sb"
		u.RawQuery = ""
		u.Fragment = ""
		return azureEnsureTrailingSlash(u.String())
	}
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	host = strings.TrimPrefix(host, "sb://")
	return "sb://" + azureEnsureTrailingSlash(host)
}

func azureEndpointSuffix(endpoint string, labels ...string) string {
	u, err := url.Parse(endpoint)
	host := endpoint
	if err == nil && u.Host != "" {
		host = u.Host
	}
	host = strings.TrimSuffix(host, "/")
	prefix := strings.Join(labels, ".") + "."
	if strings.HasPrefix(host, prefix) {
		return strings.TrimPrefix(host, prefix)
	}
	return host
}
