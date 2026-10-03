package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/simjwt"
)

// cloudRunDefaultRequestTimeout is the request timeout a revision template
// that sets none gets.
const cloudRunDefaultRequestTimeout = 300 * time.Second

// cloudRunServiceURI is the run.app URL Cloud Run serves a service on,
// https://<service>-<hash>-<region>.a.run.app, where the hash is fixed per
// project.
func cloudRunServiceURI(project, location, serviceID string) string {
	sum := sha256.Sum256([]byte(project))
	hash := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))[:10]
	return fmt.Sprintf("https://%s-%s-%s.a.run.app", serviceID, hash, location)
}

// isCloudRunHost reports whether a Host header names a run.app host. No Google
// API is served on run.app, so such a request is always for a workload.
func isCloudRunHost(host string) bool {
	return strings.HasSuffix(lbplane.Hostname(host), ".run.app")
}

var cloudRunServicesByHost sim.GenerationIndex[ServiceV2]

// cloudRunServiceByHost returns the service whose URL a Host header names.
func cloudRunServiceByHost(host string) (ServiceV2, bool) {
	hostname := lbplane.Hostname(host)
	if hostname == "" || crv2Services == nil {
		return ServiceV2{}, false
	}
	return cloudRunServicesByHost.Lookup(crv2Services, hostname, func(s ServiceV2) []string {
		if s.URI == "" {
			return nil
		}
		parsed, err := url.Parse(s.URI)
		if err != nil {
			return nil
		}
		return []string{lbplane.Hostname(parsed.Host)}
	})
}

// registerCloudRunFrontEnd serves each Cloud Run service on its run.app URL:
// a request whose Host is a service's host goes, with its method, path, query,
// headers and body, to the service's ingress container, which is started if no
// instance is running, and the container's answer comes back untouched. The
// simulator serves every host on its one endpoint, so it dispatches on the
// Host header.
func registerCloudRunFrontEnd(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isCloudRunHost(r.Host) {
				next.ServeHTTP(w, r)
				return
			}
			svc, ok := cloudRunServiceByHost(r.Host)
			if !ok {
				cloudRunFrontEndError(w, http.StatusNotFound, "The requested URL was not found on this server.")
				return
			}
			serveCloudRunService(w, r, svc)
		})
	})
}

// cloudRunFrontEndError writes the plain error page Google's front end answers
// with; it is not a Google API error, so it carries no JSON error envelope.
// message is HTML.
func cloudRunFrontEndError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<html><head>\n<meta http-equiv=\"content-type\" content=\"text/html;charset=utf-8\">\n"+
		"<title>%d %s</title>\n</head>\n<body text=#000000 bgcolor=#ffffff>\n<h1>Error: %s</h1>\n<h2>%s</h2>\n<h2></h2>\n</body></html>\n",
		status, http.StatusText(status), http.StatusText(status), message)
}

// cloudRunRequestTimeout is the revision template's request timeout.
func cloudRunRequestTimeout(svc ServiceV2) time.Duration {
	if svc.Template != nil && svc.Template.Timeout != "" {
		if d, err := time.ParseDuration(svc.Template.Timeout); err == nil && d > 0 {
			return d
		}
	}
	return cloudRunDefaultRequestTimeout
}

// cloudRunStartupBound is the longest a container's startup probe can take
// before it either succeeds or fails the container.
func cloudRunStartupBound(c Container) time.Duration {
	p := cloudRunStartupProbe(c)
	return time.Duration(p.InitialDelaySeconds)*time.Second +
		time.Duration(p.FailureThreshold)*time.Duration(max(p.PeriodSeconds, p.TimeoutSeconds))*time.Second
}

