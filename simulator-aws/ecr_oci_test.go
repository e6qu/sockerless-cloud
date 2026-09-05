package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ecrOCITestStores re-seeds the ECR control-plane stores the registry data
// plane consults, so each case starts from a registry holding nothing.
func ecrOCITestStores() {
	// Work started by whatever ran before this must finish before the
	// stores it is reading are replaced.
	AwaitSimulatorBackground()
	ecrRepositories = sim.MakeStore[ECRRepository](nil, "ecr_repositories")
	ecrRepoCreationTemplates = sim.MakeStore[ECRRepositoryCreationTemplate](nil, "ecr_repo_creation_templates")
	ecrRepoPolicies = sim.MakeStore[string](nil, "ecr_repo_policies")
	ecrLifecyclePolicies = sim.MakeStore[ECRLifecyclePolicy](nil, "ecr_lifecycle_policies")
}

// ecrAdmitProbe runs one repository through the data plane's admission and
// reports whether it was let through, together with the refusal it wrote.
func ecrAdmitProbe(t *testing.T, repo string, push bool) (bool, *http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v2/"+repo+"/manifests/v1", nil)
	rec := httptest.NewRecorder()
	admitted := ecrAdmitRepository(rec, req, repo, push)
	return admitted, rec.Result(), rec.Body.String()
}

// ecrRefusalEnvelope reads the code and message out of a Docker Registry HTTP
// API v2 error body.
func ecrRefusalEnvelope(t *testing.T, body string) (code, message string) {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("refusal body is not a registry error envelope (%v): %s", err, body)
	}
	if len(envelope.Errors) != 1 {
		t.Fatalf("refusal must carry exactly one error: %s", body)
	}
	return envelope.Errors[0].Code, envelope.Errors[0].Message
}

// A repository that does not exist is refused with the registry's NAME_UNKNOWN
// 404, naming the repository and the registry it was asked of.
func TestECRRegistryRefusesAnAbsentRepository(t *testing.T) {
	ecrOCITestStores()

	for _, push := range []bool{false, true} {
		admitted, resp, body := ecrAdmitProbe(t, "absent/app", push)
		if admitted {
			t.Fatalf("a repository that does not exist must not be admitted (push=%v)", push)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("refusal must be 404, got %d (push=%v)", resp.StatusCode, push)
		}
		code, message := ecrRefusalEnvelope(t, body)
		if code != "NAME_UNKNOWN" {
			t.Fatalf("refusal code must be NAME_UNKNOWN, got %q", code)
		}
		want := "The repository with name 'absent/app' does not exist in the registry with id '" + ecrRegistryId() + "'"
		if message != want {
			t.Fatalf("refusal message:\n got %q\nwant %q", message, want)
		}
	}

	ecrRepositories.Put("present/app", ECRRepository{RepositoryName: "present/app"})
	if admitted, _, body := ecrAdmitProbe(t, "present/app", false); !admitted {
		t.Fatalf("a repository that exists must be admitted: %s", body)
	}
}

