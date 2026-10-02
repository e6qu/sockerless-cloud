package main

import (
	"fmt"
	"sort"
	"strings"
)

// A cluster that authenticates with tokens runs each token-auth user as an
// engine user of the same name whose passwords are the user's active auth
// tokens, on every node, so the engine itself refuses a token that was never
// issued or has been deleted.

type msRedisUser struct {
	Name   string
	Tokens []string
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

// applyUsers makes the engine users of nodes the cluster's token-auth users.
func (p *msRedisPlane) applyUsers(nodes []int) error {
	p.mu.RLock()
	tokenAuth := p.tokenAuth
	p.mu.RUnlock()
	if !tokenAuth {
		return nil
	}
	users := msRedisTokenUsers(p.name)
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
			args := []string{"ACL", "SETUSER", user.Name, "reset", "on"}
			for _, token := range user.Tokens {
				args = append(args, ">"+token)
			}
			args = append(args, "~*", "&*", "+@all")
			if _, err := p.command(node, args...); err != nil {
				return err
			}
		}
	}
	return nil
}

// msRedisApplyUsers brings a running cluster's engine users in line with its
// token-auth users.
func msRedisApplyUsers(cluster string) error {
	plane, ok := msRedisLoadPlane(cluster)
	if !ok || !plane.running() {
		return nil
	}
	plane.opMu.Lock()
	defer plane.opMu.Unlock()
	return plane.applyUsers(plane.nodeOrder())
}