// serveCloudRunService serves a request to a service. An ID token is admitted
// when its audience is the service's URL, one of its custom audiences, or one
// of urls, the further URLs the request reached the service on.
func serveCloudRunService(w http.ResponseWriter, r *http.Request, svc ServiceV2, urls ...string) {
	if !cloudRunInvokerAuthorized(w, r, svc, urls) {
		return
	}
	if svc.Template == nil || len(svc.Template.Containers) == 0 || svc.Template.Containers[0].Image == "" {
		cloudRunFrontEndError(w, http.StatusServiceUnavailable, "The service has no container to serve the request.")
		return
	}
	containers := svc.Template.Containers
	requestTimeout := cloudRunRequestTimeout(svc)
	startup := cloudRunStartupBound(containers[0])
	for _, sidecar := range containers[1:] {
		if sidecar.StartupProbe != nil {
			startup += cloudRunStartupBound(sidecar)
		}
	}
	sim.DeclareWait(r.Context(), startup+requestTimeout)

	serviceID := svc.Name[strings.LastIndex(svc.Name, "/")+1:]
	sink := &cfLogSink{project: resourceProject(svc.Name), functionName: serviceID}
	inst, err := ensureCloudRunServiceInstance(r.Context(), svc.Name, serviceID, containers, svc.Template.Volumes, sink)
	if err != nil {
		cloudRunFrontEndError(w, http.StatusServiceUnavailable, html.EscapeString(fmt.Sprintf("The service could not start an instance: %v", err)))
		return
	}
	address, err := inst.awaitReady(r.Context())
	if err != nil {
		if r.Context().Err() == nil {
			deleteCloudRunServiceInstanceIf(svc.Name, inst)
		}
		cloudRunFrontEndError(w, http.StatusServiceUnavailable, html.EscapeString(fmt.Sprintf("The instance failed to start: %v", err)))
		return
	}
	err = lbplane.Forward(w, r, lbplane.Upstream{
		Scheme:   "http",
		Address:  address,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Header:   cloudRunForwardedHeader(r.Header),
		Timeout:  requestTimeout,
	})
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	case errors.Is(err, context.DeadlineExceeded):
		cloudRunFrontEndError(w, http.StatusGatewayTimeout, "upstream request timeout")
	default:
		if !inst.ingressRunning() {
			deleteCloudRunServiceInstanceIf(svc.Name, inst)
		}
		cloudRunFrontEndError(w, http.StatusServiceUnavailable, html.EscapeString(fmt.Sprintf("The instance did not answer: %v", err)))
	}
}

// cloudRunForwardedHeader is the request header a container receives. Cloud
// Run passes on the header that carried the caller's credential with a JWT's
// signature replaced, so the container can read the claims but cannot replay
// the token; an Authorization header beside X-Serverless-Authorization belongs
// to the application and passes untouched.
func cloudRunForwardedHeader(in http.Header) http.Header {
	header := in.Clone()
	name := cloudRunCredentialHeader(header)
	auth := header.Get(name)
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return header
	}
	parts := strings.Split(strings.TrimSpace(auth[len(prefix):]), ".")
	if len(parts) == 3 {
		parts[2] = "SIGNATURE_REMOVED_BY_GOOGLE"
		header.Set(name, prefix+strings.Join(parts, "."))
	}
	return header
}

// cloudRunCredentialHeader names the header Cloud Run authenticates a request
// with: X-Serverless-Authorization when the caller sends it, which frees
// Authorization for the application's own scheme, and Authorization otherwise.
func cloudRunCredentialHeader(header http.Header) string {
	if header.Get("X-Serverless-Authorization") != "" {
		return "X-Serverless-Authorization"
	}
	return "Authorization"
}

// cloudRunIDTokenClaims are the claims of a Google-signed ID token the front
// end reads to name the caller.
type cloudRunIDTokenClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
}

