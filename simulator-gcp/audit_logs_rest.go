package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
)

// auditCaptureLimit bounds the request and response bodies an audit entry
// records; a larger body leaves the entry without it.
const auditCaptureLimit = 1 << 20

// registerAuditLogs audits the REST calls of the services that write Cloud
// Audit Logs. It wraps the whole route table, so it runs after every
// service's own front-end and middleware has been mounted.
func registerAuditLogs(srv *sim.Server) error {
	if err := auditLoadRPCs(); err != nil {
		return err
	}
	srv.WrapHandler(auditLogMiddleware)
	return nil
}

// auditPending is an audited REST call in flight: complete builds its record
// from the answer the handler gave.
type auditPending struct {
	serviceLabel    string
	captureRequest  bool
	captureResponse bool
	complete        func(status int, requestBody, responseBody []byte) (auditRecord, bool)
}

func auditLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname := lbplane.Hostname(r.Host)
		if isCloudRunHost(r.Host) || strings.HasSuffix(hostname, ".cloudfunctions.net") {
			next.ServeHTTP(w, r)
			return
		}
		pending, ok := auditResolveGCS(r)
		if !ok {
			pending, ok = auditResolveOnePlatform(r)
		}
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if host := gcpServiceFromHost(r); host != "" && host != pending.serviceLabel {
			next.ServeHTTP(w, r)
			return
		}
		at := time.Now()
		var requestBody []byte
		if pending.captureRequest && r.Body != nil {
			requestBody = auditCaptureRequest(r)
		}
		recorder := &auditResponseRecorder{ResponseWriter: w, capture: pending.captureResponse}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if status < 200 || (status >= 300 && status < 400) || status == http.StatusUnauthorized {
			return
		}
		var responseBody []byte
		if !recorder.overflow {
			responseBody = recorder.body.Bytes()
		}
		rec, ok := pending.complete(status, requestBody, responseBody)
		if !ok {
			return
		}
		rec.at = at
		rec.caller = auditCallerFromCredential(r.Header.Get("Authorization"), r.RemoteAddr, r.UserAgent())
		emitAuditLog(rec)
	})
}

// auditCaptureRequest reads the request body for the audit entry and leaves
// the handler an identical one.
func auditCaptureRequest(r *http.Request) []byte {
	original := r.Body
	head, err := io.ReadAll(io.LimitReader(original, auditCaptureLimit+1))
	if err != nil || len(head) > auditCaptureLimit {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), original), original}
		return nil
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{bytes.NewReader(head), original}
	return head
}

type auditResponseRecorder struct {
	http.ResponseWriter
	status   int
	capture  bool
	overflow bool
	body     bytes.Buffer
}

