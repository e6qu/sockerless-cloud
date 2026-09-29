package sim

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	RegistryError(rec, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required",
		[]map[string]any{{"Type": "repository", "Name": "app", "Action": "pull"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Fatalf("Docker-Distribution-Api-Version = %q", got)
	}
	want := `{"errors":[{"code":"UNAUTHORIZED","detail":[{"Action":"pull","Name":"app","Type":"repository"}],"message":"authentication required"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}

	rec = httptest.NewRecorder()
	RegistryError(rec, http.StatusForbidden, "DENIED", "no", nil)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"errors":[{"code":"DENIED","message":"no"}]}` {
		t.Fatalf("body without detail = %s", got)
	}
}

func TestChallenges(t *testing.T) {
	for got, want := range map[string]string{
		BearerChallenge("https://r/v2/token", "", "", ""):                                    `Bearer realm="https://r/v2/token"`,
		BearerChallenge("https://r/oauth2/token", "r", "repository:a:pull", "invalid_token"): `Bearer realm="https://r/oauth2/token",service="r",scope="repository:a:pull",error="invalid_token"`,
		BasicChallenge("https://1234.dkr.ecr.eu-west-2.amazonaws.com/", "ecr.amazonaws.com"): `Basic realm="https://1234.dkr.ecr.eu-west-2.amazonaws.com/",service="ecr.amazonaws.com"`,
	} {
		if got != want {
			t.Errorf("challenge = %s\nwant        %s", got, want)
		}
	}
}

func TestParseAuthorization(t *testing.T) {
	for header, want := range map[string][2]string{
		"Bearer abc":     {"Bearer", "abc"},
		"  basic   xyz ": {"basic", "xyz"},
		"Basic":          {"Basic", ""},
		"":               {"", ""},
		"Basic\tabc":     {"Basic\tabc", ""},
	} {
		scheme, credential := ParseAuthorization(header)
		if scheme != want[0] || credential != want[1] {
			t.Errorf("ParseAuthorization(%q) = %q, %q; want %q, %q", header, scheme, credential, want[0], want[1])
		}
	}
	user, pass, ok := BasicCredential(base64.StdEncoding.EncodeToString([]byte("AWS:p:w")))
	if !ok || user != "AWS" || pass != "p:w" {
		t.Fatalf("BasicCredential = %q %q %v", user, pass, ok)
	}
	if _, _, ok := BasicCredential(base64.StdEncoding.EncodeToString([]byte("nocolon"))); ok {
		t.Fatal("a credential without a colon decoded")
	}
	if _, _, ok := BasicCredential("!!!"); ok {
		t.Fatal("non-base64 decoded")
	}
}

func TestParseRegistryScopes(t *testing.T) {
	got := ParseRegistryScopes([]string{"repository:a/b:pull,push registry:catalog:*", "repository:c:", "bad", "repository::pull"})
	want := []RegistryScope{
		{Type: "repository", Name: "a/b", Actions: []string{"pull", "push"}},
		{Type: "registry", Name: "catalog", Actions: []string{"*"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %#v", got)
	}
	if got[0].String() != "repository:a/b:pull,push" {
		t.Fatalf("String = %q", got[0].String())
	}
	scope, ok := ParseRegistryScope("repository:x:")
	if !ok || scope.Name != "x" || len(scope.Actions) != 0 {
		t.Fatalf("scope with no action = %#v, %v", scope, ok)
	}
}

func TestImageOnPort(t *testing.T) {
	for image, want := range map[string]bool{
		"localhost:4566/repo:tag":                  true,
		"127.0.0.1:4566/a/b":                       true,
		"localhost:4567/repo":                      false,
		"123.dkr.ecr.us-east-1.amazonaws.com/repo": false,
		"repo:4566":                                false,
	} {
		if got := ImageOnPort(image, 4566); got != want {
			t.Errorf("ImageOnPort(%q) = %v, want %v", image, got, want)
		}
	}
}
