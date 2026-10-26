package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// WebApps_GetSitePhpErrorLogFlag reports log_errors and log_errors_max_len in
// local (the site's configuration) and master (the platform php.ini) form,
// which `php -i` prints as "name => local value => master value", so the
// simulator asks the PHP in the site's container. A container without PHP runs
// no PHP worker and has no such settings, which it says rather than defaulting
// them.

type webPHPDirectives struct {
	Local, Master string
}

// False when the workload has no PHP.
func webReadPHPDirectives(instance placement.Handle, wanted ...string) (map[string]webPHPDirectives, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := instance.Exec(ctx, []string{"php", "-i"})
	if err != nil || result.ExitCode != 0 {
		// No PHP in the container, so no setting to report.
		return nil, false
	}

	want := map[string]bool{}
	for _, name := range wanted {
		want[name] = true
	}
	out := map[string]webPHPDirectives{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		// "directive => local value => master value"; a single-valued directive prints
		// once.
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

func webGetSitePhpErrorLogFlag(w http.ResponseWriter, r *http.Request) {
	instance, ok := webRequireInstance(w, r)
	if !ok {
		return
	}
	directives, hasPHP := webReadPHPDirectives(instance, "log_errors", "log_errors_max_len")
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
