package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

// A Valkey or Redis OSS cache authenticates its clients in the engine: its
// default user takes the cache's AUTH tokens, or, with user groups attached,
// the groups' users are engine users their access strings rule. The control
// plane administers the nodes as an engine user of its own; ElastiCache
// leaves clients no ACL command that writes.
const ecAdminUser = "elasticache-admin"

func ecNewAdminSecret() ([]byte, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return ecSealToken(hex.EncodeToString(raw))
}

// ecSealTokens seals the AUTH tokens a cache's default user accepts, oldest
// first; an empty entry stands for connecting without one.
func ecSealTokens(tokens []string) ([]byte, error) {
	encoded, err := json.Marshal(tokens)
	if err != nil {
		return nil, err
	}
	return ecSealToken(string(encoded))
}

func ecOpenTokens(sealed []byte) ([]string, error) {
	encoded, err := ecOpenToken(sealed)
	if err != nil {
		return nil, err
	}
	var tokens []string
	if err := json.Unmarshal([]byte(encoded), &tokens); err != nil {
		return nil, fmt.Errorf("decode the AUTH tokens: %w", err)
	}
	return tokens, nil
}

// ecResourceTokens are the AUTH tokens of a cache cluster or replication
// group: those it records, else the one its engine started with.
func ecResourceTokens(kind, id string) ([]string, error) {
	var sealed []byte
	switch kind {
	case ecKindCluster:
		c, _ := ecClusters.Get(id)
		sealed = c.AuthTokens
	case ecKindReplicationGroup:
		g, _ := ecReplGroups.Get(id)
		sealed = g.AuthTokens
	}
	if len(sealed) > 0 {
		return ecOpenTokens(sealed)
	}
	record, ok := ecEngineRecords.Get(ecEngineKey(kind, id))
	if !ok {
		return nil, nil
	}
	token, err := ecOpenToken(record.Token)
	if err != nil || token == "" {
		return nil, err
	}
	return []string{token}, nil
}

func ecTokensEnabled(tokens []string) bool {
	return slices.ContainsFunc(tokens, func(token string) bool { return token != "" })
}

// ecNextTokens applies an AuthTokenUpdateStrategy to the tokens the default
// user accepts. ROTATE adds a token beside the newest one, connecting
// without a token counting as one when the cache had none; SET keeps only
// the token the last ROTATE added; DELETE drops every token as the cache
// moves to user groups.
func ecNextTokens(current []string, token, strategy string, toUserGroups bool) ([]string, string) {
	switch strategy {
	case "", "ROTATE", "SET":
		if token == "" {
			return nil, "AuthTokenUpdateStrategy must be specified with AuthToken."
		}
	}
	switch strategy {
	case "", "ROTATE":
		next := slices.Clone(current)
		if len(next) == 0 {
			next = []string{""}
		}
		next = append(next, token)
		return next[max(0, len(next)-2):], ""
	case "SET":
		if len(current) != 2 || current[1] != token {
			return nil, "AuthTokenUpdateStrategy SET is allowed only after ROTATE, with the AuthToken ROTATE added."
		}
		return []string{token}, ""
	case "DELETE":
		if token != "" {
			return nil, "AuthTokenUpdateStrategy DELETE takes no AuthToken."
		}
		if !toUserGroups {
			return nil, "AuthTokenUpdateStrategy DELETE is allowed only when transitioning to RBAC."
		}
		return []string{}, ""
	}
	return nil, fmt.Sprintf("AuthTokenUpdateStrategy %q is not one of SET, ROTATE or DELETE.", strategy)
}

func ecAttachedUserGroups(kind, id string) []string {
	switch kind {
	case ecKindReplicationGroup:
		if g, ok := ecReplGroups.Get(id); ok {
			return g.UserGroupIds
		}
	case ecKindServerless:
		if c, ok := ecServerlessCaches.Get(id); ok && c.UserGroupId != "" {
			return []string{c.UserGroupId}
		}
	}
	return nil
}

// ecCachesUsingGroup lists the replication groups and serverless caches a
// user group is attached to.
func ecCachesUsingGroup(groupID string) (replicationGroups, serverlessCaches []string) {
	for _, g := range ecReplGroups.List() {
		if slices.Contains(g.UserGroupIds, groupID) {
			replicationGroups = append(replicationGroups, g.ReplicationGroupId)
		}
	}
	for _, c := range ecServerlessCaches.List() {
		if c.UserGroupId == groupID {
			serverlessCaches = append(serverlessCaches, c.ServerlessCacheName)
		}
	}
	sort.Strings(replicationGroups)
	sort.Strings(serverlessCaches)
	return replicationGroups, serverlessCaches
}

