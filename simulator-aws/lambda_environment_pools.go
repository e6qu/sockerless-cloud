package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/funcrt"
)

// lambdaAccountConcurrency is the account's concurrency limit, the
// ConcurrentExecutions GetAccountSettings reports: "By default, Lambda
// provides your account with a total concurrency limit of 1,000 concurrent
// executions across all functions in an AWS Region." (lambda-concurrency
// .md.gz.)
const lambdaAccountConcurrency = 1000

// lambdaMinimumUnreservedConcurrency is what reserved concurrency must leave
// to the functions without it: "You can reserve up to the Unreserved account
// concurrency value minus 100." (configuration-concurrency.md.gz.)
const lambdaMinimumUnreservedConcurrency = 100

// lambdaEnvironments holds the execution environments of every function
// version, one pool per version, and the concurrency they count against: a
// limit per function, under the account's unreserved concurrency.
var lambdaEnvironments = &lambdaEnvironmentRegistry{}

type lambdaEnvironmentRegistry struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	account *funcrt.Limit
	limits  map[string]*funcrt.Limit
	pools   map[string]*lambdaVersionPool
}

// lambdaVersionPool is the pool of one function version. revision is the
// RevisionId the pool's environments were set up from: a code or
// configuration update gives $LATEST a new one, and its invocations then run
// on new environments while the old ones drain.
type lambdaVersionPool struct {
	function string
	version  string
	revision string
	pool     *funcrt.Pool[*lambdaEnvironment]
}

// reset retires every environment and starts over empty, for a server that
// registers AWS Lambda afresh.
func (r *lambdaEnvironmentRegistry) reset() {
	r.retireAll()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.account = funcrt.NewLimit(nil, lambdaAccountConcurrency)
	r.limits = map[string]*funcrt.Limit{}
	r.pools = map[string]*lambdaVersionPool{}
}

// retireAll stops every environment, as the simulator does when it stops.
func (r *lambdaEnvironmentRegistry) retireAll() {
	r.mu.Lock()
	pools := r.pools
	r.pools = map[string]*lambdaVersionPool{}
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range pools {
		wg.Add(1)
		bg.JoinedGo(func() {
			defer wg.Done()
			p.pool.Retire()
		})
	}
	wg.Wait()
}

func lambdaVersionKey(function, version string) string {
	if version == "" {
		version = "$LATEST"
	}
	return function + "\x00" + version
}

func (r *lambdaEnvironmentRegistry) limitLocked(function string) *funcrt.Limit {
	limit, ok := r.limits[function]
	if !ok {
		max := -1
		if reserved, has := lambdaConcurrency.Get(function); has {
			max = reserved
		}
		limit = funcrt.NewLimit(r.account, max)
		r.limits[function] = limit
	}
	return limit
}

func (r *lambdaEnvironmentRegistry) accountLimit() *funcrt.Limit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.account
}

// pool is the pool fn's invocations run on, replacing the version's pool
// when fn was set up from another revision.
func (r *lambdaEnvironmentRegistry) pool(fn LambdaFunction) *funcrt.Pool[*lambdaEnvironment] {
	key := lambdaVersionKey(fn.FunctionName, fn.Version)
	r.mu.Lock()
	entry := r.pools[key]
	if entry != nil && entry.revision == fn.RevisionId {
		r.mu.Unlock()
		return entry.pool
	}
	snapshot := fn
	p := funcrt.New(r.ctx, funcrt.Hooks[*lambdaEnvironment]{
		Start: func(ctx context.Context, kind funcrt.Kind) (*lambdaEnvironment, error) {
			return startLambdaEnvironment(ctx, snapshot, kind)
		},
		Expires: func(env *lambdaEnvironment) time.Time { return env.expires },
	}, funcrt.Options{Limit: r.limitLocked(fn.FunctionName), PerEnvironment: 1})
	r.pools[key] = &lambdaVersionPool{function: fn.FunctionName, version: fn.Version, revision: fn.RevisionId, pool: p}
	r.mu.Unlock()
	if entry != nil {
		bg.JoinedGo(entry.pool.Retire)
	}
	return p
}

func (r *lambdaEnvironmentRegistry) existing(function, version string) *funcrt.Pool[*lambdaEnvironment] {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.pools[lambdaVersionKey(function, version)]; entry != nil {
		return entry.pool
	}
	return nil
}

// lambdaReservedTotal is the concurrency reserved across the account.
func lambdaReservedTotal() int {
	total := 0
	for _, reserved := range lambdaConcurrency.List() {
		total += reserved
	}
	return total
}

// lambdaUnreservedConcurrency is what the account leaves to the functions
// without reserved concurrency.
func lambdaUnreservedConcurrency() int {
	return lambdaAccountConcurrency - lambdaReservedTotal()
}

// lambdaProvisionedTargets is the provisioned concurrency each version of a
// function is configured with, an alias's counting for the version it points
// to.
func lambdaProvisionedTargets(function string) map[string]int {
	targets := map[string]int{}
	aliases, _ := lambdaAliases.Get(function)
	for _, pc := range lambdaPCs.List() {
		if pc.FunctionName != function {
			continue
		}
		version := pc.Qualifier
		if alias, ok := aliases[pc.Qualifier]; ok {
			version = alias.FunctionVersion
		}
		targets[version] += pc.Requested
	}
	return targets
}

