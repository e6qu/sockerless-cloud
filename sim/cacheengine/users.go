package cacheengine

import (
	"fmt"
)

// User is an engine user every node runs, so the engine itself enforces it.
type User struct {
	Name string
	// Passwords authenticate the user and switch it on.
	Passwords []string
	// Rule is the user's ACL SETUSER arguments; nil grants every command,
	// key and channel.
	Rule []string
}

// InvalidRuleError is an ACL rule the engine refused.
type InvalidRuleError struct {
	User string
	Err  error
}

func (e *InvalidRuleError) Error() string {
	return fmt.Sprintf("set user %s from its ACL rule: %v", e.User, e.Err)
}

func (e *InvalidRuleError) Unwrap() error { return e.Err }

// ApplyUsers makes the engine users of nodes the plane's Users, and removes
// every other user but default. A plane whose Config names no Users leaves
// the engine's users as they are.
func (p *Plane) ApplyUsers(nodes []int) error {
	if p.cfg.Users == nil {
		return nil
	}
	users, err := p.cfg.Users()
	if err != nil {
		return err
	}
	desired := map[string]bool{"default": true}
	for _, user := range users {
		desired[user.Name] = true
	}
	for _, index := range nodes {
		reply, err := p.Command(index, "ACL", "USERS")
		if err != nil {
			return err
		}
		existing, _ := reply.([]any)
		for _, name := range existing {
			if !desired[fmt.Sprint(name)] {
				if _, err := p.Command(index, "ACL", "DELUSER", fmt.Sprint(name)); err != nil {
					return err
				}
			}
		}
		for _, user := range users {
			args := []string{"ACL", "SETUSER", user.Name, "reset"}
			if user.Rule == nil || len(user.Passwords) > 0 {
				args = append(args, "on")
			}
			for _, password := range user.Passwords {
				args = append(args, ">"+password)
			}
			if user.Rule == nil {
				args = append(args, "~*", "&*", "+@all")
			} else {
				args = append(args, user.Rule...)
			}
			if _, err := p.Command(index, args...); err != nil {
				return &InvalidRuleError{User: user.Name, Err: err}
			}
		}
	}
	if p.cfg.UsersApplied != nil {
		p.cfg.UsersApplied(users)
	}
	return nil
}
