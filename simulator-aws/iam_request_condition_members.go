package main

import (
	"encoding/json"
	"strconv"
	"time"
)

// iamBodyMembers is one object of an awsJson request body, member by member,
// so a condition key can read a boolean, a number or a list in the type the
// model gives it.
type iamBodyMembers map[string]json.RawMessage

func iamParseBodyMembers(body []byte) iamBodyMembers {
	var members iamBodyMembers
	if json.Unmarshal(body, &members) != nil {
		return nil
	}
	return members
}

func (m iamBodyMembers) object(name string) iamBodyMembers {
	raw, ok := m[name]
	if !ok {
		return nil
	}
	return iamParseBodyMembers(raw)
}

func (m iamBodyMembers) str(name string) (string, bool) {
	var value string
	raw, ok := m[name]
	if !ok || json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false
	}
	return value, true
}

func (m iamBodyMembers) boolean(name string) (string, bool) {
	var value bool
	raw, ok := m[name]
	if !ok || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return strconv.FormatBool(value), true
}

func (m iamBodyMembers) number(name string) (string, bool) {
	var value json.Number
	raw, ok := m[name]
	if !ok || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value.String(), true
}

// timestamp reads an awsJson timestamp, which the protocol sends as epoch
// seconds, in the RFC 3339 form the Date condition operators compare.
func (m iamBodyMembers) timestamp(name string) (string, bool) {
	var seconds float64
	raw, ok := m[name]
	if !ok || json.Unmarshal(raw, &seconds) != nil {
		return "", false
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1e9)
	return time.Unix(whole, nanos).UTC().Format(time.RFC3339), true
}

func (m iamBodyMembers) strings(name string) []string {
	var values []string
	raw, ok := m[name]
	if !ok || json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func (m iamBodyMembers) objects(name string) []iamBodyMembers {
	var values []iamBodyMembers
	raw, ok := m[name]
	if !ok || json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

// setters binds a request's members to a condition context, adding a key only
// when the request carries the member it reads.
func (m iamBodyMembers) setters(ctx map[string][]string) iamBodyMemberSetters {
	return iamBodyMemberSetters{members: m, ctx: ctx}
}

type iamBodyMemberSetters struct {
	members iamBodyMembers
	ctx     map[string][]string
}

func (s iamBodyMemberSetters) set(key string, read func(string) (string, bool), member string) {
	if value, ok := read(member); ok {
		s.ctx[key] = []string{value}
	}
}

func (s iamBodyMemberSetters) str(key, member string) { s.set(key, s.members.str, member) }

func (s iamBodyMemberSetters) boolean(key, member string) { s.set(key, s.members.boolean, member) }

func (s iamBodyMemberSetters) number(key, member string) { s.set(key, s.members.number, member) }

func (s iamBodyMemberSetters) timestamp(key, member string) {
	s.set(key, s.members.timestamp, member)
}

func (s iamBodyMemberSetters) strings(key, member string) {
	if values := s.members.strings(member); len(values) > 0 {
		s.ctx[key] = values
	}
}
