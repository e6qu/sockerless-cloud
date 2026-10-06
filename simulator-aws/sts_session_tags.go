package main

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// AWS STS session tags: the tags a temporary credential carries as principal
// tags, passed on AssumeRole, carried in a SAML assertion's PrincipalTag
// attributes, or in a web identity token's https://aws.amazon.com/tags claim.
// A transitive tag passes on to every session chained from the one it tags.

const (
	samlPrincipalTagAttribute      = "https://aws.amazon.com/SAML/Attributes/PrincipalTag:"
	samlTransitiveTagKeysAttribute = "https://aws.amazon.com/SAML/Attributes/TransitiveTagKeys"
	webIdentityTagsClaim           = "https://aws.amazon.com/tags"
)

// stsSessionTags is the tag set of one session and the keys of it that are
// transitive.
type stsSessionTags struct {
	Tags       []IAMTag
	Transitive []string
}

// stsSessionTagsError is AWS STS's message refusing a session tag set.
type stsSessionTagsError string

func (e stsSessionTagsError) Error() string { return string(e) }

// stsNewSessionTags assembles a session's tags from the transitive tags of the
// session it is chained from and the tags passed for it. Tag keys are
// case-insensitive: a key passed twice, a transitive key naming no tag, and a
// passed tag that would replace an inherited transitive one are refused.
func stsNewSessionTags(inherited stsSessionTags, tags []IAMTag, transitive []string) (stsSessionTags, error) {
	seen := map[string]bool{}
	for _, tag := range tags {
		folded := strings.ToLower(tag.Key)
		if seen[folded] {
			return stsSessionTags{}, stsSessionTagsError("Duplicate tag keys found. Please note that Tag keys are case insensitive.")
		}
		seen[folded] = true
	}
	for _, key := range transitive {
		if !seen[strings.ToLower(key)] {
			return stsSessionTags{}, stsSessionTagsError("The transitive tag key " + key + " names no session tag.")
		}
	}
	out := stsSessionTags{}
	for _, tag := range inherited.Tags {
		if seen[strings.ToLower(tag.Key)] {
			return stsSessionTags{}, stsSessionTagsError("The session tag " + tag.Key + " is a transitive tag of the calling session and cannot be overridden.")
		}
		out.Tags = append(out.Tags, tag)
	}
	out.Tags = append(out.Tags, tags...)
	out.Transitive = append(append(out.Transitive, inherited.Transitive...), transitive...)
	sort.Slice(out.Tags, func(i, j int) bool { return out.Tags[i].Key < out.Tags[j].Key })
	return out, nil
}

// passed reports whether the session receives any tag, which AWS STS
// authorizes as sts:TagSession.
func (s stsSessionTags) passed() bool {
	return len(s.Tags) > 0
}

// conditionContext adds the request keys sts:TagSession is authorized with:
// the tags as aws:RequestTag/<key>, their keys as aws:TagKeys, and the
// transitive keys as sts:TransitiveTagKeys.
func (s stsSessionTags) conditionContext(ctx map[string][]string) map[string][]string {
	out := make(map[string][]string, len(ctx)+len(s.Tags)+2)
	for key, values := range ctx {
		out[key] = values
	}
	var keys []string
	for _, tag := range s.Tags {
		out["aws:RequestTag/"+tag.Key] = []string{tag.Value}
		keys = append(keys, tag.Key)
	}
	if len(keys) > 0 {
		out["aws:TagKeys"] = keys
	}
	if len(s.Transitive) > 0 {
		out["sts:TransitiveTagKeys"] = s.Transitive
	}
	return out
}

// stsInheritedSessionTags are the transitive tags of the session signing r,
// which a role it assumes keeps.
func stsInheritedSessionTags(r *http.Request) stsSessionTags {
	tc, ok := iamTempCreds.Get(iamAccessKeyIDFromRequest(r))
	if !ok {
		return stsSessionTags{}
	}
	out := stsSessionTags{Transitive: tc.TransitiveTagKeys}
	for _, tag := range tc.SessionTags {
		if slices.ContainsFunc(tc.TransitiveTagKeys, func(key string) bool { return strings.EqualFold(key, tag.Key) }) {
			out.Tags = append(out.Tags, tag)
		}
	}
	return out
}

// stsRequestSessionTags reads the Tags and TransitiveTagKeys an AssumeRole or
// GetFederationToken request passes.
func stsRequestSessionTags(r *http.Request) ([]IAMTag, []string) {
	var tags []IAMTag
	for _, tag := range iamRequestTags(r, "sts") {
		tags = append(tags, IAMTag(tag))
	}
	return tags, snsListMembers(r, "TransitiveTagKeys")
}

// sessionTags reads the session tags an assertion carries: one
// PrincipalTag:<key> attribute per tag and the TransitiveTagKeys attribute.
func (a samlAssertion) sessionTags() ([]IAMTag, []string, error) {
	var tags []IAMTag
	for name, values := range a.Attributes {
		key, ok := strings.CutPrefix(name, samlPrincipalTagAttribute)
		if !ok || key == "" {
			continue
		}
		if len(values) != 1 {
			return nil, nil, fmt.Errorf("the SAML attribute %s carries %d values; a session tag has one", name, len(values))
		}
		tags = append(tags, IAMTag{Key: key, Value: values[0]})
	}
	return tags, a.Attributes[samlTransitiveTagKeysAttribute], nil
}

// sessionTags reads the session tags a token carries in its
// https://aws.amazon.com/tags claim: principal_tags maps each key to a
// one-element list, and transitive_tag_keys lists the transitive keys.
func (id webIdentity) sessionTags() ([]IAMTag, []string, error) {
	raw, ok := id.Claims[webIdentityTagsClaim]
	if !ok {
		return nil, nil, nil
	}
	claim, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("the %s claim is not an object", webIdentityTagsClaim)
	}
	var tags []IAMTag
	principal, _ := claim["principal_tags"].(map[string]any)
	for key, value := range principal {
		values := webIdentityClaimValues(value)
		if len(values) != 1 {
			return nil, nil, fmt.Errorf("the principal tag %s carries %d values; a session tag has one", key, len(values))
		}
		tags = append(tags, IAMTag{Key: key, Value: values[0]})
	}
	return tags, webIdentityClaimValues(claim["transitive_tag_keys"]), nil
}

// iamMergeSessionTags is a session's principal tags: its role's tags, with a
// session tag replacing a role tag of the same key.
func iamMergeSessionTags(roleTags, sessionTags []IAMTag) []IAMTag {
	out := make([]IAMTag, 0, len(roleTags)+len(sessionTags))
	for _, tag := range roleTags {
		if !slices.ContainsFunc(sessionTags, func(s IAMTag) bool { return strings.EqualFold(s.Key, tag.Key) }) {
			out = append(out, tag)
		}
	}
	return append(out, sessionTags...)
}
