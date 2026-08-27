package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Run v1 services slice. Shipped for parity completeness — the
// GCP simulator should cover every API surface sockerless's cloud
// offers on the runner path. Sockerless uses v2 Cloud Run Jobs today;
// services v1 (Knative-style) is here so `gcloud run services *` and
// `google_cloud_run_v2_service` terraform resources round-trip against
// the simulator. Real API (namespaces methods ride the
// /apis/serving.knative.dev/v1/... path, per the cloudrun-v1 Discovery
// document's flatPaths):
//   https://cloud.google.com/run/docs/reference/rest/v1/namespaces.services

// CRService represents a Cloud Run v1 service (Knative shape).
type CRService struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   CRServiceMetadata `json:"metadata"`
	Spec       CRServiceSpec     `json:"spec"`
	Status     *CRServiceStatus  `json:"status,omitempty"`
}

// crStatusTraffic derives status.traffic from the client-supplied spec.traffic
// (echoing the requested split, resolving latestRevision→the new revision)
// rather than discarding it; with no split requested it defaults to 100% latest.
func crStatusTraffic(specTraffic []CRTraffic, latestRev string) []CRTraffic {
	if len(specTraffic) == 0 {
		return []CRTraffic{{RevisionName: latestRev, Percent: 100, LatestRevision: true}}
	}
	out := make([]CRTraffic, len(specTraffic))
	for i, t := range specTraffic {
		out[i] = t
		if t.LatestRevision && out[i].RevisionName == "" {
			out[i].RevisionName = latestRev
		}
	}
	return out
}

type CRServiceMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
}

type CRServiceSpec struct {
	Template *CRServiceTemplate `json:"template,omitempty"`
	Traffic  []CRTraffic        `json:"traffic,omitempty"`
}

type CRServiceTemplate struct {
	Metadata *CRServiceMetadata `json:"metadata,omitempty"`
	Spec     *CRTemplateSpec    `json:"spec,omitempty"`
}

type CRTemplateSpec struct {
	Containers         []CRContainer     `json:"containers,omitempty"`
	Volumes            []CRVolume        `json:"volumes,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	TimeoutSeconds     int64             `json:"timeoutSeconds,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
}

type CRContainer struct {
	Name         string                  `json:"name,omitempty"`
	Image        string                  `json:"image"`
	Command      []string                `json:"command,omitempty"`
	Args         []string                `json:"args,omitempty"`
	Env          []CREnvVar              `json:"env,omitempty"`
	Ports        []CRPort                `json:"ports,omitempty"`
	Resources    *CRResourceRequirements `json:"resources,omitempty"`
	VolumeMounts []CRVolumeMount         `json:"volumeMounts,omitempty"`
	WorkingDir   string                  `json:"workingDir,omitempty"`
}

type CREnvVar struct {
	Name      string          `json:"name"`
	Value     string          `json:"value,omitempty"`
	ValueFrom *CREnvVarSource `json:"valueFrom,omitempty"`
}

// CREnvVarSource mirrors the Discovery EnvVarSource schema — where an
// environment variable's value comes from when it is not a literal. Cloud Run
// supports only secretKeyRef.
type CREnvVarSource struct {
	SecretKeyRef *CRSecretKeySelector `json:"secretKeyRef,omitempty"`
}

// CRSecretKeySelector mirrors the Discovery SecretKeySelector schema: `name`
// is the Secret Manager secret and `key` the version, where v2's
// SecretKeySelector calls them `secret` and `version`.
type CRSecretKeySelector struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
}

type CRPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int32  `json:"containerPort"`
}

type CRTraffic struct {
	RevisionName   string `json:"revisionName,omitempty"`
	Percent        int32  `json:"percent,omitempty"`
	Tag            string `json:"tag,omitempty"`
	LatestRevision bool   `json:"latestRevision,omitempty"`
}

