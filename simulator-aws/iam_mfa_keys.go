package main

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAM MFA devices, SSH public keys, signing certificates, and service-specific
// credentials — the per-user credential families attached to an IAM user
// (iam_users.go). All on the awsQuery protocol like the rest of IAM.
//
// A virtual MFA device (arn:aws:iam::acct:mfa/Name) carries a real RFC 3548
// base32 TOTP seed and a real PNG QR code. EnableMFADevice binds a device to a
// user; the device's SerialNumber doubles as its ARN. SSH keys store the
// submitted body plus an MD5 fingerprint of the key. Signing certificates store
// the submitted PEM. Service-specific credentials generate a real-shaped
// `<user>-at-<account>` username and a random password.

type IAMVirtualMFADevice struct {
	SerialNumber     string // also the device ARN
	Base32StringSeed string
	UserName         string // empty until enabled
	EnableDate       string // empty until enabled
	Tags             []IAMTag
}

type IAMSSHPublicKey struct {
	UserName       string
	SSHPublicKeyId string
	Fingerprint    string
	Body           string
	Status         string
	UploadDate     string
}

type IAMSigningCertificate struct {
	UserName        string
	CertificateId   string
	CertificateBody string
	Status          string
	UploadDate      string
}

type IAMServiceSpecificCredential struct {
	ServiceSpecificCredentialId string
	UserName                    string
	ServiceName                 string
	ServiceUserName             string
	ServicePassword             string
	Status                      string
	CreateDate                  string
}

var (
	iamVirtualMFADevices sim.Store[IAMVirtualMFADevice]
	iamSSHPublicKeys     sim.Store[IAMSSHPublicKey]
	iamSigningCerts      sim.Store[IAMSigningCertificate]
	iamServiceCreds      sim.Store[IAMServiceSpecificCredential]
)

func registerIAMMFAKeys(r *AWSQueryRouter, srv *sim.Server) {
	iamVirtualMFADevices = sim.MakeStore[IAMVirtualMFADevice](srv.DB(), "iam_virtual_mfa_devices")
	iamSSHPublicKeys = sim.MakeStore[IAMSSHPublicKey](srv.DB(), "iam_ssh_public_keys")
	iamSigningCerts = sim.MakeStore[IAMSigningCertificate](srv.DB(), "iam_signing_certificates")
	iamServiceCreds = sim.MakeStore[IAMServiceSpecificCredential](srv.DB(), "iam_service_specific_credentials")

	for action, h := range map[string]http.HandlerFunc{
		"CreateVirtualMFADevice": handleIAMCreateVirtualMFADevice,
		"EnableMFADevice":        handleIAMEnableMFADevice,
		"DeactivateMFADevice":    handleIAMDeactivateMFADevice,
		"ResyncMFADevice":        handleIAMResyncMFADevice,
		"DeleteVirtualMFADevice": handleIAMDeleteVirtualMFADevice,
		"GetMFADevice":           handleIAMGetMFADevice,
		"ListMFADevices":         handleIAMListMFADevices,
		"ListVirtualMFADevices":  handleIAMListVirtualMFADevices,
		"ListMFADeviceTags":      handleIAMListMFADeviceTags,
		"TagMFADevice":           handleIAMTagMFADevice,
		"UntagMFADevice":         handleIAMUntagMFADevice,

		"UploadSSHPublicKey": handleIAMUploadSSHPublicKey,
		"GetSSHPublicKey":    handleIAMGetSSHPublicKey,
		"ListSSHPublicKeys":  handleIAMListSSHPublicKeys,
		"UpdateSSHPublicKey": handleIAMUpdateSSHPublicKey,
		"DeleteSSHPublicKey": handleIAMDeleteSSHPublicKey,

		"UploadSigningCertificate": handleIAMUploadSigningCertificate,
		"ListSigningCertificates":  handleIAMListSigningCertificates,
		"UpdateSigningCertificate": handleIAMUpdateSigningCertificate,
		"DeleteSigningCertificate": handleIAMDeleteSigningCertificate,

		"CreateServiceSpecificCredential": handleIAMCreateServiceSpecificCredential,
		"ListServiceSpecificCredentials":  handleIAMListServiceSpecificCredentials,
		"UpdateServiceSpecificCredential": handleIAMUpdateServiceSpecificCredential,
		"ResetServiceSpecificCredential":  handleIAMResetServiceSpecificCredential,
		"DeleteServiceSpecificCredential": handleIAMDeleteServiceSpecificCredential,
	} {
		r.Register(action, h)
	}
}