// lambdaFunctionVersion is the function as one of its published versions
// runs it.
func lambdaFunctionVersion(function, version string) (LambdaFunction, bool) {
	versions, _ := lambdaVersions.Get(function)
	for _, v := range versions {
		if v.Version == version {
			return lambdaFunctionFromVersion(v), true
		}
	}
	return LambdaFunction{}, false
}

// reconcile brings a function's environments in line with its configuration:
// its reserved concurrency, the account's unreserved concurrency, the
// provisioned environments of each version, and the retirement of the
// environments of a revision, version or function that no longer exists.
func (r *lambdaEnvironmentRegistry) reconcile(function string) {
	fn, exists := lambdaFunctions.Get(function)
	r.mu.Lock()
	if r.account == nil {
		r.mu.Unlock()
		return
	}
	r.account.SetMax(lambdaUnreservedConcurrency())
	var retire []*funcrt.Pool[*lambdaEnvironment]
	if !exists {
		delete(r.limits, function)
	} else {
		reserved, has := lambdaConcurrency.Get(function)
		if !has {
			reserved = -1
		}
		r.limitLocked(function).SetMax(reserved)
	}
	var versions []*lambdaVersionPool
	for key, entry := range r.pools {
		if entry.function != function {
			continue
		}
		stale := !exists
		if exists && (entry.version == "" || entry.version == "$LATEST") {
			stale = entry.revision != fn.RevisionId
		} else if exists {
			_, published := lambdaFunctionVersion(function, entry.version)
			stale = !published
		}
		if stale {
			retire = append(retire, entry.pool)
			delete(r.pools, key)
			continue
		}
		versions = append(versions, entry)
	}
	r.mu.Unlock()
	defer lambdaEnvironmentChanges.Announce(function)
	for _, p := range retire {
		p.Retire()
	}
	if !exists {
		return
	}
	targets := lambdaProvisionedTargets(function)
	for _, entry := range versions {
		if _, wanted := targets[entry.version]; !wanted {
			_ = entry.pool.Provision(0)
		}
	}
	for version, n := range targets {
		snapshot, ok := lambdaFunctionVersion(function, version)
		if !ok {
			continue
		}
		if err := r.pool(snapshot).Provision(n); err != nil {
			lambdaESMLogger.Warn().Str("function", function).Str("version", version).Err(err).
				Msg("AWS Lambda could not hold the concurrency a version's provisioned environments need")
		}
	}
}

// reconcileAll reconciles every function the simulator holds and every
// function it holds environments for.
func (r *lambdaEnvironmentRegistry) reconcileAll() {
	names := map[string]bool{}
	for _, fn := range lambdaFunctions.List() {
		names[fn.FunctionName] = true
	}
	r.mu.Lock()
	for _, entry := range r.pools {
		names[entry.function] = true
	}
	for name := range r.limits {
		names[name] = true
	}
	r.mu.Unlock()
	for name := range names {
		r.reconcile(name)
	}
}

// startLambdaEnvironmentReconciler keeps every function's environments in line
// with its configuration as it changes, and stops them all when the simulator
// stops.
func startLambdaEnvironmentReconciler(srv *sim.Server) {
	srv.StartBackground("AWS Lambda execution environments", func(ctx context.Context) {
		for {
			changed := lambdaFunctionChanges.Any()
			lambdaEnvironments.reconcileAll()
			select {
			case <-ctx.Done():
				lambdaEnvironments.retireAll()
				return
			case <-changed:
			}
		}
	})
}

// lambdaThrottle is the TooManyRequestsException AWS Lambda answers an
// invocation no concurrency is left for, naming the limit that refused it
// with the API's ThrottleReason.
func lambdaThrottle(t *funcrt.ThrottledError) *lambdaServiceError {
	reason := "ReservedFunctionConcurrentInvocationLimitExceeded"
	if t.Limit == lambdaEnvironments.accountLimit() {
		reason = "ConcurrentInvocationLimitExceeded"
	}
	return &lambdaServiceError{
		Status:  http.StatusTooManyRequests,
		Code:    "TooManyRequestsException",
		Message: "Rate Exceeded.",
		Reason:  reason,
		Type:    "User",
	}
}

// lambdaProvisionedState is where a provisioned concurrency configuration's
// environments stand: IN_PROGRESS until every one of them has initialised —
// "you can't access any of those environments until Lambda completely
// finishes allocating them" (lambda-concurrency.md.gz) — then READY, or FAILED
// with the reason when one could not be initialised.
func lambdaProvisionedState(pc LambdaProvisionedConcurrency) (status string, allocated, available int, reason string) {
	version := pc.Qualifier
	if aliases, _ := lambdaAliases.Get(pc.FunctionName); aliases != nil {
		if alias, ok := aliases[pc.Qualifier]; ok {
			version = alias.FunctionVersion
		}
	}
	p := lambdaEnvironments.existing(pc.FunctionName, version)
	if p == nil {
		return "IN_PROGRESS", 0, 0, ""
	}
	prov := p.Provisioning()
	allocated = min(prov.Allocated, pc.Requested)
	switch {
	case prov.Err != nil:
		return "FAILED", allocated, 0, strings.TrimSpace(prov.Err.Error())
	case prov.Ready:
		return "READY", allocated, allocated, ""
	}
	return "IN_PROGRESS", allocated, 0, ""
}
