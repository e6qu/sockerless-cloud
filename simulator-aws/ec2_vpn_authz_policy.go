package main

import (
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// EC2ClientVpnAuthorizationPolicy is the one Cedar authorization policy a
// Client VPN endpoint can carry. It has no identifier of its own: the endpoint
// names it.
type EC2ClientVpnAuthorizationPolicy struct {
	ClientVpnEndpointId string
	PolicyDocument      string
	Description         string
	ShadowMode          string
	Status              string
}

var ec2ClientVpnAuthzPolicies sim.Store[EC2ClientVpnAuthorizationPolicy]

func registerEC2ClientVpnAuthorizationPolicy(r *AWSQueryRouter, srv *sim.Server) {
	ec2ClientVpnAuthzPolicies = sim.MakeStore[EC2ClientVpnAuthorizationPolicy](srv.DB(), "ec2_client_vpn_authorization_policies")
	r.Register("GetClientVpnEndpointAuthorizationPolicy", handleGetClientVpnEndpointAuthorizationPolicy)
	r.Register("ModifyClientVpnEndpointAuthorizationPolicy", handleModifyClientVpnEndpointAuthorizationPolicy)
	r.Register("DeleteClientVpnEndpointAuthorizationPolicy", handleDeleteClientVpnEndpointAuthorizationPolicy)
}

// ec2ClientVpnAuthzPolicyEndpoint reads the endpoint a policy request names and
// answers the request's DryRun, reporting whether the handler carries on.
func ec2ClientVpnAuthzPolicyEndpoint(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.FormValue("ClientVpnEndpointId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ClientVpnEndpointId", http.StatusBadRequest)
		return "", false
	}
	if _, ok := ec2ClientVpnEndpoint.Get(id); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", id), http.StatusBadRequest)
		return "", false
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return "", false
	}
	return id, true
}

func handleGetClientVpnEndpointAuthorizationPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := ec2ClientVpnAuthzPolicyEndpoint(w, r)
	if !ok {
		return
	}
	body := "<clientVpnEndpointId>" + ec2EscapeXML(id) + "</clientVpnEndpointId>"
	if policy, ok := ec2ClientVpnAuthzPolicies.Get(id); ok {
		body += "<policyDocument>" + ec2EscapeXML(policy.PolicyDocument) + "</policyDocument>"
		if policy.Description != "" {
			body += "<description>" + ec2EscapeXML(policy.Description) + "</description>"
		}
		body += "<shadowMode>" + policy.ShadowMode + "</shadowMode><status>" + policy.Status + "</status>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetClientVpnEndpointAuthorizationPolicyResponse %s><requestId>%s</requestId>%s</GetClientVpnEndpointAuthorizationPolicyResponse>`,
		ec2Xmlns(), sim.NewUUID(), body)
}

// handleModifyClientVpnEndpointAuthorizationPolicy creates the endpoint's
// policy or replaces the values the request sets, keeping the others, and
// applies it behind the request.
func handleModifyClientVpnEndpointAuthorizationPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := ec2ClientVpnAuthzPolicyEndpoint(w, r)
	if !ok {
		return
	}
	shadowMode := r.FormValue("ShadowMode")
	if shadowMode != "" && shadowMode != "enabled" && shadowMode != "disabled" {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("Value (%s) for parameter ShadowMode is invalid. Valid values are: enabled, disabled", shadowMode), http.StatusBadRequest)
		return
	}
	policy, exists := ec2ClientVpnAuthzPolicies.Get(id)
	if exists && policy.Status == "deleting" {
		ec2ErrorXML(w, "IncorrectState",
			fmt.Sprintf("The authorization policy of Client VPN endpoint '%s' is being deleted", id), http.StatusBadRequest)
		return
	}
	if !exists {
		if _, set := r.Form["PolicyDocument"]; !set {
			ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter PolicyDocument", http.StatusBadRequest)
			return
		}
		policy = EC2ClientVpnAuthorizationPolicy{ClientVpnEndpointId: id, ShadowMode: "disabled", Status: "creating"}
	} else {
		policy.Status = "updating"
	}
	if _, set := r.Form["PolicyDocument"]; set {
		policy.PolicyDocument = r.FormValue("PolicyDocument")
	}
	if _, set := r.Form["Description"]; set {
		policy.Description = r.FormValue("Description")
	}
	if shadowMode != "" {
		policy.ShadowMode = shadowMode
	}
	ec2ClientVpnAuthzPolicies.Put(id, policy)
	applying := policy.Status
	bg.Go(func() {
		ec2ClientVpnAuthzPolicies.Update(id, func(p *EC2ClientVpnAuthorizationPolicy) {
			if p.Status == applying {
				p.Status = "active"
			}
		})
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyClientVpnEndpointAuthorizationPolicyResponse %s><requestId>%s</requestId><status>%s</status></ModifyClientVpnEndpointAuthorizationPolicyResponse>`,
		ec2Xmlns(), sim.NewUUID(), policy.Status)
}

// handleDeleteClientVpnEndpointAuthorizationPolicy marks the policy deleting
// and removes it behind the request.
func handleDeleteClientVpnEndpointAuthorizationPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := ec2ClientVpnAuthzPolicyEndpoint(w, r)
	if !ok {
		return
	}
	if _, exists := ec2ClientVpnAuthzPolicies.Get(id); exists {
		ec2ClientVpnAuthzPolicies.Update(id, func(p *EC2ClientVpnAuthorizationPolicy) { p.Status = "deleting" })
		bg.Go(func() {
			if p, ok := ec2ClientVpnAuthzPolicies.Get(id); ok && p.Status == "deleting" {
				ec2ClientVpnAuthzPolicies.Delete(id)
			}
		})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteClientVpnEndpointAuthorizationPolicyResponse %s><requestId>%s</requestId><status>deleting</status></DeleteClientVpnEndpointAuthorizationPolicyResponse>`,
		ec2Xmlns(), sim.NewUUID())
}