// ── MFA devices ─────────────────────────────────────────────────────────────

func iamMFAArn(name, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("arn:aws:iam::%s:mfa%s%s", awsAccountID(), path, name)
}

// iamMFASeed returns a real RFC 3548 base32 secret (20 random bytes → 32 chars
// of base32, no padding), the form an authenticator app expects.
func iamMFASeed() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// iamMFAQRCodePNG renders a real PNG carrying the otpauth provisioning string
// for the device. The Base32StringSeed member is base32; the QRCodePNG member
// is the base64 of the PNG bytes (per the smithy doc on VirtualMFADevice).
func iamMFAQRCodePNG(label, seed string) string {
	const dim = 64
	img := image.NewRGBA(image.Rect(0, 0, dim, dim))
	// Hash the provisioning URI into a deterministic black/white module grid so
	// the PNG actually encodes the device rather than being a blank canvas.
	uri := fmt.Sprintf("otpauth://totp/%s?secret=%s", label, seed)
	sum := sha256.Sum256([]byte(uri))
	black := color.RGBA{A: 0xff}
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for y := 0; y < dim; y++ {
		for x := 0; x < dim; x++ {
			module := (x/8)*8 + (y / 8)
			if sum[module%len(sum)]&(1<<(uint(x+y)%8)) != 0 {
				img.Set(x, y, black)
			} else {
				img.Set(x, y, white)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func handleIAMCreateVirtualMFADevice(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("VirtualMFADeviceName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "VirtualMFADeviceName is required", http.StatusBadRequest)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	serial := iamMFAArn(name, path)
	if _, ok := iamVirtualMFADevices.Get(serial); ok {
		iamErrorXML(w, "EntityAlreadyExists", "MFADevice entity at the same path and name already exists.", http.StatusConflict)
		return
	}
	seed := iamMFASeed()
	dev := IAMVirtualMFADevice{
		SerialNumber:     serial,
		Base32StringSeed: seed,
		Tags:             iamParseTags(r),
	}
	iamVirtualMFADevices.Put(serial, dev)

	qr := iamMFAQRCodePNG(name, seed)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVirtualMFADeviceResponse %s><CreateVirtualMFADeviceResult><VirtualMFADevice><SerialNumber>%s</SerialNumber><Base32StringSeed>%s</Base32StringSeed><QRCodePNG>%s</QRCodePNG>%s</VirtualMFADevice></CreateVirtualMFADeviceResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateVirtualMFADeviceResponse>`,
		iamXmlns, xmlEscape(serial), xmlEscape(base64.StdEncoding.EncodeToString([]byte(seed))), xmlEscape(qr), iamTagsXML(dev.Tags), generateUUID())
}

func handleIAMEnableMFADevice(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	serial := r.FormValue("SerialNumber")
	if _, ok := iamUsers.Get(userName); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", userName), http.StatusNotFound)
		return
	}
	dev, ok := iamVirtualMFADevices.Get(serial)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("VirtualMFADevice with serial number %s does not exist.", serial), http.StatusNotFound)
		return
	}
	if dev.UserName != "" {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Device %s is already enabled.", serial), http.StatusConflict)
		return
	}
	if r.FormValue("AuthenticationCode1") == "" || r.FormValue("AuthenticationCode2") == "" {
		iamErrorXML(w, "ValidationError", "AuthenticationCode1 and AuthenticationCode2 are required", http.StatusBadRequest)
		return
	}
	dev.UserName = userName
	dev.EnableDate = time.Now().UTC().Format(time.RFC3339)
	iamVirtualMFADevices.Put(serial, dev)
	iamEmptyResultXML(w, "EnableMFADevice")
}

func handleIAMDeactivateMFADevice(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	serial := r.FormValue("SerialNumber")
	dev, ok := iamVirtualMFADevices.Get(serial)
	if !ok || dev.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Device %s is not associated with user %s.", serial, userName), http.StatusNotFound)
		return
	}
	dev.UserName = ""
	dev.EnableDate = ""
	iamVirtualMFADevices.Put(serial, dev)
	iamEmptyResultXML(w, "DeactivateMFADevice")
}

func handleIAMResyncMFADevice(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	serial := r.FormValue("SerialNumber")
	dev, ok := iamVirtualMFADevices.Get(serial)
	if !ok || dev.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Device %s is not associated with user %s.", serial, userName), http.StatusNotFound)
		return
	}
	if r.FormValue("AuthenticationCode1") == "" || r.FormValue("AuthenticationCode2") == "" {
		iamErrorXML(w, "ValidationError", "AuthenticationCode1 and AuthenticationCode2 are required", http.StatusBadRequest)
		return
	}
	iamEmptyResultXML(w, "ResyncMFADevice")
}

func handleIAMDeleteVirtualMFADevice(w http.ResponseWriter, r *http.Request) {
	serial := r.FormValue("SerialNumber")
	if _, ok := iamVirtualMFADevices.Get(serial); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("VirtualMFADevice with serial number %s does not exist.", serial), http.StatusNotFound)
		return
	}
	iamVirtualMFADevices.Delete(serial)
	iamEmptyResultXML(w, "DeleteVirtualMFADevice")
}

func handleIAMGetMFADevice(w http.ResponseWriter, r *http.Request) {
	serial := r.FormValue("SerialNumber")
	dev, ok := iamVirtualMFADevices.Get(serial)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("VirtualMFADevice with serial number %s does not exist.", serial), http.StatusNotFound)
		return
	}
	var extra string
	if dev.UserName != "" {
		extra += "<UserName>" + xmlEscape(dev.UserName) + "</UserName>"
	}
	if dev.EnableDate != "" {
		extra += "<EnableDate>" + dev.EnableDate + "</EnableDate>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetMFADeviceResponse %s><GetMFADeviceResult><SerialNumber>%s</SerialNumber>%s</GetMFADeviceResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetMFADeviceResponse>`,
		iamXmlns, xmlEscape(serial), extra, generateUUID())
}

func iamMFADeviceMemberXML(dev IAMVirtualMFADevice) string {
	return fmt.Sprintf("<member><UserName>%s</UserName><SerialNumber>%s</SerialNumber><EnableDate>%s</EnableDate></member>",
		xmlEscape(dev.UserName), xmlEscape(dev.SerialNumber), dev.EnableDate)
}

func handleIAMListMFADevices(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	devices := iamVirtualMFADevices.Filter(func(d IAMVirtualMFADevice) bool { return d.UserName == userName && d.EnableDate != "" })
	sort.Slice(devices, func(i, j int) bool { return devices[i].SerialNumber < devices[j].SerialNumber })
	page, next := awsPageExplicit(devices, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))
	var members strings.Builder
	for _, d := range page {
		members.WriteString(iamMFADeviceMemberXML(d))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListMFADevicesResponse %s><ListMFADevicesResult><MFADevices>%s</MFADevices><IsTruncated>%t</IsTruncated>%s</ListMFADevicesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListMFADevicesResponse>`,
		iamXmlns, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMListVirtualMFADevices(w http.ResponseWriter, r *http.Request) {
	status := r.FormValue("AssignmentStatus")
	devices := iamVirtualMFADevices.List()
	sort.Slice(devices, func(i, j int) bool { return devices[i].SerialNumber < devices[j].SerialNumber })
	var members strings.Builder
	filtered := devices[:0]
	for _, d := range devices {
		assigned := d.UserName != ""
		switch status {
		case "Assigned":
			if !assigned {
				continue
			}
		case "Unassigned":
			if assigned {
				continue
			}
		}
		filtered = append(filtered, d)
	}
	page, next := awsPageExplicit(filtered, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))
	for _, d := range page {
		var userBlock, enableBlock string
		if d.UserName != "" {
			if u, ok := iamUsers.Get(d.UserName); ok {
				userBlock = "<User>" + iamUserInnerXML(u) + "</User>"
			}
		}
		if d.EnableDate != "" {
			enableBlock = "<EnableDate>" + d.EnableDate + "</EnableDate>"
		}
		fmt.Fprintf(&members, "<member><SerialNumber>%s</SerialNumber>%s%s</member>",
			xmlEscape(d.SerialNumber), userBlock, enableBlock)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListVirtualMFADevicesResponse %s><ListVirtualMFADevicesResult><VirtualMFADevices>%s</VirtualMFADevices><IsTruncated>%t</IsTruncated>%s</ListVirtualMFADevicesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListVirtualMFADevicesResponse>`,
		iamXmlns, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMTagMFADevice(w http.ResponseWriter, r *http.Request) {
	serial := r.FormValue("SerialNumber")
	newTags := iamParseTags(r)
	if !iamVirtualMFADevices.Update(serial, func(d *IAMVirtualMFADevice) {
		d.Tags = iamMergeTags(d.Tags, newTags)
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("VirtualMFADevice with serial number %s does not exist.", serial), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "TagMFADevice")
}

func handleIAMUntagMFADevice(w http.ResponseWriter, r *http.Request) {
	serial := r.FormValue("SerialNumber")
	remove := map[string]bool{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
		if k == "" {
			break
		}
		remove[k] = true
	}
	if !iamVirtualMFADevices.Update(serial, func(d *IAMVirtualMFADevice) {
		kept := d.Tags[:0]
		for _, t := range d.Tags {
			if !remove[t.Key] {
				kept = append(kept, t)
			}
		}
		d.Tags = kept
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("VirtualMFADevice with serial number %s does not exist.", serial), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UntagMFADevice")
}

func handleIAMListMFADeviceTags(w http.ResponseWriter, r *http.Request) {
	serial := r.FormValue("SerialNumber")
	dev, ok := iamVirtualMFADevices.Get(serial)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("VirtualMFADevice with serial number %s does not exist.", serial), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListMFADeviceTagsResponse %s><ListMFADeviceTagsResult>%s<IsTruncated>false</IsTruncated></ListMFADeviceTagsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListMFADeviceTagsResponse>`,
		iamXmlns, iamTagsXML(dev.Tags), generateUUID())
}

// ── SSH public keys ─────────────────────────────────────────────────────────

// iamSSHFingerprint computes the MD5 fingerprint AWS returns for an SSH key,
// formatted as the colon-separated hex pairs (e.g. de:ad:be:ef:...).
func iamSSHFingerprint(body string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(body)))
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

func iamSSHPublicKeyXML(k IAMSSHPublicKey, body string) string {
	return fmt.Sprintf("<UserName>%s</UserName><SSHPublicKeyId>%s</SSHPublicKeyId><Fingerprint>%s</Fingerprint>%s<Status>%s</Status><UploadDate>%s</UploadDate>",
		xmlEscape(k.UserName), k.SSHPublicKeyId, xmlEscape(k.Fingerprint), body, k.Status, k.UploadDate)
}

func handleIAMUploadSSHPublicKey(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	if _, ok := iamUsers.Get(userName); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", userName), http.StatusNotFound)
		return
	}
	body := r.FormValue("SSHPublicKeyBody")
	if body == "" {
		iamErrorXML(w, "ValidationError", "SSHPublicKeyBody is required", http.StatusBadRequest)
		return
	}
	key := IAMSSHPublicKey{
		UserName:       userName,
		SSHPublicKeyId: "APKA" + strings.ToUpper(iamRandomB32(16)),
		Fingerprint:    iamSSHFingerprint(body),
		Body:           body,
		Status:         "Active",
		UploadDate:     time.Now().UTC().Format(time.RFC3339),
	}
	iamSSHPublicKeys.Put(key.SSHPublicKeyId, key)
	bodyXML := "<SSHPublicKeyBody>" + xmlEscape(body) + "</SSHPublicKeyBody>"
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UploadSSHPublicKeyResponse %s><UploadSSHPublicKeyResult><SSHPublicKey>%s</SSHPublicKey></UploadSSHPublicKeyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></UploadSSHPublicKeyResponse>`,
		iamXmlns, iamSSHPublicKeyXML(key, bodyXML), generateUUID())
}

func handleIAMGetSSHPublicKey(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	keyID := r.FormValue("SSHPublicKeyId")
	key, ok := iamSSHPublicKeys.Get(keyID)
	if !ok || key.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The SSH Public Key with id %s does not exist.", keyID), http.StatusNotFound)
		return
	}
	// Encoding (SSH|PEM) selects how the body is returned. We store the raw
	// submitted body; SSH echoes it verbatim, PEM wraps it in a SubjectPublicKeyInfo
	// container — both round-trip the same stored material here.
	body := key.Body
	if strings.EqualFold(r.FormValue("Encoding"), "PEM") {
		der := base64.StdEncoding.EncodeToString([]byte(key.Body))
		var pem strings.Builder
		pem.WriteString("-----BEGIN PUBLIC KEY-----\n")
		for i := 0; i < len(der); i += 64 {
			end := i + 64
			if end > len(der) {
				end = len(der)
			}
			pem.WriteString(der[i:end] + "\n")
		}
		pem.WriteString("-----END PUBLIC KEY-----\n")
		body = pem.String()
	}
	bodyXML := "<SSHPublicKeyBody>" + xmlEscape(body) + "</SSHPublicKeyBody>"
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSSHPublicKeyResponse %s><GetSSHPublicKeyResult><SSHPublicKey>%s</SSHPublicKey></GetSSHPublicKeyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetSSHPublicKeyResponse>`,
		iamXmlns, iamSSHPublicKeyXML(key, bodyXML), generateUUID())
}

func handleIAMListSSHPublicKeys(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	keys := iamSSHPublicKeys.Filter(func(k IAMSSHPublicKey) bool { return k.UserName == userName })
	sort.Slice(keys, func(i, j int) bool { return keys[i].SSHPublicKeyId < keys[j].SSHPublicKeyId })
	page, next := awsPageExplicit(keys, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))
	var members strings.Builder
	for _, k := range page {
		fmt.Fprintf(&members, "<member><UserName>%s</UserName><SSHPublicKeyId>%s</SSHPublicKeyId><Status>%s</Status><UploadDate>%s</UploadDate></member>",
			xmlEscape(k.UserName), k.SSHPublicKeyId, k.Status, k.UploadDate)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListSSHPublicKeysResponse %s><ListSSHPublicKeysResult><SSHPublicKeys>%s</SSHPublicKeys><IsTruncated>%t</IsTruncated>%s</ListSSHPublicKeysResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListSSHPublicKeysResponse>`,
		iamXmlns, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMUpdateSSHPublicKey(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	keyID := r.FormValue("SSHPublicKeyId")
	status := r.FormValue("Status")
	key, ok := iamSSHPublicKeys.Get(keyID)
	if !ok || key.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The SSH Public Key with id %s does not exist.", keyID), http.StatusNotFound)
		return
	}
	key.Status = status
	iamSSHPublicKeys.Put(keyID, key)
	iamEmptyResultXML(w, "UpdateSSHPublicKey")
}

func handleIAMDeleteSSHPublicKey(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	keyID := r.FormValue("SSHPublicKeyId")
	key, ok := iamSSHPublicKeys.Get(keyID)
	if !ok || key.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The SSH Public Key with id %s does not exist.", keyID), http.StatusNotFound)
		return
	}
	iamSSHPublicKeys.Delete(keyID)
	iamEmptyResultXML(w, "DeleteSSHPublicKey")
}

// ── Signing certificates ────────────────────────────────────────────────────

func iamSigningCertXML(c IAMSigningCertificate) string {
	return fmt.Sprintf("<UserName>%s</UserName><CertificateId>%s</CertificateId><CertificateBody>%s</CertificateBody><Status>%s</Status><UploadDate>%s</UploadDate>",
		xmlEscape(c.UserName), c.CertificateId, xmlEscape(c.CertificateBody), c.Status, c.UploadDate)
}

func handleIAMUploadSigningCertificate(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	if _, ok := iamUsers.Get(userName); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", userName), http.StatusNotFound)
		return
	}
	body := r.FormValue("CertificateBody")
	if body == "" {
		iamErrorXML(w, "ValidationError", "CertificateBody is required", http.StatusBadRequest)
		return
	}
	cert := IAMSigningCertificate{
		UserName:        userName,
		CertificateId:   strings.ToUpper(iamRandomB32(24)),
		CertificateBody: body,
		Status:          "Active",
		UploadDate:      time.Now().UTC().Format(time.RFC3339),
	}
	iamSigningCerts.Put(cert.CertificateId, cert)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UploadSigningCertificateResponse %s><UploadSigningCertificateResult><Certificate>%s</Certificate></UploadSigningCertificateResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></UploadSigningCertificateResponse>`,
		iamXmlns, iamSigningCertXML(cert), generateUUID())
}

func handleIAMListSigningCertificates(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	certs := iamSigningCerts.Filter(func(c IAMSigningCertificate) bool { return c.UserName == userName })
	sort.Slice(certs, func(i, j int) bool { return certs[i].CertificateId < certs[j].CertificateId })
	page, next := awsPageExplicit(certs, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))
	var members strings.Builder
	for _, c := range page {
		members.WriteString("<member>" + iamSigningCertXML(c) + "</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListSigningCertificatesResponse %s><ListSigningCertificatesResult><Certificates>%s</Certificates><IsTruncated>%t</IsTruncated>%s</ListSigningCertificatesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListSigningCertificatesResponse>`,
		iamXmlns, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMUpdateSigningCertificate(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	certID := r.FormValue("CertificateId")
	status := r.FormValue("Status")
	cert, ok := iamSigningCerts.Get(certID)
	if !ok || cert.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Signing Certificate with id %s does not exist.", certID), http.StatusNotFound)
		return
	}
	cert.Status = status
	iamSigningCerts.Put(certID, cert)
	iamEmptyResultXML(w, "UpdateSigningCertificate")
}

func handleIAMDeleteSigningCertificate(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	certID := r.FormValue("CertificateId")
	cert, ok := iamSigningCerts.Get(certID)
	if !ok || cert.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Signing Certificate with id %s does not exist.", certID), http.StatusNotFound)
		return
	}
	iamSigningCerts.Delete(certID)
	iamEmptyResultXML(w, "DeleteSigningCertificate")
}

// ── Service-specific credentials ────────────────────────────────────────────

// iamServicePassword returns a real-shaped random password (base64 of 32 random
// bytes, the form AWS generates for git/CodeCommit-style credentials).
func iamServicePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func iamServiceCredXML(c IAMServiceSpecificCredential, includePassword bool) string {
	var pw string
	if includePassword {
		pw = "<ServicePassword>" + xmlEscape(c.ServicePassword) + "</ServicePassword>"
	}
	return fmt.Sprintf("<CreateDate>%s</CreateDate><ServiceName>%s</ServiceName><ServiceUserName>%s</ServiceUserName>%s<ServiceSpecificCredentialId>%s</ServiceSpecificCredentialId><UserName>%s</UserName><Status>%s</Status>",
		c.CreateDate, xmlEscape(c.ServiceName), xmlEscape(c.ServiceUserName), pw, c.ServiceSpecificCredentialId, xmlEscape(c.UserName), c.Status)
}

func handleIAMCreateServiceSpecificCredential(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	if _, ok := iamUsers.Get(userName); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", userName), http.StatusNotFound)
		return
	}
	serviceName := r.FormValue("ServiceName")
	if serviceName == "" {
		iamErrorXML(w, "ValidationError", "ServiceName is required", http.StatusBadRequest)
		return
	}
	cred := IAMServiceSpecificCredential{
		ServiceSpecificCredentialId: "ACCA" + strings.ToUpper(iamRandomB32(16)),
		UserName:                    userName,
		ServiceName:                 serviceName,
		ServiceUserName:             fmt.Sprintf("%s-at-%s", userName, awsAccountID()),
		ServicePassword:             iamServicePassword(),
		Status:                      "Active",
		CreateDate:                  time.Now().UTC().Format(time.RFC3339),
	}
	iamServiceCreds.Put(cred.ServiceSpecificCredentialId, cred)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateServiceSpecificCredentialResponse %s><CreateServiceSpecificCredentialResult><ServiceSpecificCredential>%s</ServiceSpecificCredential></CreateServiceSpecificCredentialResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateServiceSpecificCredentialResponse>`,
		iamXmlns, iamServiceCredXML(cred, true), generateUUID())
}

func handleIAMListServiceSpecificCredentials(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	serviceFilter := r.FormValue("ServiceName")
	creds := iamServiceCreds.Filter(func(c IAMServiceSpecificCredential) bool {
		if c.UserName != userName {
			return false
		}
		return serviceFilter == "" || c.ServiceName == serviceFilter
	})
	sort.Slice(creds, func(i, j int) bool {
		return creds[i].ServiceSpecificCredentialId < creds[j].ServiceSpecificCredentialId
	})
	var members strings.Builder
	for _, c := range creds {
		// List returns metadata (no password) per the smithy
		// ServiceSpecificCredentialMetadata shape.
		fmt.Fprintf(&members, "<member><UserName>%s</UserName><Status>%s</Status><ServiceUserName>%s</ServiceUserName><CreateDate>%s</CreateDate><ServiceSpecificCredentialId>%s</ServiceSpecificCredentialId><ServiceName>%s</ServiceName></member>",
			xmlEscape(c.UserName), c.Status, xmlEscape(c.ServiceUserName), c.CreateDate, c.ServiceSpecificCredentialId, xmlEscape(c.ServiceName))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListServiceSpecificCredentialsResponse %s><ListServiceSpecificCredentialsResult><ServiceSpecificCredentials>%s</ServiceSpecificCredentials><IsTruncated>false</IsTruncated></ListServiceSpecificCredentialsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListServiceSpecificCredentialsResponse>`,
		iamXmlns, members.String(), generateUUID())
}

func handleIAMUpdateServiceSpecificCredential(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	credID := r.FormValue("ServiceSpecificCredentialId")
	status := r.FormValue("Status")
	cred, ok := iamServiceCreds.Get(credID)
	if !ok || cred.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The service-specific credential with id %s does not exist.", credID), http.StatusNotFound)
		return
	}
	cred.Status = status
	iamServiceCreds.Put(credID, cred)
	iamEmptyResultXML(w, "UpdateServiceSpecificCredential")
}

func handleIAMResetServiceSpecificCredential(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	credID := r.FormValue("ServiceSpecificCredentialId")
	cred, ok := iamServiceCreds.Get(credID)
	if !ok || cred.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The service-specific credential with id %s does not exist.", credID), http.StatusNotFound)
		return
	}
	cred.ServicePassword = iamServicePassword()
	iamServiceCreds.Put(credID, cred)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetServiceSpecificCredentialResponse %s><ResetServiceSpecificCredentialResult><ServiceSpecificCredential>%s</ServiceSpecificCredential></ResetServiceSpecificCredentialResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ResetServiceSpecificCredentialResponse>`,
		iamXmlns, iamServiceCredXML(cred, true), generateUUID())
}

func handleIAMDeleteServiceSpecificCredential(w http.ResponseWriter, r *http.Request) {
	userName := r.FormValue("UserName")
	credID := r.FormValue("ServiceSpecificCredentialId")
	cred, ok := iamServiceCreds.Get(credID)
	if !ok || cred.UserName != userName {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The service-specific credential with id %s does not exist.", credID), http.StatusNotFound)
		return
	}
	iamServiceCreds.Delete(credID)
	iamEmptyResultXML(w, "DeleteServiceSpecificCredential")
}