// CRServiceStatus is the status block returned on every Cloud Run
// service describe. `URL` and `Address.URL` are the canonical
// run.app invocation endpoints real Cloud Run emits
// (`https://<service>-<hash>-<region>.a.run.app`). The sim emits a
// canonical-shape URL on every describe so SDK consumers see what
// they expect, but the sim's actual invocation surface is at its
// own configured endpoint via the `/v2-services-invoke/...` path —
// NOT at the advertised run.app URL. Operators wanting to invoke a
// sim service do so via the documented endpoint, not by following
// the URL field. Per the `sim-emitted-url-roundtrip` skill's
// "document external" branch.
type CRServiceStatus struct {
	ObservedGeneration        int64         `json:"observedGeneration,omitempty"`
	LatestReadyRevisionName   string        `json:"latestReadyRevisionName,omitempty"`
	LatestCreatedRevisionName string        `json:"latestCreatedRevisionName,omitempty"`
	URL                       string        `json:"url,omitempty"` // external: canonical run.app URL; sim invokes via /v2-services-invoke at the configured endpoint
	Address                   *CRAddress    `json:"address,omitempty"`
	Conditions                []CRCondition `json:"conditions,omitempty"`
	Traffic                   []CRTraffic   `json:"traffic,omitempty"`
}

type CRAddress struct {
	URL string `json:"url"` // external: cluster-internal DNS form `http://<svc>.<ns>.svc.cluster.local`; sim does not implement Knative cluster-internal routing
}

type CRCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// CRServiceList mirrors the Discovery ListServicesResponse schema.
type CRServiceList struct {
	APIVersion  string      `json:"apiVersion"`
	Kind        string      `json:"kind"`
	Metadata    *CRListMeta `json:"metadata,omitempty"`
	Items       []CRService `json:"items"`
	Unreachable []string    `json:"unreachable,omitempty"`
}

// CRConfiguration is the Knative Configuration resource. Cloud Run
// auto-creates one Configuration per Service (the Configuration owns the
// rolling sequence of Revisions). Shape per the cloudrun-v1 Discovery
// `Configuration`/`ConfigurationSpec`/`ConfigurationStatus` schemas.
type CRConfiguration struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   CRServiceMetadata      `json:"metadata"`
	Spec       *CRConfigurationSpec   `json:"spec,omitempty"`
	Status     *CRConfigurationStatus `json:"status,omitempty"`
}

type CRConfigurationSpec struct {
	Template *CRServiceTemplate `json:"template,omitempty"`
}

type CRConfigurationStatus struct {
	ObservedGeneration        int64         `json:"observedGeneration,omitempty"`
	LatestCreatedRevisionName string        `json:"latestCreatedRevisionName,omitempty"`
	LatestReadyRevisionName   string        `json:"latestReadyRevisionName,omitempty"`
	Conditions                []CRCondition `json:"conditions,omitempty"`
}

// CRRevision is the Knative Revision resource — an immutable snapshot of a
// Configuration's container spec. Shape per the cloudrun-v1 Discovery
// `Revision`/`RevisionSpec`/`RevisionStatus` schemas.
type CRRevision struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   CRServiceMetadata `json:"metadata"`
	Spec       *CRRevisionSpec   `json:"spec,omitempty"`
	Status     *CRRevisionStatus `json:"status,omitempty"`
}

type CRRevisionSpec struct {
	Containers         []CRContainer `json:"containers,omitempty"`
	TimeoutSeconds     int64         `json:"timeoutSeconds,omitempty"`
	ServiceAccountName string        `json:"serviceAccountName,omitempty"`
}

type CRRevisionStatus struct {
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
	ServiceName        string        `json:"serviceName,omitempty"`
	ImageDigest        string        `json:"imageDigest,omitempty"`
	LogURL             string        `json:"logUrl,omitempty"`
	DesiredReplicas    int32         `json:"desiredReplicas,omitempty"`
	Conditions         []CRCondition `json:"conditions,omitempty"`
}

// CRRoute is the Knative Route resource — the traffic-routing surface
// auto-created alongside the Service. Shape per the cloudrun-v1 Discovery
// `Route`/`RouteSpec`/`RouteStatus` schemas.
type CRRoute struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   CRServiceMetadata `json:"metadata"`
	Spec       *CRRouteSpec      `json:"spec,omitempty"`
	Status     *CRRouteStatus    `json:"status,omitempty"`
}

