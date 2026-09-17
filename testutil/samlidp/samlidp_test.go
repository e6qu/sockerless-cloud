package samlidp

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestResponseCarriesASignedAssertion(t *testing.T) {
	idp, err := New("https://idp.example.test/saml")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := idp.Response(Assertion{Subject: "alice", Attributes: map[string][]string{RoleSessionNameAttribute: {"alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<ds:Signature", "<saml:NameID", "alice", AWSAudience} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("response lacks %q", want)
		}
	}
	if !strings.Contains(idp.Metadata(), "X509Certificate") {
		t.Error("metadata carries no certificate")
	}
}