// The engine keys of the caches the groups are attached to.
func ecCachesOfGroups(groups []string) []string {
	var keys []string
	for _, group := range groups {
		replicationGroups, serverlessCaches := ecCachesUsingGroup(group)
		for _, id := range replicationGroups {
			keys = append(keys, ecEngineKey(ecKindReplicationGroup, id))
		}
		for _, name := range serverlessCaches {
			keys = append(keys, ecEngineKey(ecKindServerless, name))
		}
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// ecApplyEngineUsers brings a running engine's users in line with its
// cache's tokens and user groups.
func ecApplyEngineUsers(key string) error {
	engine, ok := ecLoadEngine(key)
	if !ok || engine.redis == nil {
		return nil
	}
	engine.opMu.Lock()
	defer engine.opMu.Unlock()
	if !engine.redis.Running() {
		return nil
	}
	return engine.redis.ApplyUsers(engine.redis.Nodes())
}

// ecReapplyUsers applies the engine users of every cache the groups reach,
// in the background when there is one, and then runs settled.
func ecReapplyUsers(groups []string, settled func()) {
	keys := ecCachesOfGroups(groups)
	if len(keys) == 0 {
		settled()
		return
	}
	bg.Go(func() {
		for _, key := range keys {
			if err := ecApplyEngineUsers(key); err != nil {
				ecLogEngineFailure(key, err)
			}
		}
		settled()
	})
}

// ecSplitAccessString splits an access string into its ACL rules. A
// selector is one rule however many spaces its parentheses hold.
func ecSplitAccessString(access string) ([]string, error) {
	var rules []string
	var current strings.Builder
	depth := 0
	for _, r := range access {
		switch {
		case r == '(':
			depth++
		case r == ')':
			if depth == 0 {
				return nil, fmt.Errorf("access string %q closes a selector it never opened", access)
			}
			depth--
		case (r == ' ' || r == '\t') && depth == 0:
			if current.Len() > 0 {
				rules = append(rules, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if depth != 0 {
		return nil, fmt.Errorf("access string %q leaves a selector open", access)
	}
	if current.Len() > 0 {
		rules = append(rules, current.String())
	}
	return rules, nil
}

// ecEngineUser is the engine user an RBAC user is: off unless its access
// string turns it on, and authenticating with its passwords, without one, or
// for an IAM-enabled user with the control plane's secret, which the
// endpoints swap a valid IAM authentication token for.
func ecEngineUser(u ECUser, secret string) (cacheengine.User, error) {
	access, err := ecSplitAccessString(u.AccessString)
	if err != nil {
		return cacheengine.User{}, err
	}
	rule := []string{}
	if !slices.ContainsFunc(access, func(arg string) bool { return strings.EqualFold(arg, "on") || strings.EqualFold(arg, "off") }) {
		rule = append(rule, "off")
	}
	user := cacheengine.User{Name: u.UserName}
	switch ecUserAuthType(u) {
	case ecAuthNoPassword:
		rule = append(rule, "nopass")
	case ecAuthIAM:
		rule = append(rule, ">"+secret)
	default:
		if user.Passwords, err = ecUserPasswords(u); err != nil {
			return cacheengine.User{}, err
		}
	}
	user.Rule = append(rule, access...)
	return user, nil
}

// ecEngineUsers are the engine users of a cache besides the control plane's.
// Without a user group the default user takes the cache's AUTH tokens, or no
// password when a token is optional or there is none. With user groups each
// member is an engine user, and the default user is off unless a member is
// named default.
func ecEngineUsers(kind, id string) ([]cacheengine.User, error) {
	key := ecEngineKey(kind, id)
	record, ok := ecEngineRecords.Get(key)
	if !ok {
		return nil, fmt.Errorf("no engine record for %s", key)
	}
	groups := ecAttachedUserGroups(kind, id)
	if len(groups) == 0 {
		tokens, err := ecResourceTokens(kind, id)
		if err != nil {
			return nil, err
		}
		if len(tokens) == 0 || slices.Contains(tokens, "") {
			return []cacheengine.User{{Name: ecDefaultName, Rule: []string{"on", "nopass", "~*", "&*", "+@all"}}}, nil
		}
		return []cacheengine.User{{Name: ecDefaultName, Passwords: tokens, Rule: []string{"~*", "&*", "+@all"}}}, nil
	}
	secret, err := ecOpenToken(record.Admin)
	if err != nil {
		return nil, err
	}
	named := map[string]bool{}
	var users []cacheengine.User
	for _, groupID := range groups {
		group, ok := ecUserGroups.Get(groupID)
		if !ok {
			continue
		}
		for _, userID := range group.UserIds {
			u, ok := ecUsers.Get(userID)
			if !ok || named[u.UserName] || u.Status == "deleting" {
				continue
			}
			named[u.UserName] = true
			engineUser, err := ecEngineUser(u, secret)
			if err != nil {
				return nil, err
			}
			users = append(users, engineUser)
		}
	}
	if !named[ecDefaultName] {
		users = append(users, cacheengine.User{Name: ecDefaultName, Rule: []string{"off"}})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users, nil
}

// ecIAMAuthenticator accepts an IAM authentication token for the cache: a
// SigV4-presigned GET of http://<cache>/?Action=connect&User=<user>, with
// ResourceType=ServerlessCache for a serverless cache, signed for the
// elasticache service within the last 15 minutes by a principal that may
// elasticache:Connect to the cache and as the user, which is an IAM-enabled
// member of a user group the cache has. It answers the engine user the
// connection authenticates as.
func ecIAMAuthenticator(kind, id string) func(string) (string, bool) {
	return func(token string) (string, bool) {
		parsed, err := url.Parse("http://" + token)
		if err != nil || parsed.Host != id || (parsed.Path != "/" && parsed.Path != "") {
			return "", false
		}
		query := parsed.Query()
		if query.Get("Action") != "connect" {
			return "", false
		}
		resourceARN := ecReplGroupARN(id)
		if kind == ecKindServerless {
			if query.Get("ResourceType") != "ServerlessCache" {
				return "", false
			}
			resourceARN = ecServerlessCacheARN(id)
		}
		user, ok := ecUsers.Get(query.Get("User"))
		if !ok || ecUserAuthType(user) != ecAuthIAM || user.Status == "deleting" {
			return "", false
		}
		member := false
		for _, groupID := range ecAttachedUserGroups(kind, id) {
			if group, ok := ecUserGroups.Get(groupID); ok && slices.Contains(group.UserIds, user.UserId) {
				member = true
			}
		}
		if !member {
			return "", false
		}
		credential, ok := parseCredScope(query.Get("X-Amz-Credential"))
		if !ok || credential.service != "elasticache" || credential.region != awsRegion() {
			return "", false
		}
		expires, err := strconv.Atoi(query.Get("X-Amz-Expires"))
		if err != nil || expires <= 0 || expires > 900 {
			return "", false
		}
		signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
		if err != nil || time.Now().Before(signedAt.Add(-5*time.Minute)) || time.Now().After(signedAt.Add(time.Duration(expires)*time.Second)) {
			return "", false
		}
		request := &http.Request{Method: http.MethodGet, URL: parsed, Host: parsed.Host, Header: make(http.Header)}
		if result, signatureErr := sigv4VerifyPresigned(request, query, nil, true); signatureErr != nil || result != sigv4Verified {
			return "", false
		}
		request.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s, SignedHeaders=%s, Signature=%s",
			query.Get("X-Amz-Credential"), query.Get("X-Amz-SignedHeaders"), query.Get("X-Amz-Signature")))
		request.TLS = &tls.ConnectionState{}
		request.RemoteAddr = "127.0.0.1:0"
		for _, resource := range []string{resourceARN, user.ARN} {
			if allowed, _, registered := iamAuthorize(request, "elasticache:Connect", resource); registered && !allowed {
				return "", false
			}
		}
		return user.UserName, true
	}
}

// ecCheckUserGroupsFor checks the user groups a cache of engine is to have:
// each exists and is active, and a Redis OSS cache takes Redis OSS groups
// only. It answers the fault code and message of the first that fails.
func ecCheckUserGroupsFor(engine string, groups []string) (string, string, int, bool) {
	for _, id := range groups {
		g, ok := ecUserGroups.Get(id)
		switch {
		case !ok:
			return "UserGroupNotFound", fmt.Sprintf("User group %s not found.", id), http.StatusNotFound, false
		case g.Status != "active":
			return "InvalidUserGroupState", fmt.Sprintf("User group %s is %s, not active.", id, g.Status), http.StatusBadRequest, false
		case engine == "redis" && g.Engine != "redis":
			return "InvalidParameterCombination", fmt.Sprintf("User group %s runs %s; a Redis OSS cache takes Redis OSS user groups only.", id, g.Engine), http.StatusBadRequest, false
		}
	}
	return "", "", 0, true
}