// A push covered by a repository creation template applied for CREATE_ON_PUSH
// creates the repository from that template; a pull for the same name does not.
func TestECRRegistryCreatesOnPushFromItsTemplate(t *testing.T) {
	ecrOCITestStores()
	const lifecycle = `{"rules":[]}`
	const policy = `{"Version":"2012-10-17","Statement":[]}`
	ecrRepoCreationTemplates.Put("team", ECRRepositoryCreationTemplate{
		Prefix:                  "team",
		AppliedFor:              []string{"CREATE_ON_PUSH"},
		ImageTagMutability:      "IMMUTABLE",
		EncryptionConfiguration: &ECREncryptionConfiguration{EncryptionType: "KMS"},
		ResourceTags:            []SMTag{{Key: "owner", Value: "platform"}},
		LifecyclePolicy:         lifecycle,
		RepositoryPolicy:        policy,
	})

	if admitted, resp, _ := ecrAdmitProbe(t, "team/app", false); admitted {
		t.Fatal("a pull must not create a repository from a CREATE_ON_PUSH template")
	} else if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("refusal must be 404, got %d", resp.StatusCode)
	}
	if _, exists := ecrRepositories.Get("team/app"); exists {
		t.Fatal("a refused pull must not have created the repository")
	}

	if admitted, _, body := ecrAdmitProbe(t, "team/app", true); !admitted {
		t.Fatalf("a push covered by a CREATE_ON_PUSH template must be admitted: %s", body)
	}
	created, exists := ecrRepositories.Get("team/app")
	if !exists {
		t.Fatal("the push must have created the repository")
	}
	if created.ImageTagMutability != "IMMUTABLE" {
		t.Fatalf("created repository must carry the template's tag mutability, got %q", created.ImageTagMutability)
	}
	if created.EncryptionConfiguration.EncryptionType != "KMS" {
		t.Fatalf("created repository must carry the template's encryption, got %q", created.EncryptionConfiguration.EncryptionType)
	}
	if len(created.Tags) != 1 || created.Tags[0].Key != "owner" {
		t.Fatalf("created repository must carry the template's resource tags, got %+v", created.Tags)
	}
	if created.RepositoryArn != ecrArn("repository", "team/app") {
		t.Fatalf("created repository must carry its ARN, got %q", created.RepositoryArn)
	}
	if got, _ := ecrRepoPolicies.Get("team/app"); got != policy {
		t.Fatalf("created repository must carry the template's repository policy, got %q", got)
	}
	if got, _ := ecrLifecyclePolicies.Get("team/app"); got.LifecyclePolicyText != lifecycle {
		t.Fatalf("created repository must carry the template's lifecycle policy, got %q", got.LifecyclePolicyText)
	}

	// A push outside the prefix is still refused.
	if admitted, _, _ := ecrAdmitProbe(t, "other/app", true); admitted {
		t.Fatal("a push outside every template prefix must be refused")
	}
}

// The template a push is created from is the most specific one that matches,
// with ROOT applying only where no other template does, and a template applied
// for another scenario never applying to a push at all.
func TestECRCreateOnPushTemplateSelection(t *testing.T) {
	ecrOCITestStores()
	ecrRepoCreationTemplates.Put("prod", ECRRepositoryCreationTemplate{
		Prefix: "prod", AppliedFor: []string{"CREATE_ON_PUSH"}, Description: "prod",
	})
	ecrRepoCreationTemplates.Put("prod/team", ECRRepositoryCreationTemplate{
		Prefix: "prod/team", AppliedFor: []string{"CREATE_ON_PUSH"}, Description: "prod/team",
	})
	ecrRepoCreationTemplates.Put("cache", ECRRepositoryCreationTemplate{
		Prefix: "cache", AppliedFor: []string{"PULL_THROUGH_CACHE"}, Description: "cache",
	})

	for _, tc := range []struct {
		repo    string
		want    string
		matched bool
	}{
		{"prod/team/app", "prod/team", true},
		{"prod/other/app", "prod", true},
		{"prodigy/app", "", false},
		{"cache/app", "", false},
		{"elsewhere/app", "", false},
	} {
		template, matched := ecrCreateOnPushTemplate(tc.repo)
		if matched != tc.matched {
			t.Fatalf("%s: matched=%v, want %v", tc.repo, matched, tc.matched)
		}
		if matched && template.Description != tc.want {
			t.Fatalf("%s: matched template %q, want %q", tc.repo, template.Description, tc.want)
		}
	}

	// ROOT covers the repositories no other template matches, and only those.
	ecrRepoCreationTemplates.Put("ROOT", ECRRepositoryCreationTemplate{
		Prefix: "ROOT", AppliedFor: []string{"CREATE_ON_PUSH"}, Description: "root",
	})
	for repo, want := range map[string]string{
		"elsewhere/app":  "root",
		"cache/app":      "root",
		"prod/other/app": "prod",
		"prod/team/app":  "prod/team",
	} {
		template, matched := ecrCreateOnPushTemplate(repo)
		if !matched {
			t.Fatalf("%s: no template matched with ROOT present", repo)
		}
		if template.Description != want {
			t.Fatalf("%s: matched template %q, want %q", repo, template.Description, want)
		}
	}
}
