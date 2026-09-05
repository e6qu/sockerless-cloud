package main

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// EC2KeyPair models an EC2 key pair. CreateKeyPair generates a real RSA key and
// returns the private material; ImportKeyPair stores a caller-supplied public
// key. The fingerprint is the MD5 of the key material (colon-hex) — a real,
// deterministic value derived from the key (the attribute is computed, so it
// need not byte-match AWS's exact per-algorithm digest).
type EC2KeyPair struct {
	KeyName        string
	KeyPairId      string
	KeyFingerprint string
	KeyType        string
	PublicKey      string
	Tags           []EC2Tag
}

var ec2KeyPairs sim.Store[EC2KeyPair]

// ec2AmiIDFromName derives a stable ami-id from an image name so a
// `data.aws_ami` filter lookup resolves to the same id across plans.
func ec2AmiIDFromName(name string) string {
	sum := md5.Sum([]byte(name))
	return "ami-" + fmt.Sprintf("%x", sum)[:17]
}

func ec2Fingerprint(data []byte) string {
	sum := md5.Sum(data)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

func handleCreateKeyPair(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("KeyName")
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter KeyName", http.StatusBadRequest)
		return
	}
	if _, ok := ec2KeyPairs.Get(name); ok {
		ec2ErrorXML(w, "InvalidKeyPair.Duplicate", fmt.Sprintf("The keypair %q already exists.", name), http.StatusBadRequest)
		return
	}
	keyType := r.FormValue("KeyType")
	if keyType == "" {
		keyType = "rsa"
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	kp := EC2KeyPair{
		KeyName:        name,
		KeyPairId:      ec2ID("key"),
		KeyFingerprint: ec2Fingerprint(pubDER),
		KeyType:        keyType,
		Tags:           parseTags(r),
	}
	ec2KeyPairs.Put(name, kp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateKeyPairResponse %s><requestId>%s</requestId><keyName>%s</keyName><keyFingerprint>%s</keyFingerprint><keyMaterial>%s</keyMaterial><keyPairId>%s</keyPairId></CreateKeyPairResponse>`,
		ec2Xmlns(), generateUUID(), kp.KeyName, kp.KeyFingerprint, xmlEscape(string(privPEM)), kp.KeyPairId)
}

func handleImportKeyPair(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("KeyName")
	material := r.FormValue("PublicKeyMaterial")
	if name == "" || material == "" {
		ec2ErrorXML(w, "MissingParameter", "KeyName and PublicKeyMaterial are required", http.StatusBadRequest)
		return
	}
	if _, ok := ec2KeyPairs.Get(name); ok {
		ec2ErrorXML(w, "InvalidKeyPair.Duplicate", fmt.Sprintf("The keypair %q already exists.", name), http.StatusBadRequest)
		return
	}
	// The SDK base64-encodes the OpenSSH public key; some callers send it raw.
	pubKey := material
	if decoded, err := base64.StdEncoding.DecodeString(material); err == nil {
		pubKey = string(decoded)
	}
	keyType := "rsa"
	if strings.Contains(pubKey, "ssh-ed25519") {
		keyType = "ed25519"
	}
	kp := EC2KeyPair{
		KeyName:        name,
		KeyPairId:      ec2ID("key"),
		KeyFingerprint: ec2Fingerprint([]byte(pubKey)),
		KeyType:        keyType,
		PublicKey:      pubKey,
		Tags:           parseTags(r),
	}
	ec2KeyPairs.Put(name, kp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportKeyPairResponse %s><requestId>%s</requestId><keyName>%s</keyName><keyFingerprint>%s</keyFingerprint><keyPairId>%s</keyPairId></ImportKeyPairResponse>`,
		ec2Xmlns(), generateUUID(), kp.KeyName, kp.KeyFingerprint, kp.KeyPairId)
}

func handleDeleteKeyPair(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("KeyName")
	keyPairID := r.FormValue("KeyPairId")
	if keyPairID != "" {
		for _, kp := range ec2KeyPairs.List() {
			if kp.KeyPairId == keyPairID {
				ec2KeyPairs.Delete(kp.KeyName)
			}
		}
	} else {
		ec2KeyPairs.Delete(name)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteKeyPairResponse %s><requestId>%s</requestId><return>true</return></DeleteKeyPairResponse>`, ec2Xmlns(), generateUUID())
}

func ec2KeyPairMatchesFilters(kp EC2KeyPair, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "key-name":
			if !ec2StrInValues(kp.KeyName, vals) {
				return false
			}
		case "key-pair-id":
			if !ec2StrInValues(kp.KeyPairId, vals) {
				return false
			}
		case "key-type":
			if !ec2StrInValues(kp.KeyType, vals) {
				return false
			}
		case "fingerprint":
			if !ec2StrInValues(kp.KeyFingerprint, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, kp.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func keyPairItemXML(kp EC2KeyPair) string {
	pub := ""
	if kp.PublicKey != "" {
		pub = fmt.Sprintf("<publicKey>%s</publicKey>", xmlEscape(kp.PublicKey))
	}
	return fmt.Sprintf("<item><keyName>%s</keyName><keyPairId>%s</keyPairId><keyFingerprint>%s</keyFingerprint><keyType>%s</keyType>%s%s</item>",
		kp.KeyName, kp.KeyPairId, kp.KeyFingerprint, kp.KeyType, pub, writeTagSetXML(kp.Tags))
}

func handleDescribeKeyPairs(w http.ResponseWriter, r *http.Request) {
	names := ec2ParamList(r, "KeyName")
	ids := ec2ParamList(r, "KeyPairId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, kp := range ec2KeyPairs.List() {
		if len(names) > 0 && !ec2StrInValues(kp.KeyName, names) {
			continue
		}
		if len(ids) > 0 && !ec2StrInValues(kp.KeyPairId, ids) {
			continue
		}
		if !ec2KeyPairMatchesFilters(kp, filters) {
			continue
		}
		items.WriteString(keyPairItemXML(kp))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeKeyPairsResponse %s><requestId>%s</requestId><keySet>%s</keySet></DescribeKeyPairsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}
