package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The site's PHP error-logging flag, read from the PHP the site is running.
//
// WebApps_GetSitePhpErrorLogFlag reports four values: log_errors and
// log_errors_max_len, each in its local form (what the site's own
// configuration sets) and its master form (what the platform's php.ini sets).
// PHP itself reports exactly that distinction — `php -i` prints every directive
// as "name => local value => master value" — so a site whose workload container
// runs PHP already holds the answer, and the simulator reads it out of that
// container rather than describing a platform image it does not have.
//
// A site whose container has no PHP is not running a PHP worker, and there is
// no such configuration to report. That is not a gap in the simulator: it is
// the true answer for that site, and it is said rather than defaulted, because
// a settings resource with plausible values in it claims the site is configured
// that way.

// webPHPDirectives is the pair of values PHP reports for one directive.
type webPHPDirectives struct {
	Local, Master string
}

// webReadPHPDirectives runs PHP's own configuration dump inside the site's
// container and returns the directives it reports. The second result is false
// when the container has no PHP to ask.
func webReadPHPDirectives(containerID string, wanted ...string) (map[string]webPHPDirectives, bool) {
	cli := sim.DockerClient()
	if cli == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := cli.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{
		Cmd: []string{"php", "-i"}, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return nil, false
	}
	attached, err := cli.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return nil, false
	}
	output, _ := io.ReadAll(attached.Reader)
	attached.Close()
	inspected, err := cli.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil || inspected.ExitCode != 0 {
		// A container without PHP answers "executable file not found"; there is
		// no PHP worker to report a setting for.
		return nil, false
	}

	want := map[string]bool{}
	for _, name := range wanted {
		want[name] = true
	}
	out := map[string]webPHPDirectives{}
	for _, line := range strings.Split(string(output), "\n") {
		// php -i prints "directive => local value => master value"; a directive
		// with one value prints it once.
		parts := strings.Split(line, "=>")
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if !want[name] {
			continue
		}
		local := strings.TrimSpace(parts[1])
		master := local
		if len(parts) >= 3 {
			master = strings.TrimSpace(parts[2])
		}
		out[name] = webPHPDirectives{Local: local, Master: master}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// webGetSitePhpErrorLogFlag answers WebApps_GetSitePhpErrorLogFlag and its slot
// spelling.
func webGetSitePhpErrorLogFlag(w http.ResponseWriter, r *http.Request) {
	containerID, ok := webRequireInstance(w, r)
	if !ok {
		return
	}
	directives, hasPHP := webReadPHPDirectives(containerID, "log_errors", "log_errors_max_len")
	if !hasPHP {
		AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"WebApps_GetSitePhpErrorLogFlag is not implemented for this site: the flag "+
				"reports PHP's own log_errors and log_errors_max_len settings, and the "+
				"site's workload container runs no PHP for the simulator to read them "+
				"from. A site running PHP answers with the values that PHP reports.")
		return
	}
	errors := directives["log_errors"]
	maxLength := directives["log_errors_max_len"]
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":   webResourceID(r) + "/phplogging",
		"name": "phplogging",
		"type": "Microsoft.Web/sites/phplogging",
		"properties": map[string]any{
			"localLogErrors":           errors.Local,
			"masterLogErrors":          errors.Master,
			"localLogErrorsMaxLength":  maxLength.Local,
			"masterLogErrorsMaxLength": maxLength.Master,
		},
	})
}
