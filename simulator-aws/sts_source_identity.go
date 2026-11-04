package main

import (
	"fmt"
	"net/http"
	"regexp"
)

// A source identity names the person or application behind a role session.
// The first assumption sets it, every session chained from that one keeps
// it, and a role's trust policy must allow sts:SetSourceIdentity for a
// session that carries one, as the IAM User Guide's "Monitor and control
// actions taken with assumed roles" describes.

const (
	stsSourceIdentitySAMLAttribute = "https://aws.amazon.com/SAML/Attributes/SourceIdentity"
	stsSourceIdentityTokenClaim    = "https://aws.amazon.com/source_identity"
	stsSourceIdentityMinLength     = 2
	stsSourceIdentityMaxLength     = 64
)

var stsSourceIdentityPattern = regexp.MustCompile(`^[\w+=,.@-]*$`)

// stsValidSourceIdentity checks a source identity against the length and
// pattern the AWS STS model declares for sourceIdentityType.
func stsValidSourceIdentity(value string) *stsRequestError {
	switch {
	case len(value) < stsSourceIdentityMinLength:
		return stsValidationError("1 validation error detected: Value '%s' at 'sourceIdentity' failed to satisfy constraint: Member must have length greater than or equal to %d", value, stsSourceIdentityMinLength)
	case len(value) > stsSourceIdentityMaxLength:
		return stsValidationError("1 validation error detected: Value '%s' at 'sourceIdentity' failed to satisfy constraint: Member must have length less than or equal to %d", value, stsSourceIdentityMaxLength)
	case !stsSourceIdentityPattern.MatchString(value):
		return stsValidationError("1 validation error detected: Value '%s' at 'sourceIdentity' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\w+=,.@-]*", value)
	}
	return nil
}

// stsSessionSourceIdentity is the source identity a session assumed from
// the caller of r carries: the caller session's own, which a chained
// assumption cannot change, or the one passed, which a session that has
// none sets.
func stsSessionSourceIdentity(r *http.Request, passed string) (string, *stsRequestError) {
	inherited := stsCallerSourceIdentity(r)
	if passed == "" {
		return inherited, nil
	}
	if err := stsValidSourceIdentity(passed); err != nil {
		return "", err
	}
	if inherited != "" && inherited != passed {
		return "", stsAccessDenied("The source identity %s of the calling session cannot be changed to %s.", inherited, passed)
	}
	return passed, nil
}

// stsCallerSourceIdentity is the source identity of the session that signed
// r, empty for a long-term credential or a session that carries none.
func stsCallerSourceIdentity(r *http.Request) string {
	if tc, ok := iamTempCreds.Get(iamAccessKeyIDFromRequest(r)); ok {
		return tc.SourceIdentity
	}
	return ""
}

// stsSourceIdentityXML is the SourceIdentity element of a response, empty
// for a session that carries none.
func stsSourceIdentityXML(value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("<SourceIdentity>%s</SourceIdentity>", xmlEscape(value))
}

// stsWithSourceIdentity is ctx with sts:SourceIdentity set, for the trust
// policy's sts:SetSourceIdentity check.
func stsWithSourceIdentity(ctx map[string][]string, value string) map[string][]string {
	out := make(map[string][]string, len(ctx)+1)
	for k, v := range ctx {
		out[k] = v
	}
	out["sts:SourceIdentity"] = []string{value}
	return out
}

func (id webIdentity) claimStrings(claim string) []string {
	return webIdentityClaimValues(id.Claims[claim])
}

// stsFederatedSourceIdentity reads the source identity a SAML assertion or a
// web identity token carries, refusing a value outside the model's
// constraint as the provider's claim.
func stsFederatedSourceIdentity(values []string) (string, error) {
	if len(values) == 0 || values[0] == "" {
		return "", nil
	}
	if err := stsValidSourceIdentity(values[0]); err != nil {
		return "", fmt.Errorf("%s", err.message)
	}
	return values[0], nil
}
