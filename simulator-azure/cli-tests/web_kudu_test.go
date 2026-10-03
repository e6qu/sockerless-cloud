package azure_cli_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the App Service SCM (Kudu) deployment API. The native
// deployment commands read the SCM hostname from the site's hostNameSslStates
// and send the artifact there themselves, with the publishing credentials as
// basic auth:
//
//	az webapp deploy --type zip           POST /api/publish?type=zip
//	az webapp deployment source config-zip POST /api/zipdeploy?isAsync=true
//
// and both then read GET /api/deployments/latest until Kudu reports the
// deployment complete; with --track-status, az webapp deploy then polls
// GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/deploymentStatus/{deploymentStatusId}
// until the restarted site runs the new content. az webapp log deployment
// list and show read GET /api/deployments and GET /api/deployments/{id}/log.

// startLoopbackProxy runs the HTTPS proxy the CLI reaches the simulator's
// `.localhost` hostnames through. The SCM hostname the simulator advertises is
// `<app>.scm.localhost:<port>`, and `.localhost` names do not resolve on every
// platform; the CLI honours HTTPS_PROXY, so the proxy carries each CONNECT to
// a `.localhost` name on the simulator's port to the simulator's loopback
// listener. TLS runs end to end through the tunnel, so the CLI verifies the
// simulator's certificate for the hostname it asked for.
func startLoopbackProxy(t *testing.T, simPort string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var conns sync.WaitGroup
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if r.Method != http.MethodConnect || err != nil || port != simPort ||
			(host != "localhost" && !strings.HasSuffix(host, ".localhost")) {
			http.Error(w, "this proxy reaches only the simulator's .localhost hostnames", http.StatusForbidden)
			return
		}
		upstream, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		client, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		conns.Add(2)
		go func() {
			defer conns.Done()
			_, _ = io.Copy(upstream, buf)
			_ = upstream.(*net.TCPConn).CloseWrite()
		}()
		go func() {
			defer conns.Done()
			_, _ = io.Copy(client, upstream)
			_ = client.Close()
		}()
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		conns.Wait()
	})
	return "http://" + ln.Addr().String()
}

// kuduCLIEnv is a logged-in CLI whose HTTPS traffic goes through the loopback
// proxy.
type kuduCLIEnv struct {
	azLoginEnv
	proxy string
}

func (e kuduCLIEnv) command(args ...string) *exec.Cmd {
	cmd := e.azLoginEnv.command(args...)
	cmd.Env = append(cmd.Env,
		"HTTPS_PROXY="+e.proxy, "https_proxy="+e.proxy,
		"HTTP_PROXY="+e.proxy, "http_proxy="+e.proxy,
		"NO_PROXY=", "no_proxy=",
	)
	return cmd
}

// kuduCLIWebApp starts a TLS simulator, logs the CLI in to it through the
// loopback proxy, and creates a NODE:20-lts web app, returning the CLI and
// the SCM host the app reports.
func kuduCLIWebApp(t *testing.T, rg, plan, app string) (kuduCLIEnv, string) {
	t.Helper()
	env := startAzTLSSimulator(t)
	port := env.baseURL[strings.LastIndex(env.baseURL, ":")+1:]
	az := kuduCLIEnv{azLoginEnv: env, proxy: startLoopbackProxy(t, port)}

	runCLI(t, az.command("cloud", "register", "-n", "sockerless-kudu",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, az.command("cloud", "set", "-n", "sockerless-kudu"))
	runCLI(t, az.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, az.command("logout")) })

	runCLI(t, az.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, az.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "B1", "-o", "json"))
	runCLI(t, az.command("webapp", "create", "-g", rg, "-p", plan, "-n", app,
		"--runtime", "NODE:20-lts", "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })

	// az webapp show prints the site's properties flattened.
	var site struct {
		HostNameSslStates []struct {
			Name     string `json:"name"`
			HostType string `json:"hostType"`
		} `json:"hostNameSslStates"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "show", "-g", rg, "-n", app, "-o", "json")), &site)
	var scmHost string
	for _, s := range site.HostNameSslStates {
		if s.HostType == "Repository" {
			scmHost = s.Name
		}
	}
	assert.Equal(t, app+".scm.localhost:"+port, scmHost, "the SCM site is advertised at the simulator's coordinate")
	return az, scmHost
}

func TestWebAppKudu_NativeAzDeployAndConfigZip(t *testing.T) {
	rg, app := "kudu-cli-rg", "kudu-cli-app"
	az, _ := kuduCLIWebApp(t, rg, "kudu-cli-plan", app)
	env := az.azLoginEnv

	dir := t.TempDir()
	writeZip := func(name string, files map[string]string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, stage3Zip(t, files), 0o644))
		return p
	}
	serverJS := `const fs = require("fs"), path = require("path");
require("http").createServer((req, res) => {
  fs.readFile(path.join(__dirname, req.url), (err, data) => {
    if (err) { res.statusCode = 404; res.end(String(err)); return; }
    res.end(data);
  });
}).listen(process.env.PORT);
`
	served := func(path string) string {
		return strings.TrimSpace(runCLI(t, az.command("rest", "--method", "GET", "--url", env.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header")))
	}

	runCLI(t, az.command("webapp", "deploy", "-g", rg, "-n", app,
		"--src-path", writeZip("v1.zip", map[string]string{"server.js": serverJS, "greeting.txt": "az webapp deploy"}),
		"--type", "zip", "--track-status", "true", "-o", "json"))
	assert.Equal(t, "az webapp deploy", served("/greeting.txt"))

	runCLI(t, az.command("webapp", "deployment", "source", "config-zip", "-g", rg, "-n", app,
		"--src", writeZip("v2.zip", map[string]string{"server.js": serverJS, "greeting.txt": "az webapp deployment source config-zip"}),
		"-o", "json"))
	assert.Equal(t, "az webapp deployment source config-zip", served("/greeting.txt"))

	// Kudu's own deployment list, read through the SCM site.
	var kuduDeployments []struct {
		ID       string `json:"id"`
		Deployer string `json:"deployer"`
		Status   int    `json:"status"`
		Active   bool   `json:"active"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "log", "deployment", "list", "-g", rg, "-n", app, "-o", "json")), &kuduDeployments)
	require.Len(t, kuduDeployments, 2)
	deployers := map[string]bool{}
	for _, d := range kuduDeployments {
		deployers[d.Deployer] = true
		assert.Equal(t, 4, d.Status)
	}
	assert.Equal(t, map[string]bool{"OneDeploy": true, "ZipDeploy": true}, deployers)
	assert.Contains(t, runCLI(t, az.command("webapp", "log", "deployment", "show", "-g", rg, "-n", app, "-o", "json")),
		"Deployment successful.")

	// Azure Resource Manager's deployment list proxies Kudu's.
	var armDeployments []struct {
		Name       string `json:"name"`
		Properties struct {
			Deployer string `json:"deployer"`
			Status   int    `json:"status"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, az.command("rest", "--method", "GET", "--url",
		fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/deployments?api-version=2025-03-01",
			env.baseURL, subscriptionID, rg, app),
		"--query", "value", "-o", "json")), &armDeployments)
	require.Len(t, armDeployments, 2)
	for _, d := range armDeployments {
		assert.True(t, deployers[d.Properties.Deployer], "ARM lists Kudu deployment %s", d.Name)
		assert.Equal(t, 4, d.Properties.Status)
	}
}
