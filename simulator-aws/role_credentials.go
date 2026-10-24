package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// A workload a service runs under a role holds a session of that role: an
// instance through its profile, a task through its task role, a function
// through its execution role, a build through its service role. The service
// hands the session over through its own channel — the instance metadata
// service, the container credentials endpoint, the environment — and serves
// the holder one session until it nears expiry.

const (
	roleSessionLifetime = time.Hour
	// roleSessionMinRemaining is how much of a held session's lifetime must
	// remain for the channel to keep serving it. The Amazon ECS agent and the
	// instance metadata service serve one credential until they refresh it
	// ahead of expiry, and SDKs refresh their own copy minutes before expiry;
	// half the lifetime clears every SDK's window.
	roleSessionMinRemaining = roleSessionLifetime / 2
)

// roleHeldSession is the session a holder is served now.
type roleHeldSession struct {
	Holder      string
	AccessKeyID string
}

// containerCredentialGrant is what one container credentials endpoint ID
// serves: a session of a role, held by the workload the ID was handed to.
type containerCredentialGrant struct {
	ID          string
	Holder      string
	RoleArn     string
	SessionName string
}

var (
	roleHeldSessions          sim.Store[roleHeldSession]
	containerCredentialGrants sim.Store[containerCredentialGrant]
	roleSessionMu             sync.Mutex
)

// roleHolderLive reports, per holder kind, whether the workload a holder
// names still runs; the sweep drops what the rest hold.
var roleHolderLive = map[string]func(id string) bool{
	"ec2": func(id string) bool {
		inst, ok := ec2Instances.Get(id)
		return ok && inst.State != "terminated"
	},
	"ecs": func(id string) bool {
		_, ok := ecsTasks.Get(id)
		return ok
	},
	"lambda": func(name string) bool {
		_, ok := lambdaFunctions.Get(name)
		return ok
	},
	"amplify": func(key string) bool {
		appID, branch, _ := strings.Cut(key, "/")
		stored, ok := amplifyApps.Get(appID)
		_, live := stored.Branches[branch]
		return ok && live
	},
	"codebuild": func(id string) bool {
		build, ok := cbBuilds.Get(id)
		return ok && build.BuildStatus == "IN_PROGRESS"
	},
	"codebuild-command": func(id string) bool {
		ce, ok := cbCommandExecs.Get(id)
		return ok && ce.EndTime == 0
	},
	"glue": func(key string) bool {
		run, ok := glueJobRuns.Get(key)
		return ok && (run.JobRunState == "RUNNING" || run.JobRunState == "STARTING")
	},
	// A service holding a session of a role it acts as, keyed
	// <service principal>|<role ARN>, holds it while the role exists.
	"service": func(key string) bool {
		_, roleArn, _ := strings.Cut(key, "|")
		role, ok := iamRoles.Get(iamRoleNameFromArn(roleArn))
		return ok && role.Arn == roleArn
	},
	"batch": func(jobID string) bool {
		job, ok := batchJobs.Get(jobID)
		return ok && (job.Status == batchStatusStarting || job.Status == batchStatusRunning)
	},
}

func roleHolder(kind, id string) string { return kind + ":" + id }

func roleHolderIsLive(holder string) bool {
	kind, id, _ := strings.Cut(holder, ":")
	live, ok := roleHolderLive[kind]
	return ok && live(id)
}

func registerRoleCredentials(srv *sim.Server) {
	roleHeldSessions = sim.MakeStore[roleHeldSession](srv.DB(), "role_held_sessions")
	containerCredentialGrants = sim.MakeStore[containerCredentialGrant](srv.DB(), "container_credential_grants")
	kvstore.StartSweeper(srv, "role credential expiry", storeSweepInterval, func(now time.Time) { sweepRoleCredentials(now) })

	// The container credentials endpoint serves the session an ID was granted
	// for, in the shape the AWS SDKs' container credential provider reads.
	srv.HandleFunc("GET /v2/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		grant, ok := containerCredentialGrants.Get(sim.PathParam(r, "id"))
		if !ok || !roleHolderIsLive(grant.Holder) {
			containerCredentialsError(w, http.StatusNotFound, "NotFound", "Credentials not found")
			return
		}
		role, ok := iamRoles.Get(iamRoleNameFromArn(grant.RoleArn))
		if !ok {
			containerCredentialsError(w, http.StatusBadRequest, "InvalidRole",
				fmt.Sprintf("Unable to assume role %s", grant.RoleArn))
			return
		}
		cred := roleSessionCredential(grant.Holder, role.RoleName, grant.SessionName, time.Now().UTC())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"RoleArn":         role.Arn,
			"AccessKeyId":     cred.AccessKeyID,
			"SecretAccessKey": cred.SecretAccessKey,
			"Token":           cred.SessionToken,
			"Expiration":      cred.Expiration,
		})
	})
}

func containerCredentialsError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// containerCredentialsLoopback is where a workload's own loopback serves the
// container credentials endpoint: where the Amazon ECS agent serves it
// behind 169.254.170.2.
const containerCredentialsLoopback = "127.0.0.1:51679"

