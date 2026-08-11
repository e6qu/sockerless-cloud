package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cloud Run Worker Pools and Instances from the vendor CLI.
//
// What `gcloud run worker-pools` reaches, and what it does not:
//
//   - The IAM commands (`get-iam-policy`, `set-iam-policy`,
//     `add-iam-policy-binding`, `remove-iam-policy-binding`) are declarative
//     commands bound to the `run.projects.locations.workerpools` collection.
//     They call the Cloud Run Admin v1 IAM triple at
//     `/v1/projects/{p}/locations/{l}/workerpools/{id}:{verb}` on the global
//     endpoint, which addresses the same worker pool the v2 collection does.
//     Those are driven here as real gcloud invocations.
//   - `deploy`, `describe`, `delete`, `update`, `update-instance-split`,
//     `replace` and `list --region` resolve the *regional* Cloud Run host
//     (`{region}-run.googleapis.com`) and speak the Knative surface at
//     `/apis/run.googleapis.com/v1/namespaces/{ns}/workerpools`. gcloud builds
//     that host by prefixing the configured endpoint with `{region}-`, which
//     no endpoint coordinate can point at a loopback simulator, and the
//     simulator does not serve the Knative worker-pool collection. Those
//     commands therefore have no CLI coverage; the v2 collection they would
//     otherwise reach is covered by the SDK tests and the wire round-trips
//     below.
//   - Cloud Run *Instances* have a `gcloud alpha run instances` group whose
//     IAM commands reach the simulator; they are driven in
//     run_instances_iam_test.go. Its lifecycle commands resolve the regional
//     host like the worker-pool ones do, so the instance collection itself is
//     exercised through the raw v2 wire here and through the official clients
//     in sdk-tests.
//   - `scaling.scalingMode`, `scaling.minInstanceCount` and
//     `scaling.maxInstanceCount` are members the pinned Cloud Run Discovery
//     revision does not publish, so the Discovery-generated Go client cannot
//     carry them (see specs/SIM_SURFACE_TABLES/gcp-cloudrun.md). gcloud's own
//     generated Cloud Run v2 client and the google Terraform provider both
//     model them; they are round-tripped over the raw v2 wire below and driven
//     end to end by the terraform-tests worker pool.

func workerPoolsBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/workerPools", baseURL, project, location)
}

func workerPoolURL(name string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/workerPools/%s", baseURL, project, location, name)
}

func instancesBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/instances", baseURL, project, location)
}

func instanceURL(name string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/instances/%s", baseURL, project, location, name)
}

// createWorkerPoolForCLI deploys a worker pool over the v2 collection and
// registers its teardown. gcloud's own deploy command cannot reach the
// simulator (see the file comment), so the resource the CLI commands act on is
// created through the same v2 API the Cloud Run backend uses.
func createWorkerPoolForCLI(t *testing.T, id string) {
	t.Helper()
	body := `{"template": {"containers": [{"image": "gcr.io/test-project/` + id + `"}]}}`
	httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId="+id, body)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", workerPoolURL(id), "")
		if err == nil {
			resp.Body.Close()
		}
	})
}