type CRRouteSpec struct {
	Traffic []CRTraffic `json:"traffic,omitempty"`
}

type CRRouteStatus struct {
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
	URL                string        `json:"url,omitempty"`
	Address            *CRAddress    `json:"address,omitempty"`
	Traffic            []CRTraffic   `json:"traffic,omitempty"`
	Conditions         []CRCondition `json:"conditions,omitempty"`
}

// CRDomainMapping is the Knative DomainMapping resource (the
// domains.cloudrun.com API group). Shape per the cloudrun-v1 Discovery
// `DomainMapping`/`DomainMappingSpec`/`DomainMappingStatus` schemas.
type CRDomainMapping struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   CRServiceMetadata      `json:"metadata"`
	Spec       *CRDomainMappingSpec   `json:"spec,omitempty"`
	Status     *CRDomainMappingStatus `json:"status,omitempty"`
}

type CRDomainMappingSpec struct {
	RouteName       string `json:"routeName,omitempty"`
	CertificateMode string `json:"certificateMode,omitempty"`
	ForceOverride   bool   `json:"forceOverride,omitempty"`
}

type CRDomainMappingStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	MappedRouteName    string             `json:"mappedRouteName,omitempty"`
	URL                string             `json:"url,omitempty"`
	ResourceRecords    []CRResourceRecord `json:"resourceRecords,omitempty"`
	Conditions         []CRCondition      `json:"conditions,omitempty"`
}

type CRResourceRecord struct {
	Name   string `json:"name,omitempty"`
	Rrdata string `json:"rrdata,omitempty"`
	Type   string `json:"type,omitempty"`
}

// Knative list-response shapes. There is no nextPageToken: the
// serving.knative.dev List responses carry their paging cursor in
// `metadata.continue`, and the regions a partial list could not reach in
// `unreachable`.
type CRConfigurationList struct {
	APIVersion  string            `json:"apiVersion"`
	Kind        string            `json:"kind"`
	Metadata    *CRListMeta       `json:"metadata,omitempty"`
	Items       []CRConfiguration `json:"items"`
	Unreachable []string          `json:"unreachable,omitempty"`
}

type CRRevisionList struct {
	APIVersion  string       `json:"apiVersion"`
	Kind        string       `json:"kind"`
	Metadata    *CRListMeta  `json:"metadata,omitempty"`
	Items       []CRRevision `json:"items"`
	Unreachable []string     `json:"unreachable,omitempty"`
}

type CRRouteList struct {
	APIVersion  string      `json:"apiVersion"`
	Kind        string      `json:"kind"`
	Metadata    *CRListMeta `json:"metadata,omitempty"`
	Items       []CRRoute   `json:"items"`
	Unreachable []string    `json:"unreachable,omitempty"`
}

type CRDomainMappingList struct {
	APIVersion  string            `json:"apiVersion"`
	Kind        string            `json:"kind"`
	Metadata    *CRListMeta       `json:"metadata,omitempty"`
	Items       []CRDomainMapping `json:"items"`
	Unreachable []string          `json:"unreachable,omitempty"`
}

// knativeResource is implemented by the serving.knative.dev and
// domains.cloudrun.com resources whose collections share the Knative list
// envelope: each carries the ObjectMeta that names it, places it in a
// namespace, and holds the labels a `labelSelector` selects on.
type knativeResource interface {
	knativeMeta() CRServiceMetadata
}

func (s CRService) knativeMeta() CRServiceMetadata        { return s.Metadata }
func (c CRConfiguration) knativeMeta() CRServiceMetadata  { return c.Metadata }
func (rev CRRevision) knativeMeta() CRServiceMetadata     { return rev.Metadata }
func (rt CRRoute) knativeMeta() CRServiceMetadata         { return rt.Metadata }
func (dm CRDomainMapping) knativeMeta() CRServiceMetadata { return dm.Metadata }