// containerCredentials is how a workload reaches the container credentials
// endpoint: the environment that names it and the forwards that serve it on
// the workload's loopback.
type containerCredentials struct {
	Env      map[string]string
	Forwards []placement.Forward
}

// grantContainerCredentials hands holder an endpoint ID that serves sessions
// of roleArn named sessionName to a workload backend places on network. The
// AWS SDKs' container credential provider takes a plain-HTTP full URI only
// on a loopback address or the endpoint's link-local one, which a bridge
// network cannot route, so on a backend that forwards a workload's loopback
// the endpoint is that loopback, relayed to the network's sidecar; on any
// other it is the sidecar itself.
func grantContainerCredentials(backend placement.Backend, network awsWorkloadNetwork, holder, roleArn, sessionName string) containerCredentials {
	if _, ok := backend.(placement.Forwarding); !ok || roleArn == "" {
		return containerCredentials{Env: grantContainerCredentialsAt(network.MetadataOrigin(), holder, roleArn, sessionName)}
	}
	return containerCredentials{
		Env:      grantContainerCredentialsAt("http://"+containerCredentialsLoopback, holder, roleArn, sessionName),
		Forwards: []placement.Forward{{Listen: containerCredentialsLoopback, Upstream: net.JoinHostPort(network.Sidecar.String(), "80")}},
	}
}

// place places w on backend with the forwards that serve its credentials.
func (c containerCredentials) place(ctx context.Context, backend placement.Backend, w placement.Workload, sink sim.LogSink) (placement.Handle, error) {
	if len(c.Forwards) == 0 {
		return backend.Place(ctx, w, sink)
	}
	forwarding, ok := backend.(placement.Forwarding)
	if !ok {
		return nil, fmt.Errorf("place %s: the %s backend serves no forwards", w.Name, backend.Kind())
	}
	return forwarding.PlaceForwarded(ctx, w, c.Forwards, sink)
}

// grantContainerCredentialsAt hands holder an endpoint ID that serves
// sessions of roleArn named sessionName, and returns the environment that
// points the AWS SDKs' container credential provider at it through origin.
func grantContainerCredentialsAt(origin, holder, roleArn, sessionName string) map[string]string {
	if roleArn == "" {
		return map[string]string{}
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	containerCredentialGrants.Put(id, containerCredentialGrant{ID: id, Holder: holder, RoleArn: roleArn, SessionName: sessionName})
	return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": origin + "/v2/credentials/" + id}
}

// roleEnvironmentCredentials is the environment that hands a holder a session
// of roleArn directly, the way AWS Lambda gives a function its execution
// role. A role IAM does not hold yields none.
func roleEnvironmentCredentials(holder, roleArn, sessionName string) map[string]string {
	role, ok := iamRoles.Get(iamRoleNameFromArn(roleArn))
	if roleArn == "" || !ok {
		return map[string]string{}
	}
	cred := roleSessionCredential(holder, role.RoleName, sessionName, time.Now().UTC())
	return map[string]string{
		"AWS_ACCESS_KEY_ID":     cred.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": cred.SecretAccessKey,
		"AWS_SESSION_TOKEN":     cred.SessionToken,
	}
}

// revokeRoleCredentials ends what holder holds: its grants stop answering
// and its session is no longer served.
func revokeRoleCredentials(holder string) {
	containerCredentialGrants.Prune(func(grant containerCredentialGrant) bool { return grant.Holder == holder })
	roleHeldSessions.Delete(holder)
}

func sweepRoleCredentials(time.Time) int {
	n := containerCredentialGrants.Prune(func(grant containerCredentialGrant) bool { return !roleHolderIsLive(grant.Holder) })
	return n + roleHeldSessions.Prune(func(held roleHeldSession) bool { return !roleHolderIsLive(held.Holder) })
}

// roleSessionCredential returns the session holder is served for roleName at
// now, minting one named sessionName only when it holds none for the role or
// the one it holds is too close to expiry.
func roleSessionCredential(holder, roleName, sessionName string, now time.Time) IAMTempCred {
	roleSessionMu.Lock()
	defer roleSessionMu.Unlock()
	if held, ok := roleHeldSessions.Get(holder); ok {
		if cred, ok := iamTempCreds.Get(held.AccessKeyID); ok && cred.RoleName == roleName {
			if exp, err := time.Parse(time.RFC3339, cred.Expiration); err == nil && exp.Sub(now) > roleSessionMinRemaining {
				return cred
			}
		}
	}
	expiration := now.Add(roleSessionLifetime)
	accessKeyID, secretAccessKey, token := stsMintTempCred(expiration)
	cred := IAMTempCred{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		SessionToken:    token,
		RoleName:        roleName,
		PrincipalArn:    fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", awsAccountID(), roleName, sessionName),
		Expiration:      expiration.Format(time.RFC3339),
		CreatedAt:       now.Format(time.RFC3339),
	}
	iamTempCreds.Put(accessKeyID, cred)
	roleHeldSessions.Put(holder, roleHeldSession{Holder: holder, AccessKeyID: accessKeyID})
	return cred
}
