package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// The bootstrap service account and its key are the well-known credential the
// simulator seeds into its default project: the Google Cloud counterpart of
// the AWS simulator's "test"/"test" access key and the Azure simulator's
// test-client-id application registration. Google's token endpoint mints an
// access token only for a JWT-bearer assertion that a registered key signed,
// so a client's first credential has to be a key an administrator provisioned.
// The simulator publishes this one, private half included, the way the Azurite
// emulator publishes its well-known account key: a client writes it into an
// ordinary service-account credential file and authenticates exactly as with
// a key IAM issued.
const (
	gcpBootstrapProjectID           = "sockerless"
	gcpBootstrapServiceAccountEmail = "sockerless-bootstrap@" + gcpBootstrapProjectID + ".iam.gserviceaccount.com"
	gcpBootstrapUniqueID            = "108374620195837462019"
	gcpBootstrapKeyID               = "49c4c9ee1dffb89ca13534183ea5e484e46abaaa"
	gcpBootstrapPrivateKeyPEM       = `-----BEGIN PRIVATE KEY-----
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

// gcpBootstrapKeyValidBefore is the validBeforeTime IAM reports for a key that
// never expires.
var gcpBootstrapKeyValidBefore = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// iamSeedBootstrapServiceAccount seeds the bootstrap account into the default
// project crmEnsureDefaultProject materializes, with its key registered as an
// uploaded user-managed key: IAM reports both through serviceAccounts.get and
// keys.list, and the token endpoint verifies the key's assertions as it
// verifies any other key's. Seeding is idempotent: a store that already holds
// the rows (a persisted database on restart) keeps them as they are, a
// disabled bit included.
func iamSeedBootstrapServiceAccount() error {
	if _, ok := crmProjects.Get(gcpBootstrapProjectID); !ok {
		return fmt.Errorf("the default project %s does not exist", gcpBootstrapProjectID)
	}
	iamSeedBootstrapOwner()
	saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", gcpBootstrapProjectID, gcpBootstrapServiceAccountEmail)
	if _, ok := iamServiceAccounts.Get(saName); !ok {
		iamServiceAccounts.Put(saName, GCPServiceAccount{
			Name:        saName,
			ProjectId:   gcpBootstrapProjectID,
			UniqueId:    gcpBootstrapUniqueID,
			Email:       gcpBootstrapServiceAccountEmail,
			DisplayName: "Sockerless Bootstrap",
			Description: "The simulator's well-known administrator credential.",
		})
	}
	keyName := saName + "/keys/" + gcpBootstrapKeyID
	if _, ok := iamSAKeys.Get(keyName); ok {
		return nil
	}
	block, _ := pem.Decode([]byte(gcpBootstrapPrivateKeyPEM))
	if block == nil {
		return fmt.Errorf("bootstrap key: no PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("bootstrap key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("bootstrap key is %T, not RSA", parsed)
	}
	id, err := serviceAccountKeyID(&key.PublicKey)
	if err != nil {
		return err
	}
	if id != gcpBootstrapKeyID {
		return fmt.Errorf("bootstrap key id %s does not name the key's public half %s", gcpBootstrapKeyID, id)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("bootstrap public key: %w", err)
	}
	serial, err := hex.DecodeString(gcpBootstrapKeyID)
	if err != nil {
		return fmt.Errorf("bootstrap key id: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serial),
		Subject:               pkix.Name{CommonName: gcpBootstrapServiceAccountEmail},
		Issuer:                pkix.Name{CommonName: gcpBootstrapServiceAccountEmail},
		NotBefore:             saCertificateEpoch,
		NotAfter:              gcpBootstrapKeyValidBefore,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("certify bootstrap key: %w", err)
	}
	iamSAKeys.Put(keyName, GCPServiceAccountKey{
		Name:            keyName,
		KeyAlgorithm:    "KEY_ALG_RSA_2048",
		ValidAfterTime:  saCertificateEpoch.Format(time.RFC3339),
		ValidBeforeTime: gcpBootstrapKeyValidBefore.Format(time.RFC3339),
		KeyType:         "USER_MANAGED",
		KeyOrigin:       "USER_PROVIDED",
	})
	iamSAKeyPublics.Put(keyName, GCPServiceAccountKeyMaterial{
		Name:           keyName,
		PublicKeyPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
	})
	return nil
}

// iamSeedBootstrapOwner grants the bootstrap service account the Owner role
// on the organization every bootstrap resource hangs off, the binding that
// makes it the administrator of the projects and folders under it. A
// persisted organization policy is kept as it is.
func iamSeedBootstrapOwner() {
	key := "organization/" + strings.TrimPrefix(crmDefaultOrganization, "organizations/")
	gcpIAMPolicyWrites.Lock()
	defer gcpIAMPolicyWrites.Unlock()
	if _, ok := gcpResourcePolicies.Get(key); ok {
		return
	}
	gcpResourcePolicies.Put(key, IAMPolicy{
		Bindings: []IAMBinding{{Role: gcpRoleOwner, Members: []string{"serviceAccount:" + gcpBootstrapServiceAccountEmail}}},
		Etag:     gcpPolicyETag(),
		Version:  1,
	})
}