func (w *auditResponseRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditResponseRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.capture && !w.overflow {
		if w.body.Len()+len(b) > auditCaptureLimit {
			w.overflow = true
			w.body.Reset()
		} else {
			w.body.Write(b)
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *auditResponseRecorder) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *auditResponseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// auditHTTPStatusCodes are the google.rpc.Code values of the HTTP statuses an
// error answer without a status name carries.
var auditHTTPStatusCodes = map[int]codes.Code{
	http.StatusBadRequest:          codes.InvalidArgument,
	http.StatusForbidden:           codes.PermissionDenied,
	http.StatusNotFound:            codes.NotFound,
	http.StatusConflict:            codes.Aborted,
	http.StatusPreconditionFailed:  codes.FailedPrecondition,
	http.StatusTooManyRequests:     codes.ResourceExhausted,
	499:                            codes.Canceled,
	http.StatusNotImplemented:      codes.Unimplemented,
	http.StatusServiceUnavailable:  codes.Unavailable,
	http.StatusGatewayTimeout:      codes.DeadlineExceeded,
	http.StatusInternalServerError: codes.Internal,
}

// auditRESTStatus is the google.rpc.Status of a REST answer.
func auditRESTStatus(status int, body []byte) map[string]any {
	if status < 300 {
		return auditStatus(0, "")
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	code, ok := auditHTTPStatusCodes[status]
	if !ok {
		code = codes.Unknown
	}
	if envelope.Error.Status != "" {
		var named codes.Code
		if err := named.UnmarshalJSON([]byte(`"` + envelope.Error.Status + `"`)); err == nil {
			code = named
		}
	}
	return auditStatus(int(code), envelope.Error.Message)
}

// auditResolveOnePlatform resolves a REST call to an audited RPC through the
// RPC's google.api.http binding.
func auditResolveOnePlatform(r *http.Request) (*auditPending, bool) {
	binding, vars, ok := auditMatchREST(r.Method, r.URL.EscapedPath())
	if !ok {
		return nil, false
	}
	rpc := binding.rpc
	query := r.URL.Query()
	label, _, _ := strings.Cut(rpc.serviceName, ".")
	return &auditPending{
		serviceLabel:    label,
		captureRequest:  binding.body != "",
		captureResponse: rpc.logType == auditAdminWrite,
		complete: func(status int, requestBody, responseBody []byte) (auditRecord, bool) {
			input := rpc.method.Input()
			request := map[string]any{}
			if len(requestBody) > 0 {
				var body any
				if err := json.Unmarshal(requestBody, &body); err == nil {
					if fields, ok := body.(map[string]any); ok && binding.body == "*" {
						request = fields
					} else if binding.body != "*" {
						auditSetPath(request, auditJSONPath(input, binding.body), body)
					}
				}
			}
			for key, values := range query {
				if key == "alt" || key == "$alt" || key == "prettyPrint" || key == "key" || key == "access_token" {
					continue
				}
				if len(values) == 1 {
					auditSetPath(request, strings.Split(key, "."), values[0])
				} else {
					list := make([]any, len(values))
					for i, v := range values {
						list[i] = v
					}
					auditSetPath(request, strings.Split(key, "."), list)
				}
			}
			for field, value := range vars {
				auditSetPath(request, auditJSONPath(input, field), value)
			}
			var response map[string]any
			if status < 300 && len(responseBody) > 0 {
				_ = json.Unmarshal(responseBody, &response)
			}
			return auditOnePlatformRecord(rpc, request, response, auditRESTStatus(status, responseBody), auditCaller{}), true
		},
	}, true
}

// Cloud Storage writes its audit entries per JSON API method under names of
// its own: storage.buckets.create for buckets.insert, storage.objects.create
// for an upload, storage.setIamPermissions for buckets.setIamPolicy.
type auditGCSMethod struct {
	methodName string
	permission string
	logType    string
	object     bool
}

type auditGCSMatchKey struct{}

// auditGCSRoutes is the JSON API route table Cloud Storage audits.
var auditGCSRoutes = func() *http.ServeMux {
	mux := http.NewServeMux()
	route := func(pattern string, m auditGCSMethod) {
		mux.HandleFunc(pattern, func(_ http.ResponseWriter, r *http.Request) {
			if out, ok := r.Context().Value(auditGCSMatchKey{}).(*auditGCSMatch); ok {
				out.method, out.bucket, out.object, out.ok = m, r.PathValue("bucket"), r.PathValue("object"), true
			}
		})
	}
	bucketsCreate := auditGCSMethod{"storage.buckets.create", "storage.buckets.create", auditAdminWrite, false}
	bucketsUpdate := auditGCSMethod{"storage.buckets.update", "storage.buckets.update", auditAdminWrite, false}
	objectsCreate := auditGCSMethod{"storage.objects.create", "storage.objects.create", auditDataWrite, true}
	objectsGet := auditGCSMethod{"storage.objects.get", "storage.objects.get", auditDataRead, true}
	objectsUpdate := auditGCSMethod{"storage.objects.update", "storage.objects.update", auditDataWrite, true}
	route("POST /storage/v1/b", bucketsCreate)
	route("GET /storage/v1/b", auditGCSMethod{"storage.buckets.list", "storage.buckets.list", auditAdminRead, false})
	route("GET /storage/v1/b/{bucket}", auditGCSMethod{"storage.buckets.get", "storage.buckets.get", auditAdminRead, false})
	route("PATCH /storage/v1/b/{bucket}", bucketsUpdate)
	route("PUT /storage/v1/b/{bucket}", bucketsUpdate)
	route("DELETE /storage/v1/b/{bucket}", auditGCSMethod{"storage.buckets.delete", "storage.buckets.delete", auditAdminWrite, false})
	route("GET /storage/v1/b/{bucket}/iam", auditGCSMethod{"storage.getIamPermissions", "storage.buckets.getIamPolicy", auditAdminRead, false})
	route("PUT /storage/v1/b/{bucket}/iam", auditGCSMethod{"storage.setIamPermissions", "storage.buckets.setIamPolicy", auditAdminWrite, false})
	route("GET /storage/v1/b/{bucket}/o", auditGCSMethod{"storage.objects.list", "storage.objects.list", auditDataRead, false})
	route("GET /storage/v1/b/{bucket}/o/{object...}", objectsGet)
	route("GET /download/storage/v1/b/{bucket}/o/{object...}", objectsGet)
	route("PATCH /storage/v1/b/{bucket}/o/{object...}", objectsUpdate)
	route("PUT /storage/v1/b/{bucket}/o/{object...}", objectsUpdate)
	route("DELETE /storage/v1/b/{bucket}/o/{object...}", auditGCSMethod{"storage.objects.delete", "storage.objects.delete", auditDataWrite, true})
	route("POST /upload/storage/v1/b/{bucket}/o", objectsCreate)
	route("PUT /upload/storage/v1/b/{bucket}/o", objectsCreate)
	route("POST /resumable/upload/storage/v1/b/{bucket}/o", objectsCreate)
	route("PUT /resumable/upload/storage/v1/b/{bucket}/o", objectsCreate)
	return mux
}()

type auditGCSMatch struct {
	method auditGCSMethod
	bucket string
	object string
	ok     bool
}

type auditDiscardWriter struct{ header http.Header }

func (d *auditDiscardWriter) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *auditDiscardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *auditDiscardWriter) WriteHeader(int)             {}

// auditResolveGCS resolves a Cloud Storage JSON API call. It reads the
// bucket's project and location before the handler runs, since a delete
// removes them.
func auditResolveGCS(r *http.Request) (*auditPending, bool) {
	if !strings.HasPrefix(r.URL.Path, "/storage/v1/b") && !strings.Contains(r.URL.Path, "/upload/storage/v1/b/") &&
		!strings.HasPrefix(r.URL.Path, "/download/storage/v1/b/") {
		return nil, false
	}
	match := &auditGCSMatch{}
	auditGCSRoutes.ServeHTTP(&auditDiscardWriter{}, r.WithContext(context.WithValue(r.Context(), auditGCSMatchKey{}, match)))
	if !match.ok {
		return nil, false
	}
	m := match.method
	// The upload that opens a resumable session creates nothing; the request
	// that completes the session does.
	if m.methodName == "storage.objects.create" && r.Method == http.MethodPost && r.URL.Query().Get("uploadType") == "resumable" {
		return nil, false
	}
	var project, location string
	if bucket, ok := gcsBuckets.Get(match.bucket); ok {
		project = bucket.Project
		location, _ = bucket.Data["location"].(string)
	}
	if match.bucket == "" {
		project = r.URL.Query().Get("project")
	}
	return &auditPending{
		serviceLabel:    "storage",
		captureRequest:  m.methodName == "storage.buckets.create",
		captureResponse: m.methodName == "storage.buckets.create" || m.methodName == "storage.objects.create",
		complete: func(status int, requestBody, responseBody []byte) (auditRecord, bool) {
			bucket, object := match.bucket, match.object
			var requested, created struct {
				Name     string `json:"name"`
				Location string `json:"location"`
			}
			if len(requestBody) > 0 {
				_ = json.Unmarshal(requestBody, &requested)
			}
			if status < 300 && len(responseBody) > 0 {
				_ = json.Unmarshal(responseBody, &created)
			}
			switch m.methodName {
			case "storage.buckets.create":
				bucket, location = requested.Name, created.Location
			case "storage.objects.create":
				object = created.Name
			}
			if project == "" {
				return auditRecord{}, false
			}
			resourceName := "projects/_"
			if bucket != "" {
				resourceName += "/buckets/" + bucket
				if m.object && object != "" {
					resourceName += "/objects/" + object
				}
			}
			location = strings.ToLower(location)
			rec := auditRecord{
				project:      project,
				serviceName:  "storage.googleapis.com",
				methodName:   m.methodName,
				resourceName: resourceName,
				logType:      m.logType,
				permission:   m.permission,
				status:       auditRESTStatus(status, responseBody),
				location:     location,
				resource: &MonitoredResource{Type: "gcs_bucket", Labels: map[string]string{
					"project_id":  project,
					"bucket_name": bucket,
					"location":    location,
				}},
			}
			if location != "" {
				rec.currentLocations = []string{location}
			}
			return rec, true
		},
	}, true
}
