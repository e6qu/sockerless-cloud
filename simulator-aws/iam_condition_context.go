package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Resource-scoped + service-specific IAM condition keys (#661). The evaluator
// (#660) already supports the operators; this feeds the request's target
// resource into the condition context so tag- and cluster-conditioned grants
// enforce faithfully:
//
//   - aws:ResourceTag/<k> (and the service-prefixed ec2:ResourceTag/<k>) — the
//     tags of the resource the request targets (e.g. the volume DeleteVolume
//     acts on), so a policy allowing the action only on resources carrying a
//     given tag matches when, and only when, the resource carries it.
//   - ecs:cluster — the cluster ARN an ECS task operation targets.
//   - aws:RequestTag/<k> + aws:TagKeys — the tags supplied on a tag-on-create /
//     CreateTags request.

// iamPopulateGlobalConditionKeys adds the global (`aws:`) condition keys that
// derive from the request envelope and the calling principal: the request time,
// transport, user-agent, the principal ARN + its tags, the resource account, and
// MFA state. Service-to-service keys (aws:SourceArn/SourceAccount/CalledVia/
// ViaAWSService) are intentionally absent — the sim originates direct client
// calls, not service-initiated ones, so in real AWS those keys are absent here
// too. aws:PrincipalOrgID is absent because the sim models a single account with
// no Organizations slice.
func iamPopulateGlobalConditionKeys(r *http.Request, akid, principalArn, userName string, ctx map[string][]string) {
	now := time.Now().UTC()
	ctx["aws:CurrentTime"] = []string{now.Format(time.RFC3339)}
	ctx["aws:EpochTime"] = []string{strconv.FormatInt(now.Unix(), 10)}
	ctx["aws:SecureTransport"] = []string{strconv.FormatBool(r.TLS != nil)}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		ctx["aws:UserAgent"] = []string{ua}
	}
	if principalArn != "" {
		ctx["aws:PrincipalArn"] = []string{principalArn}
	}
	ctx["aws:ResourceAccount"] = []string{awsAccountID()}

	// Principal tags (a user's tags, or an assumed role's tags).
	for _, t := range iamPrincipalTags(akid, userName) {
		ctx["aws:PrincipalTag/"+t.Key] = []string{t.Value}
	}

	// MFA: present only for an MFA-authenticated temporary session.
	mfa, age := iamSessionMFA(akid)
	ctx["aws:MultiFactorAuthPresent"] = []string{strconv.FormatBool(mfa)}
	if mfa {
		ctx["aws:MultiFactorAuthAge"] = []string{strconv.FormatInt(age, 10)}
	}
}

// iamPrincipalTags returns the calling principal's tags (user tags for an AKIA
// key; the assumed role's tags for an ASIA session).
func iamPrincipalTags(akid, userName string) []IAMTag {
	if userName != "" {
		if u, ok := iamUsers.Get(userName); ok {
			return u.Tags
		}
	}
	if tc, ok := iamTempCreds.Get(akid); ok && tc.RoleName != "" {
		if role, rok := iamRoles.Get(tc.RoleName); rok {
			return role.Tags
		}
	}
	return nil
}

// iamSessionMFA reports whether the credential is an MFA-authenticated session
// and, if so, its age in seconds.
func iamSessionMFA(akid string) (present bool, ageSeconds int64) {
	tc, ok := iamTempCreds.Get(akid)
	if !ok || !tc.MFA {
		return false, 0
	}
	if created, err := time.Parse(time.RFC3339, tc.CreatedAt); err == nil {
		return true, int64(time.Since(created).Seconds())
	}
	return true, 0
}

// iamPopulateResourceConditionKeys augments ctx with the resource-scoped and
// service-specific condition keys implied by the request.
func iamPopulateResourceConditionKeys(r *http.Request, action string, ctx map[string][]string) {
	service := strings.SplitN(action, ":", 2)[0]
	switch service {
	case "ec2":
		iamPopulateEC2ResourceTags(r, ctx)
	case "ecs":
		iamPopulateECSCluster(r, ctx)
	default:
		// Every other tag-storing sim service resolves the request's target
		// resource into aws:ResourceTag/<k> + <service>:ResourceTag/<k>.
		iamPopulateServiceResourceTags(r, service, ctx)
	}
	iamPopulateRequestTags(r, ctx)
}

// iamPopulateEC2ResourceTags resolves the tags of the EC2 resource the request
// targets and exposes them as aws:ResourceTag/<k> and ec2:ResourceTag/<k>.
func iamPopulateEC2ResourceTags(r *http.Request, ctx map[string][]string) {
	tags, ok := iamEC2RequestResourceTags(r)
	if !ok {
		return
	}
	for _, t := range tags {
		ctx["aws:ResourceTag/"+t.Key] = []string{t.Value}
		ctx["ec2:ResourceTag/"+t.Key] = []string{t.Value}
	}
}

// iamEC2RequestResourceTags returns the tags of the first EC2 resource the
// request references by id (volume / snapshot / instance / network interface).
func iamEC2RequestResourceTags(r *http.Request) ([]EC2Tag, bool) {
	for _, param := range []string{"VolumeId", "SnapshotId", "InstanceId", "InstanceId.1", "NetworkInterfaceId", "ResourceId", "ResourceId.1"} {
		id := r.FormValue(param)
		if id == "" {
			continue
		}
		switch {
		case strings.HasPrefix(id, "vol-"):
			if v, ok := ec2Volumes.Get(id); ok {
				return v.Tags, true
			}
		case strings.HasPrefix(id, "snap-"):
			if s, ok := ec2Snapshots.Get(id); ok {
				return s.Tags, true
			}
		case strings.HasPrefix(id, "i-"):
			if i, ok := ec2Instances.Get(id); ok {
				return i.Tags, true
			}
		case strings.HasPrefix(id, "eni-"):
			if e, ok := ec2NetworkInterfaces.Get(id); ok {
				return e.Tags, true
			}
		}
	}
	return nil, false
}

// iamPopulateECSCluster exposes ecs:cluster (the targeted cluster's ARN) for an
// ECS task operation. ECS is awsJson, so the cluster lives in the request body;
// the body is read and restored so the downstream handler still sees it.
func iamPopulateECSCluster(r *http.Request, ctx map[string][]string) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return
	}
	var req struct {
		Cluster string `json:"cluster"`
	}
	if json.Unmarshal(body, &req) != nil {
		return
	}
	name := req.Cluster
	if name == "" {
		name = "default"
	}
	arn := name
	if !strings.HasPrefix(name, "arn:") {
		arn = ecsArn("cluster", name)
	}
	ctx["ecs:cluster"] = []string{arn}
}

// iamPopulateRequestTags exposes aws:RequestTag/<k> + aws:TagKeys from the tags
// supplied on a tag-on-create / CreateTags request (Tag.N.Key/Value form).
func iamPopulateRequestTags(r *http.Request, ctx map[string][]string) {
	tags := parseIndexedTags(r, "Tag")
	if len(tags) == 0 {
		return
	}
	var keys []string
	for _, t := range tags {
		ctx["aws:RequestTag/"+t.Key] = []string{t.Value}
		keys = append(keys, t.Key)
	}
	ctx["aws:TagKeys"] = keys
}
