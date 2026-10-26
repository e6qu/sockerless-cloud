// Package gcpbootstrap holds the Google Cloud simulator's well-known bootstrap
// service-account key, the credential a client authenticates with before it
// has provisioned any of its own, and writes it as the service-account
// credential file Google's client libraries, gcloud and the Terraform provider
// read.
package gcpbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	ProjectID = "sockerless"
	Email     = "sockerless-bootstrap@sockerless.iam.gserviceaccount.com"
	ClientID  = "108374620195837462019"
	KeyID     = "49c4c9ee1dffb89ca13534183ea5e484e46abaaa"
	// PrivateKeyPEM is the private half the simulator publishes; it holds the
	// public half registered as the account's key.
	PrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQCgIehWBhPTjtE4
Dq1bxsFfLbjm8gQXLIBXDExpfTN5iiQaJXmiR7kZXO8y+IGgh2BuJCJNpxWwrPHj
qWaymDd/qjaU8Ni15o84+0V1zKtoQ+tmnQqDi0VTEenLpV8iRYG68ambB1FZ5F+A
4n3IgAMVfUXe6GUdMaRLQUZv4blFimKxNORR0NzL3msr7toxmHhTikmiN23cIBFB
OEIHIlXXe2az54FHBPXuuGTNhsDKMRiw32+XIeXi+nRF3XuopiIVUOihhbqmjomV
+kgSx5O20QwQ8ITtVXq75Yp9KFCTtyCUY2MtPIVRTnMYTkFrUV613hEOc2MiI+hU
7BgXk5kxAgMBAAECggEADVRNOe3nFBbhCPzFxuwwN/s/FzBBkWlBpJ1QNf63PHB4
iVmhupWkTX1sJs7DQYjLSNfGqDaWil6XAvecphSgGAcj5Hl43Ilc1iYqVcAr63Gt
5FyBncoHwv6seqhhFyH4hsZrcw1kouos3fZceIZZW15SZNfs5MV3UtFT6ue765MT
4saqJqCSt9i26PnP6VILDCpmcXoPF5gMlSNLKmp7q0jlxLOsXalQULFGPOg7MEE0
maOzd9M4A5B3dUo4ucBG6BdjK763VasR7NeWKlzSiotG/NS+fqZy6C5bNPzxKktt
EDZlLxtqiahthmFzRbpUj4hqSiBmY92wG+2HnBynzQKBgQDRO3SGDlTZ6RbTzYvr
PJGQY6u6J+jT3JHoBMs6cGVWOJceu4UUSreTO4oYlDcLWMkKAF3snZha6us5h0k2
NIO0MVEnx+VWDIHpHEA1VCoAe48cufN1CXsbZRwF14zxSY1IpZ+kLVwrh4UQt7Ns
RdcsB+xC0229K439EiUYeDCvRQKBgQDD7OW8y/A74f8tI8+QSXCFo1VdDGc8MfjX
ieKaeRvDn5GsxvAGjRODenQL8s35TOztUdnn5AAouaWqX1T0NkvDyRKMD/w/1vQj
XxtXY8zS3ADzbdrNNhuBOeQek15iDxzo8vRwIFtpJEaZ47uFoapg9MhLhj/TWlZt
dIOD7Wn6/QKBgQCxz5QsJNvJQIRMxT6mUBfonDuhfS8pdSZuBjzr0f7cskUDMhtb
bk/qCMxILRVGSFK5dCxd3GRQx5nVjvoR3bkbllnAs+lbIk/42jBa4ca8GEU8BW83
xDHhyyBweolluLsfxNkRfhb5SzczSNprJkNBQQ5wkBuPiwckzZnG3MWK/QKBgQC6
OjGAS8Rz1vNqn5BKUFPg4ohgIkr6H3GsxcYbGCR4w/J+bLV3NJ8oqLsnJEn9vc2y
Icviq4gvLY63K1gfrIBFf5jegs88iv+Ro3Pkk9r7TdylhXSk+9oW8cTP/zmGMWFE
VrJa5ZadkLp+ycTTVwAQmnrLtrZyGXrvHQFo48CJrQKBgG2E+MuCRr0kRc7jEkHE
tJUXWzmzh7IgglfexVWc2/PLNlP2foy/8oiE+hn4QO9/xd3q3LIsNl5xlT61AtZK
XCB2hUM2czc3i/9DRIo249YLUSkUGJB6PHXNkq3b2NEL2uKkvz6Wzpof30OV7Pn6
eQDiTQyEvNvkuOrWUcehkn2z
-----END PRIVATE KEY-----
`
)

// CredentialJSON is the service-account credential file for the bootstrap
// key, in the shape IAM's keys.create returns, whose token_uri names the
// simulator's token endpoint: the one coordinate a client pointed at the
// simulator swaps.
func CredentialJSON(tokenURI string) []byte {
	raw, err := json.MarshalIndent(struct {
		Type                    string `json:"type"`
		ProjectID               string `json:"project_id"`
		PrivateKeyID            string `json:"private_key_id"`
		PrivateKey              string `json:"private_key"`
		ClientEmail             string `json:"client_email"`
		ClientID                string `json:"client_id"`
		AuthURI                 string `json:"auth_uri"`
		TokenURI                string `json:"token_uri"`
		AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
		ClientX509CertURL       string `json:"client_x509_cert_url"`
		UniverseDomain          string `json:"universe_domain"`
	}{
		Type:                    "service_account",
		ProjectID:               ProjectID,
		PrivateKeyID:            KeyID,
		PrivateKey:              PrivateKeyPEM,
		ClientEmail:             Email,
		ClientID:                ClientID,
		AuthURI:                 "https://accounts.google.com/o/oauth2/auth",
		TokenURI:                tokenURI,
		AuthProviderX509CertURL: "https://www.googleapis.com/oauth2/v1/certs",
		ClientX509CertURL:       "https://www.googleapis.com/robot/v1/metadata/x509/" + Email,
		UniverseDomain:          "googleapis.com",
	}, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("marshal the bootstrap credential: %v", err))
	}
	return raw
}

// WriteCredentialFile writes CredentialJSON(tokenURI) to path, readable by its
// owner only, as a downloaded key file is kept.
func WriteCredentialFile(path, tokenURI string) error {
	return os.WriteFile(path, CredentialJSON(tokenURI), 0o600)
}