// knativeCollectionPage narrows a stored Knative collection to one namespace,
// applies the request's `labelSelector`, orders what is left by resource name
// so the cursor is stable across requests, and applies the `limit`/`continue`
// cursor. It returns the page and the token that continues it.
func knativeCollectionPage[T knativeResource](w http.ResponseWriter, r *http.Request, all []T, namespace string) ([]T, string, bool) {
	selector := r.URL.Query().Get("labelSelector")
	items := make([]T, 0, len(all))
	for _, item := range all {
		meta := item.knativeMeta()
		if meta.Namespace != namespace || !knativeLabelSelectorMatches(selector, meta.Labels) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].knativeMeta().Name < items[j].knativeMeta().Name
	})
	return knativeListPage(w, r, items)
}

// CRAuthorizedDomain mirrors the Discovery `AuthorizedDomain` schema.
type CRAuthorizedDomain struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func registerCloudRun(srv *sim.Server) {
	services := sim.MakeStore[CRService](srv.DB(), "cloudrun_services")
	crv1Services = services
	configurations := sim.MakeStore[CRConfiguration](srv.DB(), "cloudrun_v1_configurations")
	revisions := sim.MakeStore[CRRevision](srv.DB(), "cloudrun_v1_revisions")
	routes := sim.MakeStore[CRRoute](srv.DB(), "cloudrun_v1_routes")
	domainmappings := sim.MakeStore[CRDomainMapping](srv.DB(), "cloudrun_v1_domainmappings")

	svcKey := func(namespace, name string) string {
		return namespace + "/" + name
	}

	// reconcileKnativeChildren mirrors Cloud Run's server-side Knative
	// reconciliation: creating or replacing a Service materializes the
	// owned Configuration, Route, and the new Revision. The sim records
	// real child resources (rather than synthesizing them on read) so
	// get/list over configurations/revisions/routes return faithful data.
	reconcileKnativeChildren := func(namespace string, svc CRService, revName string) {
		now := time.Now().UTC().Format(time.RFC3339)
		key := svcKey(namespace, svc.Metadata.Name)
		var tmplSpec *CRTemplateSpec
		if svc.Spec.Template != nil {
			tmplSpec = svc.Spec.Template.Spec
		}
		var containers []CRContainer
		var timeout int64
		var sa string
		if tmplSpec != nil {
			containers = tmplSpec.Containers
			timeout = tmplSpec.TimeoutSeconds
			sa = tmplSpec.ServiceAccountName
		}
		meta := func(name string) CRServiceMetadata {
			return CRServiceMetadata{
				Name:              name,
				Namespace:         namespace,
				UID:               generateUUID(),
				Generation:        svc.Metadata.Generation,
				ResourceVersion:   svc.Metadata.ResourceVersion,
				Labels:            svc.Metadata.Labels,
				CreationTimestamp: now,
			}
		}
		ready := []CRCondition{{Type: "Ready", Status: "True", LastTransitionTime: now}}

		configurations.Put(key, CRConfiguration{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "Configuration",
			Metadata:   meta(svc.Metadata.Name),
			Spec:       &CRConfigurationSpec{Template: svc.Spec.Template},
			Status: &CRConfigurationStatus{
				ObservedGeneration:        svc.Metadata.Generation,
				LatestCreatedRevisionName: revName,
				LatestReadyRevisionName:   revName,
				Conditions:                ready,
			},
		})

		routes.Put(key, CRRoute{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "Route",
			Metadata:   meta(svc.Metadata.Name),
			Spec:       &CRRouteSpec{Traffic: svc.Spec.Traffic},
			Status: &CRRouteStatus{
				ObservedGeneration: svc.Metadata.Generation,
				URL:                fmt.Sprintf("https://%s-%s.run.app", svc.Metadata.Name, namespace),
				Address:            &CRAddress{URL: fmt.Sprintf("http://%s.%s.svc.cluster.local", svc.Metadata.Name, namespace)},
				Traffic:            crStatusTraffic(svc.Spec.Traffic, revName),
				Conditions:         ready,
			},
		})

		revisions.Put(svcKey(namespace, revName), CRRevision{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "Revision",
			Metadata:   meta(revName),
			Spec: &CRRevisionSpec{
				Containers:         containers,
				TimeoutSeconds:     timeout,
				ServiceAccountName: sa,
			},
			Status: &CRRevisionStatus{
				ObservedGeneration: svc.Metadata.Generation,
				ServiceName:        svc.Metadata.Name,
				DesiredReplicas:    1,
				Conditions:         ready,
			},
		})
	}
	crv1ReconcileChildren = reconcileKnativeChildren
	crv1DeleteChildren = func(namespace, name string) {
		key := svcKey(namespace, name)
		configurations.Delete(key)
		routes.Delete(key)
		revPrefix := key + "-"
		for _, revision := range revisions.List() {
			revisionKey := svcKey(revision.Metadata.Namespace, revision.Metadata.Name)
			if strings.HasPrefix(revisionKey, revPrefix) {
				revisions.Delete(revisionKey)
			}
		}
	}

	// CreateService: POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services
	srv.HandleFunc("POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")

		var svc CRService
		if err := sim.ReadJSON(r, &svc); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid service body: %v", err)
			return
		}
		if svc.Metadata.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "metadata.name is required", "INVALID_ARGUMENT")
			return
		}
		// Namespace in URL wins over body.
		svc.Metadata.Namespace = namespace
		key := svcKey(namespace, svc.Metadata.Name)

		if _, exists := services.Get(key); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"service %q already exists in namespace %q", svc.Metadata.Name, namespace)
			return
		}

		svc.APIVersion = "serving.knative.dev/v1"
		svc.Kind = "Service"
		svc.Metadata.UID = generateUUID()
		svc.Metadata.Generation = 1
		svc.Metadata.ResourceVersion = "1"
		svc.Metadata.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)

		svc.Status = &CRServiceStatus{
			ObservedGeneration:        1,
			LatestReadyRevisionName:   svc.Metadata.Name + "-00001",
			LatestCreatedRevisionName: svc.Metadata.Name + "-00001",
			URL:                       fmt.Sprintf("https://%s-%s.run.app", svc.Metadata.Name, namespace),
			Address:                   &CRAddress{URL: fmt.Sprintf("http://%s.%s.svc.cluster.local", svc.Metadata.Name, namespace)},
			Conditions: []CRCondition{{
				Type:               "Ready",
				Status:             "True",
				LastTransitionTime: time.Now().UTC().Format(time.RFC3339),
			}},
			Traffic: crStatusTraffic(svc.Spec.Traffic, svc.Metadata.Name+"-00001"),
		}

		services.Put(key, svc)
		reconcileKnativeChildren(namespace, svc, svc.Metadata.Name+"-00001")
		projectCloudRunV1ToV2(svc, namespace, cloudRunDefaultLocation)
		sim.WriteJSON(w, http.StatusOK, svc)
	})

	// GetService: GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		svc, ok := services.Get(svcKey(namespace, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"service %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, svc)
	})

	// ListServices: GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services", func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeCollectionPage(w, r, services.List(), sim.PathParam(r, "namespace"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRServiceList{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "ServiceList",
			Metadata:   knativeListMeta(next),
			Items:      page,
		})
	})

	// ReplaceService: PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}
	srv.HandleFunc("PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		key := svcKey(namespace, name)

		existing, ok := services.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"service %q not found in namespace %q", name, namespace)
			return
		}

		var update CRService
		if err := sim.ReadJSON(r, &update); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid service body: %v", err)
			return
		}
		if !knativeReplaceAllowed(w, "service", name, namespace,
			knativeResourceVersion(existing.Metadata.Generation), update.Metadata.ResourceVersion) {
			return
		}
		update.APIVersion = "serving.knative.dev/v1"
		update.Kind = "Service"
		update.Metadata.Name = name
		update.Metadata.Namespace = namespace
		update.Metadata.UID = existing.Metadata.UID
		update.Metadata.Generation = existing.Metadata.Generation + 1
		update.Metadata.ResourceVersion = fmt.Sprintf("%d", update.Metadata.Generation)
		update.Metadata.CreationTimestamp = existing.Metadata.CreationTimestamp
		revName := fmt.Sprintf("%s-%05d", name, update.Metadata.Generation)
		update.Status = &CRServiceStatus{
			ObservedGeneration:        update.Metadata.Generation,
			LatestReadyRevisionName:   revName,
			LatestCreatedRevisionName: revName,
			URL:                       fmt.Sprintf("https://%s-%s.run.app", name, namespace),
			Address:                   &CRAddress{URL: fmt.Sprintf("http://%s.%s.svc.cluster.local", name, namespace)},
			Conditions: []CRCondition{{
				Type:               "Ready",
				Status:             "True",
				LastTransitionTime: time.Now().UTC().Format(time.RFC3339),
			}},
			Traffic: crStatusTraffic(update.Spec.Traffic, revName),
		}

		services.Put(key, update)
		reconcileKnativeChildren(namespace, update, revName)
		projectCloudRunV1ToV2(update, namespace, cloudRunDefaultLocation)
		sim.WriteJSON(w, http.StatusOK, update)
	})

	// DeleteService: DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}
	srv.HandleFunc("DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		if !services.Delete(svcKey(namespace, name)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"service %q not found in namespace %q", name, namespace)
			return
		}
		deleteCloudRunServiceProjections(namespace, cloudRunDefaultLocation, name)
		key := svcKey(namespace, name)
		configurations.Delete(key)
		routes.Delete(key)
		revPrefix := key + "-"
		for _, rev := range revisions.List() {
			if strings.HasPrefix(svcKey(rev.Metadata.Namespace, rev.Metadata.Name), revPrefix) {
				revisions.Delete(svcKey(rev.Metadata.Namespace, rev.Metadata.Name))
			}
		}
		// Knative DELETE returns a Status object. The cloudrun-v1
		// Discovery Status schema declares no apiVersion/kind members —
		// only code/details/message/metadata/reason/status.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "Success",
		})
	})

	// --- Knative Configurations (read-only; auto-created with the Service) ---

	getConfiguration := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		cfg, ok := configurations.Get(svcKey(namespace, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"configuration %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, cfg)
	}
	listConfigurations := func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeCollectionPage(w, r, configurations.List(), sim.PathParam(r, "namespace"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRConfigurationList{
			APIVersion: "serving.knative.dev/v1", Kind: "ConfigurationList",
			Metadata: knativeListMeta(next), Items: page,
		})
	}
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}", getConfiguration)
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations", listConfigurations)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/configurations/{name}", getConfiguration)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/configurations", listConfigurations)

	getRevision := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		rev, ok := revisions.Get(svcKey(namespace, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"revision %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rev)
	}
	listRevisions := func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeCollectionPage(w, r, revisions.List(), sim.PathParam(r, "namespace"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRRevisionList{
			APIVersion: "serving.knative.dev/v1", Kind: "RevisionList",
			Metadata: knativeListMeta(next), Items: page,
		})
	}
	deleteRevision := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		if !revisions.Delete(svcKey(namespace, name)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"revision %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Success"})
	}
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}", getRevision)
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions", listRevisions)
	srv.HandleFunc("DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}", deleteRevision)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/revisions/{name}", getRevision)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/revisions", listRevisions)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}", deleteRevision)

	getRoute := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		rt, ok := routes.Get(svcKey(namespace, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"route %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rt)
	}
	listRoutes := func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeCollectionPage(w, r, routes.List(), sim.PathParam(r, "namespace"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRRouteList{
			APIVersion: "serving.knative.dev/v1", Kind: "RouteList",
			Metadata: knativeListMeta(next), Items: page,
		})
	}
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}", getRoute)
	srv.HandleFunc("GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes", listRoutes)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/routes/{name}", getRoute)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/routes", listRoutes)

	// --- Knative DomainMappings (create/get/list/delete; domains.cloudrun.com group) ---

	createDomainMapping := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		var dm CRDomainMapping
		if err := sim.ReadJSON(r, &dm); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid domain mapping body: %v", err)
			return
		}
		if dm.Metadata.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "metadata.name is required", "INVALID_ARGUMENT")
			return
		}
		dm.Metadata.Namespace = namespace
		key := svcKey(namespace, dm.Metadata.Name)
		if _, exists := domainmappings.Get(key); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"domain mapping %q already exists in namespace %q", dm.Metadata.Name, namespace)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		dm.APIVersion = "domains.cloudrun.com/v1"
		dm.Kind = "DomainMapping"
		dm.Metadata.UID = generateUUID()
		dm.Metadata.Generation = 1
		dm.Metadata.ResourceVersion = "1"
		dm.Metadata.CreationTimestamp = now
		routeName := ""
		if dm.Spec != nil {
			routeName = dm.Spec.RouteName
		}
		dm.Status = &CRDomainMappingStatus{
			ObservedGeneration: 1,
			MappedRouteName:    routeName,
			URL:                "https://" + dm.Metadata.Name,
			ResourceRecords: []CRResourceRecord{
				{Name: dm.Metadata.Name, Rrdata: "ghs.googlehosted.com.", Type: "CNAME"},
			},
			Conditions: []CRCondition{{Type: "Ready", Status: "True", LastTransitionTime: now}},
		}
		domainmappings.Put(key, dm)
		sim.WriteJSON(w, http.StatusOK, dm)
	}
	getDomainMapping := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		dm, ok := domainmappings.Get(svcKey(namespace, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"domain mapping %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, dm)
	}
	listDomainMappings := func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeCollectionPage(w, r, domainmappings.List(), sim.PathParam(r, "namespace"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRDomainMappingList{
			APIVersion: "domains.cloudrun.com/v1", Kind: "DomainMappingList",
			Metadata: knativeListMeta(next), Items: page,
		})
	}
	deleteDomainMapping := func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		if !domainmappings.Delete(svcKey(namespace, name)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"domain mapping %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Success"})
	}
	srv.HandleFunc("POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings", createDomainMapping)
	srv.HandleFunc("GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}", getDomainMapping)
	srv.HandleFunc("GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings", listDomainMappings)
	srv.HandleFunc("DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}", deleteDomainMapping)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{namespace}/domainmappings", createDomainMapping)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}", getDomainMapping)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/domainmappings", listDomainMappings)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}", deleteDomainMapping)

	listAuthorizedDomains := func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"domains": []CRAuthorizedDomain{},
		})
	}
	srv.HandleFunc("GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains", listAuthorizedDomains)
	srv.HandleFunc("GET /v1/projects/{project}/authorizeddomains", listAuthorizedDomains)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/authorizeddomains", listAuthorizedDomains)

	// --- Per-location Service mirrors (v1/projects/.../locations/.../services) ---
	// Real Cloud Run exposes the same Knative Service surface under the
	// regional path; the sim maps the {location} segment to the Knative
	// namespace so a service created under either spelling round-trips.

	srv.HandleFunc("POST /v1/projects/{project}/locations/{namespace}/services", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		var svc CRService
		if err := sim.ReadJSON(r, &svc); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid service body: %v", err)
			return
		}
		if svc.Metadata.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "metadata.name is required", "INVALID_ARGUMENT")
			return
		}
		svc.Metadata.Namespace = namespace
		key := svcKey(namespace, svc.Metadata.Name)
		if _, exists := services.Get(key); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"service %q already exists in namespace %q", svc.Metadata.Name, namespace)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		svc.APIVersion = "serving.knative.dev/v1"
		svc.Kind = "Service"
		svc.Metadata.UID = generateUUID()
		svc.Metadata.Generation = 1
		svc.Metadata.ResourceVersion = "1"
		svc.Metadata.CreationTimestamp = now
		revName := svc.Metadata.Name + "-00001"
		svc.Status = &CRServiceStatus{
			ObservedGeneration:        1,
			LatestReadyRevisionName:   revName,
			LatestCreatedRevisionName: revName,
			URL:                       fmt.Sprintf("https://%s-%s.run.app", svc.Metadata.Name, namespace),
			Address:                   &CRAddress{URL: fmt.Sprintf("http://%s.%s.svc.cluster.local", svc.Metadata.Name, namespace)},
			Conditions:                []CRCondition{{Type: "Ready", Status: "True", LastTransitionTime: now}},
			Traffic:                   crStatusTraffic(svc.Spec.Traffic, revName),
		}
		services.Put(key, svc)
		reconcileKnativeChildren(namespace, svc, revName)
		projectCloudRunV1ToV2(svc, sim.PathParam(r, "project"), namespace)
		sim.WriteJSON(w, http.StatusOK, svc)
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		// Service IAM verbs ride the {name} segment as "<svc>:getIamPolicy".
		if id, action, found := strings.Cut(name, ":"); found {
			handleResourceIAM(w, r, gcpResourceIAMStore(),
				fmt.Sprintf("projects/%s/locations/%s/services/%s", sim.PathParam(r, "project"), namespace, id), action)
			return
		}
		svc, ok := services.Get(svcKey(namespace, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"service %q not found in namespace %q", name, namespace)
			return
		}
		sim.WriteJSON(w, http.StatusOK, svc)
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{namespace}/services", func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeCollectionPage(w, r, services.List(), sim.PathParam(r, "namespace"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRServiceList{
			APIVersion: "serving.knative.dev/v1", Kind: "ServiceList",
			Metadata: knativeListMeta(next), Items: page,
		})
	})
	srv.HandleFunc("PUT /v1/projects/{project}/locations/{namespace}/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		key := svcKey(namespace, name)
		existing, ok := services.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"service %q not found in namespace %q", name, namespace)
			return
		}
		var update CRService
		if err := sim.ReadJSON(r, &update); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid service body: %v", err)
			return
		}
		if !knativeReplaceAllowed(w, "service", name, namespace,
			knativeResourceVersion(existing.Metadata.Generation), update.Metadata.ResourceVersion) {
			return
		}
		update.APIVersion = "serving.knative.dev/v1"
		update.Kind = "Service"
		update.Metadata.Name = name
		update.Metadata.Namespace = namespace
		update.Metadata.UID = existing.Metadata.UID
		update.Metadata.Generation = existing.Metadata.Generation + 1
		update.Metadata.ResourceVersion = fmt.Sprintf("%d", update.Metadata.Generation)
		update.Metadata.CreationTimestamp = existing.Metadata.CreationTimestamp
		revName := fmt.Sprintf("%s-%05d", name, update.Metadata.Generation)
		now := time.Now().UTC().Format(time.RFC3339)
		update.Status = &CRServiceStatus{
			ObservedGeneration:        update.Metadata.Generation,
			LatestReadyRevisionName:   revName,
			LatestCreatedRevisionName: revName,
			URL:                       fmt.Sprintf("https://%s-%s.run.app", name, namespace),
			Address:                   &CRAddress{URL: fmt.Sprintf("http://%s.%s.svc.cluster.local", name, namespace)},
			Conditions:                []CRCondition{{Type: "Ready", Status: "True", LastTransitionTime: now}},
			Traffic:                   crStatusTraffic(update.Spec.Traffic, revName),
		}
		services.Put(key, update)
		reconcileKnativeChildren(namespace, update, revName)
		projectCloudRunV1ToV2(update, sim.PathParam(r, "project"), namespace)
		sim.WriteJSON(w, http.StatusOK, update)
	})
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{namespace}/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		key := svcKey(namespace, name)
		if !services.Delete(key) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"service %q not found in namespace %q", name, namespace)
			return
		}
		deleteCloudRunServiceProjections(sim.PathParam(r, "project"), namespace, name)
		configurations.Delete(key)
		routes.Delete(key)
		revPrefix := key + "-"
		for _, rev := range revisions.List() {
			if strings.HasPrefix(svcKey(rev.Metadata.Namespace, rev.Metadata.Name), revPrefix) {
				revisions.Delete(svcKey(rev.Metadata.Namespace, rev.Metadata.Name))
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Success"})
	})

	// --- Service IAM verbs (getIamPolicy via {name}:verb GET; set/test via POST) ---

	srv.HandleFunc("POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		namespace := sim.PathParam(r, "namespace")
		nameAction := sim.PathParam(r, "nameAction")
		id, action, found := strings.Cut(nameAction, ":")
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on service %q", nameAction)
			return
		}
		handleResourceIAM(w, r, gcpResourceIAMStore(),
			fmt.Sprintf("projects/%s/locations/%s/services/%s", project, namespace, id), action)
	})

	// GET /v1/projects/{project}/locations (list) is served by registerEventarc.
}
