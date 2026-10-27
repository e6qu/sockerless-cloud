package main

import (
	"crypto/ed25519"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CreateKeyPair generates a real RSA or ED25519 key and returns the private
// material; ImportKeyPair stores a caller's OpenSSH public key. The
// fingerprint follows the digest the API reference names for the key's origin
// and type.
type EC2KeyPair struct {
	KeyName        string
	KeyPairId      string
	KeyFingerprint string
	KeyType        string
	PublicKey      string
	Tags           []EC2Tag
}

var ec2KeyPairs sim.Store[EC2KeyPair]

func ec2ColonHex(sum []byte) string {
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

// Amazon EC2 reports the base64 SHA-256 digest for created and imported
// ED25519 keys alike.
func ec2ED25519Fingerprint(pub ssh.PublicKey) string {
	sum := sha256.Sum256(pub.Marshal())
	return base64.StdEncoding.EncodeToString(sum[:])
}

func ec2AuthorizedKey(pub ssh.PublicKey, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + comment
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
	var material, fingerprint string
	var pub ssh.PublicKey
	switch keyType {
	case "rsa":
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		material = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		// A created RSA key is fingerprinted by the SHA-1 digest of its DER private
		// key.
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		sum := sha1.Sum(pkcs8)
		fingerprint = ec2ColonHex(sum[:])
		if pub, err = ssh.NewPublicKey(&key.PublicKey); err != nil {
			ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	case "ed25519":
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		block, err := ssh.MarshalPrivateKey(privateKey, "")
		if err != nil {
			ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		material = string(pem.EncodeToMemory(block))
		if pub, err = ssh.NewPublicKey(publicKey); err != nil {
			ec2ErrorXML(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		fingerprint = ec2ED25519Fingerprint(pub)
	default:
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter keyType is invalid. Valid values are rsa, ed25519.", keyType), http.StatusBadRequest)
		return
	}
	kp := EC2KeyPair{
		KeyName:        name,
		KeyPairId:      ec2ID("key"),
		KeyFingerprint: fingerprint,
		KeyType:        keyType,
		PublicKey:      ec2AuthorizedKey(pub, name),
		Tags:           parseTags(r),
	}
	ec2KeyPairs.Put(name, kp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateKeyPairResponse %s><requestId>%s</requestId><keyName>%s</keyName><keyFingerprint>%s</keyFingerprint><keyMaterial>%s</keyMaterial><keyPairId>%s</keyPairId></CreateKeyPairResponse>`,
		ec2Xmlns(), sim.NewUUID(), kp.KeyName, kp.KeyFingerprint, xmlEscape(material), kp.KeyPairId)
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
	// The SDK base64-encodes the public key; the CLI's fileb:// sends it raw.
	text := material
	if decoded, err := base64.StdEncoding.DecodeString(material); err == nil {
		text = string(decoded)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		ec2ErrorXML(w, "InvalidKey.Format", "Key is not in valid OpenSSH public key format", http.StatusBadRequest)
		return
	}
	var keyType, fingerprint string
	switch pub.Type() {
	case ssh.KeyAlgoRSA:
		// An imported RSA key is fingerprinted by the MD5 digest of its public key,
		// per RFC 4716 section 4.
		keyType = "rsa"
		sum := md5.Sum(pub.Marshal())
		fingerprint = ec2ColonHex(sum[:])
	case ssh.KeyAlgoED25519:
		keyType = "ed25519"
		fingerprint = ec2ED25519Fingerprint(pub)
	default:
		ec2ErrorXML(w, "InvalidKey.Format", "Key is not in valid OpenSSH public key format", http.StatusBadRequest)
		return
	}
	if comment == "" {
		comment = name
	}
	kp := EC2KeyPair{
		KeyName:        name,
		KeyPairId:      ec2ID("key"),
		KeyFingerprint: fingerprint,
		KeyType:        keyType,
		PublicKey:      ec2AuthorizedKey(pub, comment),
		Tags:           parseTags(r),
	}
	ec2KeyPairs.Put(name, kp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportKeyPairResponse %s><requestId>%s</requestId><keyName>%s</keyName><keyFingerprint>%s</keyFingerprint><keyPairId>%s</keyPairId></ImportKeyPairResponse>`,
		ec2Xmlns(), sim.NewUUID(), kp.KeyName, kp.KeyFingerprint, kp.KeyPairId)
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
	fmt.Fprintf(w, `<DeleteKeyPairResponse %s><requestId>%s</requestId><return>true</return></DeleteKeyPairResponse>`, ec2Xmlns(), sim.NewUUID())
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

func keyPairItemXML(kp EC2KeyPair, includePublicKey bool) string {
	pub := ""
	if includePublicKey && kp.PublicKey != "" {
		pub = fmt.Sprintf("<publicKey>%s</publicKey>", xmlEscape(kp.PublicKey))
	}
	return fmt.Sprintf("<item><keyName>%s</keyName><keyPairId>%s</keyPairId><keyFingerprint>%s</keyFingerprint><keyType>%s</keyType>%s%s</item>",
		kp.KeyName, kp.KeyPairId, kp.KeyFingerprint, kp.KeyType, pub, writeTagSetXML(kp.Tags))
}

func handleDescribeKeyPairs(w http.ResponseWriter, r *http.Request) {
	names := ec2ParamList(r, "KeyName")
	ids := ec2ParamList(r, "KeyPairId")
	filters := ec2Filters(r)
	includePublicKey := strings.EqualFold(r.FormValue("IncludePublicKey"), "true")
	for _, name := range names {
		if _, ok := ec2KeyPairs.Get(name); !ok {
			ec2ErrorXML(w, "InvalidKeyPair.NotFound", fmt.Sprintf("The key pair '%s' does not exist", name), http.StatusBadRequest)
			return
		}
	}
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
		items.WriteString(keyPairItemXML(kp, includePublicKey))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeKeyPairsResponse %s><requestId>%s</requestId><keySet>%s</keySet></DescribeKeyPairsResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String())
}