func TestCloudRunWorkerPools_CLI_IAMPolicyLifecycle(t *testing.T) {
	const id = "cli-wp-iam"
	createWorkerPoolForCLI(t, id)

	// A fresh worker pool starts with no bindings.
	out := runCLI(t, gcloudCLI("run", "worker-pools", "get-iam-policy", id,
		"--region="+location, "--format=json"))
	var initial struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
		Etag string `json:"etag"`
	}
	parseJSON(t, out, &initial)
	assert.Empty(t, initial.Bindings, "a newly deployed worker pool has no IAM bindings")
	assert.NotEmpty(t, initial.Etag, "getIamPolicy always returns an etag")

	// add-iam-policy-binding reads, mutates and writes the policy back.
	added := runCLI(t, gcloudCLI("run", "worker-pools", "add-iam-policy-binding", id,
		"--region="+location,
		"--member=user:cli@example.com",
		"--role=roles/run.invoker",
		"--format=json"))
	var afterAdd struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSON(t, added, &afterAdd)
	require.Len(t, afterAdd.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", afterAdd.Bindings[0].Role)
	assert.Equal(t, []string{"user:cli@example.com"}, afterAdd.Bindings[0].Members)

	// The v1 collection gcloud writes through and the v2 collection the SDK
	// reads are two spellings of one resource — the binding is visible on both.
	v2Policy := httpDoJSON(t, "GET", workerPoolURL(id)+":getIamPolicy", "")
	var fromV2 struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSON(t, v2Policy, &fromV2)
	require.Len(t, fromV2.Bindings, 1, "the v2 collection must see the policy gcloud wrote through v1")
	assert.Equal(t, "roles/run.invoker", fromV2.Bindings[0].Role)

	// remove-iam-policy-binding takes it back off.
	removed := runCLI(t, gcloudCLI("run", "worker-pools", "remove-iam-policy-binding", id,
		"--region="+location,
		"--member=user:cli@example.com",
		"--role=roles/run.invoker",
		"--format=json"))
	var afterRemove struct {
		Bindings []struct {
			Role string `json:"role"`
		} `json:"bindings"`
	}
	parseJSON(t, removed, &afterRemove)
	assert.Empty(t, afterRemove.Bindings, "the only binding is gone after remove")

	// set-iam-policy replaces the whole policy from a file.
	policyPath := filepath.Join(tmpDir, "worker-pool-policy.json")
	policy := map[string]any{
		"bindings": []map[string]any{{
			"role":    "roles/run.developer",
			"members": []string{"serviceAccount:deployer@test-project.iam.gserviceaccount.com"},
		}},
	}
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policyPath, data, 0o600))

	set := runCLI(t, gcloudCLI("run", "worker-pools", "set-iam-policy", id, policyPath,
		"--region="+location, "--format=json"))
	var afterSet struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSON(t, set, &afterSet)
	require.Len(t, afterSet.Bindings, 1)
	assert.Equal(t, "roles/run.developer", afterSet.Bindings[0].Role)
	assert.Equal(t,
		[]string{"serviceAccount:deployer@test-project.iam.gserviceaccount.com"},
		afterSet.Bindings[0].Members)
}

// TestCloudRunWorkerPools_CLI_IAMPolicyOnMissingPool pins the CLI-visible
// error for a worker pool that was never deployed: the IAM verb is NOT_FOUND,
// not an empty policy.
func TestCloudRunWorkerPools_CLI_IAMPolicyOnMissingPool(t *testing.T) {
	cmd := gcloudCLI("run", "worker-pools", "get-iam-policy", "cli-wp-never-deployed",
		"--region="+location, "--format=json")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "get-iam-policy on an absent worker pool must fail")
	assert.Contains(t, string(out), "NOT_FOUND")
	assert.Contains(t, string(out), "cli-wp-never-deployed")
}

