package main

import (
	"fmt"
	"sort"
	"strings"
)

// A cluster runs its token-auth users and the users its ACL policy defines as
// engine users on every node, so the engine itself enforces them: a token-auth
// user's passwords are its active auth tokens, and a policy user is whatever
// its rule makes it. On a cluster that authenticates with IAM, a principal
// whose email names a policy user connects as that user.

type msRedisUser struct {
	Name   string
	Tokens []string
	// Rule is the ACL SETUSER arguments of the user's ACL policy rule; nil
	// for a token-auth user the policy does not name.
	Rule []string
}

// msRedisTokenUsers are the token-auth users of cluster and their active
// tokens.
func msRedisTokenUsers(cluster string) []msRedisUser {
	prefix := cluster + "/tokenAuthUsers/"
	var users []msRedisUser
	for _, user := range msRedisTokenAuthUsers.List() {
		id, found := strings.CutPrefix(user.Name, prefix)
		if !found || strings.Contains(id, "/") {
			continue
		}
		entry := msRedisUser{Name: id}
		for _, token := range msRedisAuthTokens.List() {
			if strings.HasPrefix(token.Name, user.Name+"/authTokens/") && token.State == "ACTIVE" {
				entry.Tokens = append(entry.Tokens, token.Token)
			}
		}
		sort.Strings(entry.Tokens)
		users = append(users, entry)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users
}

// msRedisSplitAclRule splits an ACL rule into its ACL SETUSER arguments. A
// selector is one argument however many spaces its parentheses hold.
func msRedisSplitAclRule(rule string) ([]string, error) {
	var args []string
	var current strings.Builder
	depth := 0
	for _, r := range rule {
		switch {
		case r == '(':
			depth++
		case r == ')':
			if depth == 0 {
				return nil, fmt.Errorf("ACL rule %q closes a selector it never opened", rule)
			}
			depth--
		case (r == ' ' || r == '\t') && depth == 0:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if depth != 0 {
		return nil, fmt.Errorf("ACL rule %q leaves a selector open", rule)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}

// msRedisValidateAclRules refuses rules no engine can take: every rule needs
// a username other than the default user, each username has one rule, and a
// rule's selectors close.
func msRedisValidateAclRules(rules []MSRedisAclRule) error {
	if len(rules) == 0 {
		return fmt.Errorf("an ACL policy needs at least one rule")
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		if rule.Username == "" || rule.Rule == "" {
			return fmt.Errorf("every ACL rule needs a username and a rule")
		}
		if strings.ContainsAny(rule.Username, " \t") {
			return fmt.Errorf("ACL rule username %q contains whitespace", rule.Username)
		}
		if rule.Username == "default" {
			return fmt.Errorf("the default user is reserved and takes no ACL rule")
		}
		key := strings.ToLower(rule.Username)
		if seen[key] {
			return fmt.Errorf("username %q has more than one ACL rule", rule.Username)
		}
		seen[key] = true
		if _, err := msRedisSplitAclRule(rule.Rule); err != nil {
			return err
		}
	}
	return nil
}

// engineUsers are the users the cluster's engine runs besides its default
// user, and its policy users keyed by lowercased username.
func (p *msRedisPlane) engineUsers() ([]msRedisUser, map[string]string, error) {
	p.mu.RLock()
	tokenAuth, policyName := p.tokenAuth, p.aclPolicy
	p.mu.RUnlock()
	byName := map[string]*msRedisUser{}
	if tokenAuth {
		for _, user := range msRedisTokenUsers(p.name) {
			byName[user.Name] = &user
		}
	}
	policyUsers := map[string]string{}
	if policyName != "" {
		policy, ok := msRedisAclPolicies.Get(policyName)
		if !ok {
			return nil, nil, msRedisOperationFailure(rpcNotFound, "ACL policy %s not found", policyName)
		}
		for _, rule := range policy.Rules {
			args, err := msRedisSplitAclRule(rule.Rule)
			if err != nil {
				return nil, nil, msRedisOperationFailure(rpcInvalidArgument, "%v", err)
			}
			user, ok := byName[rule.Username]
			if !ok {
				user = &msRedisUser{Name: rule.Username}
				byName[rule.Username] = user
			}
			user.Rule = args
			policyUsers[strings.ToLower(rule.Username)] = rule.Username
		}
	}
	users := make([]msRedisUser, 0, len(byName))
	for _, user := range byName {
		users = append(users, *user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users, policyUsers, nil
}

// applyUsers makes the engine users of nodes the cluster's token-auth users
// and its ACL policy's users, and removes every other user but default.
func (p *msRedisPlane) applyUsers(nodes []int) error {
	users, policyUsers, err := p.engineUsers()
	if err != nil {
		return err
	}
	p.mu.RLock()
	iamAuth, secret := p.iamAuth, p.password
	p.mu.RUnlock()
	desired := map[string]bool{"default": true}
	for _, user := range users {
		desired[user.Name] = true
	}
	for _, node := range nodes {
		reply, err := p.command(node, "ACL", "USERS")
		if err != nil {
			return err
		}
		existing, _ := reply.([]any)
		for _, name := range existing {
			if !desired[fmt.Sprint(name)] {
				if _, err := p.command(node, "ACL", "DELUSER", fmt.Sprint(name)); err != nil {
					return err
				}
			}
		}
		for _, user := range users {
			args := []string{"ACL", "SETUSER", user.Name, "reset"}
			if user.Rule == nil || len(user.Tokens) > 0 {
				args = append(args, "on")
			}
			for _, token := range user.Tokens {
				args = append(args, ">"+token)
			}
			if user.Rule == nil {
				args = append(args, "~*", "&*", "+@all")
			} else {
				// The relay authenticates a mapped IAM principal with the
				// engine's credential, so a rule that drops passwords drops
				// that one too.
				if iamAuth && secret != "" {
					args = append(args, ">"+secret)
				}
				args = append(args, user.Rule...)
			}
			if _, err := p.command(node, args...); err != nil {
				return msRedisOperationFailure(rpcInvalidArgument, "set user %s from its ACL rule: %v", user.Name, err)
			}
		}
	}
	p.mu.Lock()
	p.policyUsers = policyUsers
	p.mu.Unlock()
	return nil
}

// engineUserOf is the engine user an IAM principal connects as: the policy
// user its email names, or the default user.
func (p *msRedisPlane) engineUserOf(principal string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if user, ok := p.policyUsers[strings.ToLower(principal)]; ok {
		return user
	}
	return "default"
}

// SetAclPolicy makes policy the cluster's ACL policy. A running engine takes
// its users at once, and a stopped one when it starts.
func (p *msRedisPlane) SetAclPolicy(policy string) error {
	p.mu.Lock()
	p.aclPolicy = policy
	p.mu.Unlock()
	if !p.running() {
		return nil
	}
	return p.applyUsers(p.nodeOrder())
}

// msRedisApplyUsers brings a running cluster's engine users in line with its
// token-auth users and ACL policy.
func msRedisApplyUsers(cluster string) error {
	plane, ok := msRedisLoadPlane(cluster)
	if !ok || !plane.running() {
		return nil
	}
	plane.opMu.Lock()
	defer plane.opMu.Unlock()
	return plane.applyUsers(plane.nodeOrder())
}