// cloudRunInvokerAuthorized admits a request to a service. A service whose
// invoker IAM check is disabled admits everyone, and one whose policy, or a
// policy it inherits from its project, folders and organization, grants
// run.routes.invoke to allUsers admits a caller without a credential. Any
// other request carries a Google-signed ID token whose audience is the
// service's URL or one of its custom audiences — an OAuth access token is not
// one — and the principal the token names holds run.routes.invoke on the
// service, conditions on its bindings included.
func cloudRunInvokerAuthorized(w http.ResponseWriter, r *http.Request, svc ServiceV2, urls []string) bool {
	if svc.InvokerIamDisabled {
		return true
	}
	resource := gcpIAMResourceNamed(svc.Name)
	policies := gcpHierarchyPolicies(resourceProject(svc.Name))
	if store := gcpResourceIAMStore(); store != nil {
		if own, ok := store.Get(svc.Name); ok {
			policies = append(policies, own)
		}
	}
	mayInvoke := func(principal string, owner bool) bool {
		return len(gcpPermissionsHeldUnder(principal, owner, policies, []string{"run.routes.invoke"}, resource)) == 1
	}
	if mayInvoke("", false) {
		return true
	}
	forbidden := fmt.Sprintf("Your client does not have permission to get URL <code>%s</code> from this server.",
		html.EscapeString(r.URL.Path))
	auth := r.Header.Get(cloudRunCredentialHeader(r.Header))
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		cloudRunFrontEndError(w, http.StatusForbidden, forbidden)
		return false
	}
	claims, ok := cloudRunVerifyIDToken(strings.TrimSpace(auth[len(prefix):]), svc, urls)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token" error_description="The access token could not be verified"`)
		cloudRunFrontEndError(w, http.StatusUnauthorized, fmt.Sprintf(
			"Your client does not have permission to the requested URL <code>%s</code>.", html.EscapeString(r.URL.Path)))
		return false
	}
	subject := claims.Email
	if subject == "" {
		subject = claims.Sub
	}
	if !mayInvoke(gcpSubjectPrincipal(subject)) {
		cloudRunFrontEndError(w, http.StatusForbidden, forbidden)
		return false
	}
	return true
}

// cloudRunVerifyIDToken verifies an ID token Google signed for the service:
// one whose audience is the service's URL or one of urls, with or without
// its trailing slash, or one of the service's custom audiences.
func cloudRunVerifyIDToken(token string, svc ServiceV2, urls []string) (cloudRunIDTokenClaims, bool) {
	if accessSigner == nil {
		return cloudRunIDTokenClaims{}, false
	}
	var audiences []string
	for _, u := range append([]string{svc.URI}, urls...) {
		audiences = append(audiences, u, strings.TrimRight(u, "/")+"/")
	}
	audiences = append(audiences, svc.CustomAudiences...)
	for _, audience := range audiences {
		if audience == "" {
			continue
		}
		var claims cloudRunIDTokenClaims
		if simjwt.Verify(token, &claims, simjwt.Options{
			Issuer:        googleTokenIssuer,
			Audience:      audience,
			RequireExpiry: true,
		}, accessSigner) == nil {
			return claims, true
		}
	}
	return cloudRunIDTokenClaims{}, false
}

// postToCloudRunService delivers a push to a service this simulator serves.
// Google delivers from inside its own network, where run.app resolves to the
// front end, so the request reaches the service's front end directly rather
// than through this host's resolver.
func postToCloudRunService(ctx context.Context, svc ServiceV2, req delivery.Request, classify delivery.Classifier) delivery.Outcome {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return delivery.Permanent(fmt.Errorf("build request to %s: %w", req.URL, err))
	}
	httpReq.RequestURI = httpReq.URL.RequestURI()
	for name, values := range req.Header {
		httpReq.Header[name] = append([]string(nil), values...)
	}
	recorder := httptest.NewRecorder()
	serveCloudRunService(recorder, httpReq, svc)
	resp := recorder.Result()
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if ctx.Err() != nil {
		return delivery.Retryable(fmt.Errorf("post to %s: %w", req.URL, ctx.Err()))
	}
	switch classify(resp.StatusCode) {
	case delivery.Accept:
		return delivery.Delivered().WithStatus(resp.StatusCode)
	case delivery.Reject:
		return delivery.Permanent(fmt.Errorf("%s answered %d", req.URL, resp.StatusCode)).WithStatus(resp.StatusCode)
	default:
		return delivery.Retryable(fmt.Errorf("%s answered %d", req.URL, resp.StatusCode)).WithStatus(resp.StatusCode)
	}
}

// deliverPush posts a push delivery, reaching a Cloud Run service this
// simulator serves at its front end.
func deliverPush(ctx context.Context, req delivery.Request, classify delivery.Classifier) delivery.Outcome {
	if parsed, err := url.Parse(req.URL); err == nil && isCloudRunHost(parsed.Host) {
		if svc, ok := cloudRunServiceByHost(parsed.Host); ok {
			return postToCloudRunService(ctx, svc, req, classify)
		}
	}
	return delivery.Post(ctx, req, classify)
}