func TestCloudRunV2WorkerPools_Wire_CreateGetListPatchDelete(t *testing.T) {
	createBody := `{
		"template": {"containers": [{"image": "gcr.io/test-project/wp-cli"}]},
		"scaling": {"manualInstanceCount": 3}
	}`
	createOut := httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId=cli-wp-roundtrip", createBody)
	var lro struct {
		Done     bool `json:"done"`
		Response struct {
			Name              string `json:"name"`
			UID               string `json:"uid"`
			Generation        string `json:"generation"`
			TerminalCondition struct {
				State string `json:"state"`
			} `json:"terminalCondition"`
			Scaling struct {
				ManualInstanceCount int `json:"manualInstanceCount"`
			} `json:"scaling"`
		} `json:"response"`
	}
	parseJSON(t, createOut, &lro)
	require.True(t, lro.Done, "CreateWorkerPool LRO should be done immediately in the sim")
	assert.Contains(t, lro.Response.Name, "cli-wp-roundtrip")
	assert.NotEmpty(t, lro.Response.UID)
	assert.Equal(t, "1", lro.Response.Generation)
	assert.Equal(t, "CONDITION_SUCCEEDED", lro.Response.TerminalCondition.State)
	assert.Equal(t, 3, lro.Response.Scaling.ManualInstanceCount)

	getOut := httpDoJSON(t, "GET", workerPoolURL("cli-wp-roundtrip"), "")
	var got struct {
		Name string `json:"name"`
	}
	parseJSON(t, getOut, &got)
	assert.Contains(t, got.Name, "cli-wp-roundtrip")

	listOut := httpDoJSON(t, "GET", workerPoolsBaseURL(), "")
	var list struct {
		WorkerPools []struct {
			Name string `json:"name"`
		} `json:"workerPools"`
	}
	parseJSON(t, listOut, &list)
	var found bool
	for _, p := range list.WorkerPools {
		if p.Name == got.Name {
			found = true
		}
	}
	assert.True(t, found, "ListWorkerPools must include the created pool")

	patchOut := httpDoJSON(t, "PATCH", workerPoolURL("cli-wp-roundtrip")+"?updateMask=scaling",
		`{"scaling": {"manualInstanceCount": 5}}`)
	var patched struct {
		Response struct {
			Generation string `json:"generation"`
			Scaling    struct {
				ManualInstanceCount int `json:"manualInstanceCount"`
			} `json:"scaling"`
			Template struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"template"`
		} `json:"response"`
	}
	parseJSON(t, patchOut, &patched)
	assert.Equal(t, "2", patched.Response.Generation)
	assert.Equal(t, 5, patched.Response.Scaling.ManualInstanceCount)
	require.Len(t, patched.Response.Template.Containers, 1, "an unmasked field survives the patch")
	assert.Equal(t, "gcr.io/test-project/wp-cli", patched.Response.Template.Containers[0].Image)

	revsOut := httpDoJSON(t, "GET", workerPoolURL("cli-wp-roundtrip")+"/revisions", "")
	var revs struct {
		Revisions []struct {
			Name string `json:"name"`
		} `json:"revisions"`
	}
	parseJSON(t, revsOut, &revs)
	require.GreaterOrEqual(t, len(revs.Revisions), 2, "create + patch each materialize a revision")

	httpDoJSON(t, "DELETE", baseURL+"/v2/"+revs.Revisions[0].Name, "")
	httpDoJSON(t, "DELETE", workerPoolURL("cli-wp-roundtrip"), "")
	resp, err := httpDo("GET", workerPoolURL("cli-wp-roundtrip"), "")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "GetWorkerPool after delete must 404")
}

// TestCloudRunV2WorkerPools_Wire_AutomaticScaling round-trips the
// automatic-scaling members of WorkerPoolScaling through create, get and a
// masked patch.
func TestCloudRunV2WorkerPools_Wire_AutomaticScaling(t *testing.T) {
	const id = "cli-wp-autoscaling"
	createBody := `{
		"template": {"containers": [{"image": "gcr.io/test-project/wp-autoscaling"}]},
		"scaling": {"scalingMode": "AUTOMATIC", "minInstanceCount": 2, "maxInstanceCount": 9}
	}`
	httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId="+id, createBody)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", workerPoolURL(id), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	type scaling struct {
		ScalingMode         string `json:"scalingMode"`
		MinInstanceCount    int    `json:"minInstanceCount"`
		MaxInstanceCount    int    `json:"maxInstanceCount"`
		ManualInstanceCount int    `json:"manualInstanceCount"`
	}
	var got struct {
		Scaling scaling `json:"scaling"`
	}
	parseJSON(t, httpDoJSON(t, "GET", workerPoolURL(id), ""), &got)
	assert.Equal(t, "AUTOMATIC", got.Scaling.ScalingMode)
	assert.Equal(t, 2, got.Scaling.MinInstanceCount)
	assert.Equal(t, 9, got.Scaling.MaxInstanceCount)
	assert.Zero(t, got.Scaling.ManualInstanceCount,
		"manualInstanceCount is unset under automatic scaling and must not be invented")

	patchOut := httpDoJSON(t, "PATCH", workerPoolURL(id)+"?updateMask=scaling",
		`{"scaling": {"scalingMode": "MANUAL", "manualInstanceCount": 4}}`)
	var patched struct {
		Response struct {
			Scaling scaling `json:"scaling"`
		} `json:"response"`
	}
	parseJSON(t, patchOut, &patched)
	assert.Equal(t, "MANUAL", patched.Response.Scaling.ScalingMode)
	assert.Equal(t, 4, patched.Response.Scaling.ManualInstanceCount)
	assert.Zero(t, patched.Response.Scaling.MinInstanceCount,
		"a masked scaling patch replaces the whole message")
}

// TestCloudRunV2WorkerPools_Wire_ProbesAndEnvValueSource round-trips the
// container health probes and the Secret Manager-sourced environment variable
// through the worker-pool collection.
func TestCloudRunV2WorkerPools_Wire_ProbesAndEnvValueSource(t *testing.T) {
	const id = "cli-wp-probes"
	createBody := `{
		"template": {
			"containers": [{
				"image": "gcr.io/test-project/wp-probes",
				"env": [
					{"name": "LITERAL", "value": "plain"},
					{"name": "SOURCED", "valueSource": {"secretKeyRef": {"secret": "wp-secret", "version": "3"}}}
				],
				"startupProbe": {
					"initialDelaySeconds": 2, "timeoutSeconds": 3,
					"periodSeconds": 12, "failureThreshold": 5,
					"httpGet": {"path": "/startup", "port": 9090,
						"httpHeaders": [{"name": "X-Probe", "value": "startup"}]}
				},
				"livenessProbe": {"grpc": {"port": 9090, "service": "worker.v1.Health"}},
				"readinessProbe": {"tcpSocket": {"port": 9091}}
			}]
		}
	}`
	httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId="+id, createBody)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", workerPoolURL(id), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	var got struct {
		Template struct {
			Containers []struct {
				Env []struct {
					Name        string `json:"name"`
					Value       string `json:"value"`
					ValueSource *struct {
						SecretKeyRef struct {
							Secret  string `json:"secret"`
							Version string `json:"version"`
						} `json:"secretKeyRef"`
					} `json:"valueSource"`
				} `json:"env"`
				StartupProbe struct {
					InitialDelaySeconds int `json:"initialDelaySeconds"`
					TimeoutSeconds      int `json:"timeoutSeconds"`
					PeriodSeconds       int `json:"periodSeconds"`
					FailureThreshold    int `json:"failureThreshold"`
					HTTPGet             struct {
						Path        string `json:"path"`
						Port        int    `json:"port"`
						HTTPHeaders []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"httpHeaders"`
					} `json:"httpGet"`
				} `json:"startupProbe"`
				LivenessProbe struct {
					GRPC struct {
						Port    int    `json:"port"`
						Service string `json:"service"`
					} `json:"grpc"`
				} `json:"livenessProbe"`
				ReadinessProbe struct {
					TCPSocket struct {
						Port int `json:"port"`
					} `json:"tcpSocket"`
				} `json:"readinessProbe"`
			} `json:"containers"`
		} `json:"template"`
	}
	parseJSON(t, httpDoJSON(t, "GET", workerPoolURL(id), ""), &got)
	require.Len(t, got.Template.Containers, 1)
	c := got.Template.Containers[0]

	assert.Equal(t, 2, c.StartupProbe.InitialDelaySeconds)
	assert.Equal(t, 3, c.StartupProbe.TimeoutSeconds)
	assert.Equal(t, 12, c.StartupProbe.PeriodSeconds)
	assert.Equal(t, 5, c.StartupProbe.FailureThreshold)
	assert.Equal(t, "/startup", c.StartupProbe.HTTPGet.Path)
	assert.Equal(t, 9090, c.StartupProbe.HTTPGet.Port)
	require.Len(t, c.StartupProbe.HTTPGet.HTTPHeaders, 1)
	assert.Equal(t, "X-Probe", c.StartupProbe.HTTPGet.HTTPHeaders[0].Name)
	assert.Equal(t, "startup", c.StartupProbe.HTTPGet.HTTPHeaders[0].Value)
	assert.Equal(t, 9090, c.LivenessProbe.GRPC.Port)
	assert.Equal(t, "worker.v1.Health", c.LivenessProbe.GRPC.Service)
	assert.Equal(t, 9091, c.ReadinessProbe.TCPSocket.Port)

	require.Len(t, c.Env, 2)
	byName := map[string]int{}
	for i, e := range c.Env {
		byName[e.Name] = i
	}
	require.Contains(t, byName, "LITERAL")
	assert.Equal(t, "plain", c.Env[byName["LITERAL"]].Value)
	assert.Nil(t, c.Env[byName["LITERAL"]].ValueSource)

	require.Contains(t, byName, "SOURCED")
	sourced := c.Env[byName["SOURCED"]]
	require.NotNil(t, sourced.ValueSource)
	assert.Equal(t, "wp-secret", sourced.ValueSource.SecretKeyRef.Secret)
	assert.Equal(t, "3", sourced.ValueSource.SecretKeyRef.Version)
	assert.Empty(t, sourced.Value,
		"value and valueSource are alternatives of one oneof; only the set member is returned")
}

func TestCloudRunV2Instances_Wire_CreatePatchStartStopDelete(t *testing.T) {
	createBody := `{"containers": [{"image": "gcr.io/test-project/inst-cli"}], "labels": {"team": "cli"}}`
	createOut := httpDoJSON(t, "POST", instancesBaseURL()+"?instanceId=cli-inst-roundtrip", createBody)
	var lro struct {
		Done     bool `json:"done"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, createOut, &lro)
	require.True(t, lro.Done)
	assert.Contains(t, lro.Response.Name, "cli-inst-roundtrip")

	listOut := httpDoJSON(t, "GET", instancesBaseURL(), "")
	var list struct {
		Instances []struct {
			Name string `json:"name"`
		} `json:"instances"`
	}
	parseJSON(t, listOut, &list)
	var found bool
	for _, in := range list.Instances {
		if in.Name == lro.Response.Name {
			found = true
		}
	}
	assert.True(t, found, "ListInstances must include the created instance")

	patchOut := httpDoJSON(t, "PATCH", instanceURL("cli-inst-roundtrip")+"?updateMask=containers",
		`{"containers": [{"image": "gcr.io/test-project/inst-cli:v2"}]}`)
	var patched struct {
		Response struct {
			Generation string            `json:"generation"`
			Labels     map[string]string `json:"labels"`
			Containers []struct {
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"response"`
	}
	parseJSON(t, patchOut, &patched)
	assert.Equal(t, "2", patched.Response.Generation)
	require.Len(t, patched.Response.Containers, 1)
	assert.Equal(t, "gcr.io/test-project/inst-cli:v2", patched.Response.Containers[0].Image)
	assert.Equal(t, "cli", patched.Response.Labels["team"], "an unmasked field survives the patch")

	stopOut := httpDoJSON(t, "POST", instanceURL("cli-inst-roundtrip")+":stop", "{}")
	var stopLRO struct {
		Response struct {
			TerminalCondition struct {
				State  string `json:"state"`
				Reason string `json:"reason"`
			} `json:"terminalCondition"`
		} `json:"response"`
	}
	parseJSON(t, stopOut, &stopLRO)
	assert.Equal(t, "CONDITION_PENDING", stopLRO.Response.TerminalCondition.State)
	assert.Equal(t, "Stopped", stopLRO.Response.TerminalCondition.Reason)

	startOut := httpDoJSON(t, "POST", instanceURL("cli-inst-roundtrip")+":start", "{}")
	var startLRO struct {
		Response struct {
			TerminalCondition struct {
				Type  string `json:"type"`
				State string `json:"state"`
			} `json:"terminalCondition"`
		} `json:"response"`
	}
	parseJSON(t, startOut, &startLRO)
	assert.Equal(t, "Ready", startLRO.Response.TerminalCondition.Type)
	assert.Equal(t, "CONDITION_SUCCEEDED", startLRO.Response.TerminalCondition.State)

	httpDoJSON(t, "DELETE", instanceURL("cli-inst-roundtrip"), "")
	resp, err := httpDo("GET", instanceURL("cli-inst-roundtrip"), "")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "GetInstance after delete must 404")
}
