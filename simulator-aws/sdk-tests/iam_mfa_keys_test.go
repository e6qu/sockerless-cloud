package aws_sdk_test

import (
	"encoding/base32"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iamMFAUser creates a throwaway IAM user the credential families attach to,
// registering a tolerant cleanup.
func iamMFAUser(t *testing.T, c *iam.Client, name string) {
	t.Helper()
	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(name)}) })
}

// TestIAM_VirtualMFADevice exercises the MFA-device family: CreateVirtualMFADevice
// (real base32 seed + PNG QR), EnableMFADevice, GetMFADevice, ListMFADevices,
// ListVirtualMFADevices (Assigned/Unassigned filter), the tag ops, ResyncMFADevice,
// DeactivateMFADevice, and DeleteVirtualMFADevice.
func TestIAM_VirtualMFADevice(t *testing.T) {
	c := iamClient()
	user := "mfa-user"
	iamMFAUser(t, c, user)

	created, err := c.CreateVirtualMFADevice(ctx, &iam.CreateVirtualMFADeviceInput{
		VirtualMFADeviceName: aws.String("dev1"),
		Tags:                 []types.Tag{{Key: aws.String("team"), Value: aws.String("sec")}},
	})
	require.NoError(t, err)
	serial := aws.ToString(created.VirtualMFADevice.SerialNumber)
	assert.Contains(t, serial, ":mfa/dev1")
	t.Cleanup(func() {
		c.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(serial)})
	})

	// Base32StringSeed is a blob whose content (after the SDK's base64 transport
	// decode) is the base32-encoded TOTP secret; QRCodePNG is the PNG bytes. Both
	// must decode to real material (no fakes).
	_, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(string(created.VirtualMFADevice.Base32StringSeed))
	require.NoError(t, err, "Base32StringSeed must be valid base32")
	require.NotEmpty(t, created.VirtualMFADevice.QRCodePNG)
	assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, created.VirtualMFADevice.QRCodePNG[:4], "QRCodePNG must be a real PNG")

	// Before enable, it's an Unassigned virtual MFA device.
	un, err := c.ListVirtualMFADevices(ctx, &iam.ListVirtualMFADevicesInput{AssignmentStatus: types.AssignmentStatusTypeUnassigned})
	require.NoError(t, err)
	assert.True(t, containsSerial(un.VirtualMFADevices, serial))

	_, err = c.EnableMFADevice(ctx, &iam.EnableMFADeviceInput{
		UserName:            aws.String(user),
		SerialNumber:        aws.String(serial),
		AuthenticationCode1: aws.String("123456"),
		AuthenticationCode2: aws.String("234567"),
	})
	require.NoError(t, err)

	got, err := c.GetMFADevice(ctx, &iam.GetMFADeviceInput{SerialNumber: aws.String(serial)})
	require.NoError(t, err)
	assert.Equal(t, serial, aws.ToString(got.SerialNumber))

	devs, err := c.ListMFADevices(ctx, &iam.ListMFADevicesInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, devs.MFADevices, 1)
	assert.Equal(t, serial, aws.ToString(devs.MFADevices[0].SerialNumber))
	assert.Equal(t, user, aws.ToString(devs.MFADevices[0].UserName))

	asn, err := c.ListVirtualMFADevices(ctx, &iam.ListVirtualMFADevicesInput{AssignmentStatus: types.AssignmentStatusTypeAssigned})
	require.NoError(t, err)
	assert.True(t, containsSerial(asn.VirtualMFADevices, serial))

	_, err = c.TagMFADevice(ctx, &iam.TagMFADeviceInput{
		SerialNumber: aws.String(serial),
		Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	tags, err := c.ListMFADeviceTags(ctx, &iam.ListMFADeviceTagsInput{SerialNumber: aws.String(serial)})
	require.NoError(t, err)
	assert.True(t, hasTag(tags.Tags, "env", "test"))
	assert.True(t, hasTag(tags.Tags, "team", "sec"))

	_, err = c.UntagMFADevice(ctx, &iam.UntagMFADeviceInput{
		SerialNumber: aws.String(serial),
		TagKeys:      []string{"env"},
	})
	require.NoError(t, err)
	tags2, err := c.ListMFADeviceTags(ctx, &iam.ListMFADeviceTagsInput{SerialNumber: aws.String(serial)})
	require.NoError(t, err)
	assert.False(t, hasTag(tags2.Tags, "env", "test"))

	_, err = c.ResyncMFADevice(ctx, &iam.ResyncMFADeviceInput{
		UserName:            aws.String(user),
		SerialNumber:        aws.String(serial),
		AuthenticationCode1: aws.String("345678"),
		AuthenticationCode2: aws.String("456789"),
	})
	require.NoError(t, err)

	_, err = c.DeactivateMFADevice(ctx, &iam.DeactivateMFADeviceInput{
		UserName:     aws.String(user),
		SerialNumber: aws.String(serial),
	})
	require.NoError(t, err)
	devs2, err := c.ListMFADevices(ctx, &iam.ListMFADevicesInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.Empty(t, devs2.MFADevices)

	_, err = c.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(serial)})
	require.NoError(t, err)
}

func containsSerial(list []types.VirtualMFADevice, serial string) bool {
	for _, d := range list {
		if aws.ToString(d.SerialNumber) == serial {
			return true
		}
	}
	return false
}

func hasTag(tags []types.Tag, k, v string) bool {
	for _, t := range tags {
		if aws.ToString(t.Key) == k && aws.ToString(t.Value) == v {
			return true
		}
	}
	return false
}

// TestIAM_SSHPublicKey exercises UploadSSHPublicKey (id + MD5 fingerprint),
// GetSSHPublicKey (SSH + PEM encoding), ListSSHPublicKeys, UpdateSSHPublicKey,
// and DeleteSSHPublicKey.
func TestIAM_SSHPublicKey(t *testing.T) {
	c := iamClient()
	user := "ssh-user"
	iamMFAUser(t, c, user)

	const body = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDexample sockerless@test"
	up, err := c.UploadSSHPublicKey(ctx, &iam.UploadSSHPublicKeyInput{
		UserName:         aws.String(user),
		SSHPublicKeyBody: aws.String(body),
	})
	require.NoError(t, err)
	keyID := aws.ToString(up.SSHPublicKey.SSHPublicKeyId)
	assert.Contains(t, keyID, "APKA")
	assert.NotEmpty(t, up.SSHPublicKey.Fingerprint)
	assert.Equal(t, "Active", string(up.SSHPublicKey.Status))

	gotSSH, err := c.GetSSHPublicKey(ctx, &iam.GetSSHPublicKeyInput{
		UserName:       aws.String(user),
		SSHPublicKeyId: aws.String(keyID),
		Encoding:       types.EncodingTypeSsh,
	})
	require.NoError(t, err)
	assert.Equal(t, body, aws.ToString(gotSSH.SSHPublicKey.SSHPublicKeyBody))

	gotPEM, err := c.GetSSHPublicKey(ctx, &iam.GetSSHPublicKeyInput{
		UserName:       aws.String(user),
		SSHPublicKeyId: aws.String(keyID),
		Encoding:       types.EncodingTypePem,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(gotPEM.SSHPublicKey.SSHPublicKeyBody), "BEGIN PUBLIC KEY")

	list, err := c.ListSSHPublicKeys(ctx, &iam.ListSSHPublicKeysInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, list.SSHPublicKeys, 1)
	assert.Equal(t, keyID, aws.ToString(list.SSHPublicKeys[0].SSHPublicKeyId))

	_, err = c.UpdateSSHPublicKey(ctx, &iam.UpdateSSHPublicKeyInput{
		UserName:       aws.String(user),
		SSHPublicKeyId: aws.String(keyID),
		Status:         types.StatusTypeInactive,
	})
	require.NoError(t, err)
	after, err := c.GetSSHPublicKey(ctx, &iam.GetSSHPublicKeyInput{
		UserName: aws.String(user), SSHPublicKeyId: aws.String(keyID), Encoding: types.EncodingTypeSsh,
	})
	require.NoError(t, err)
	assert.Equal(t, "Inactive", string(after.SSHPublicKey.Status))

	_, err = c.DeleteSSHPublicKey(ctx, &iam.DeleteSSHPublicKeyInput{
		UserName: aws.String(user), SSHPublicKeyId: aws.String(keyID),
	})
	require.NoError(t, err)
}

// TestIAM_SigningCertificate exercises UploadSigningCertificate (CertificateId),
// ListSigningCertificates, UpdateSigningCertificate, DeleteSigningCertificate.
func TestIAM_SigningCertificate(t *testing.T) {
	c := iamClient()
	user := "cert-user"
	iamMFAUser(t, c, user)

	const pem = "-----BEGIN CERTIFICATE-----\nMIIBexampleSimSigningCert==\n-----END CERTIFICATE-----"
	up, err := c.UploadSigningCertificate(ctx, &iam.UploadSigningCertificateInput{
		UserName:        aws.String(user),
		CertificateBody: aws.String(pem),
	})
	require.NoError(t, err)
	certID := aws.ToString(up.Certificate.CertificateId)
	require.NotEmpty(t, certID)
	assert.Equal(t, pem, aws.ToString(up.Certificate.CertificateBody))
	assert.Equal(t, "Active", string(up.Certificate.Status))

	list, err := c.ListSigningCertificates(ctx, &iam.ListSigningCertificatesInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, list.Certificates, 1)
	assert.Equal(t, certID, aws.ToString(list.Certificates[0].CertificateId))

	_, err = c.UpdateSigningCertificate(ctx, &iam.UpdateSigningCertificateInput{
		UserName:      aws.String(user),
		CertificateId: aws.String(certID),
		Status:        types.StatusTypeInactive,
	})
	require.NoError(t, err)
	list2, err := c.ListSigningCertificates(ctx, &iam.ListSigningCertificatesInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, list2.Certificates, 1)
	assert.Equal(t, "Inactive", string(list2.Certificates[0].Status))

	_, err = c.DeleteSigningCertificate(ctx, &iam.DeleteSigningCertificateInput{
		UserName: aws.String(user), CertificateId: aws.String(certID),
	})
	require.NoError(t, err)
}

// TestIAM_ServiceSpecificCredential exercises CreateServiceSpecificCredential
// (username + password + id), ListServiceSpecificCredentials, ResetServiceSpecificCredential
// (new password), UpdateServiceSpecificCredential, DeleteServiceSpecificCredential.
func TestIAM_ServiceSpecificCredential(t *testing.T) {
	c := iamClient()
	user := "svccred-user"
	iamMFAUser(t, c, user)

	created, err := c.CreateServiceSpecificCredential(ctx, &iam.CreateServiceSpecificCredentialInput{
		UserName:    aws.String(user),
		ServiceName: aws.String("codecommit.amazonaws.com"),
	})
	require.NoError(t, err)
	cred := created.ServiceSpecificCredential
	credID := aws.ToString(cred.ServiceSpecificCredentialId)
	require.NotEmpty(t, credID)
	assert.Contains(t, aws.ToString(cred.ServiceUserName), user+"-at-")
	require.NotEmpty(t, aws.ToString(cred.ServicePassword))
	assert.Equal(t, "Active", string(cred.Status))
	origPassword := aws.ToString(cred.ServicePassword)

	list, err := c.ListServiceSpecificCredentials(ctx, &iam.ListServiceSpecificCredentialsInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, list.ServiceSpecificCredentials, 1)
	assert.Equal(t, credID, aws.ToString(list.ServiceSpecificCredentials[0].ServiceSpecificCredentialId))

	reset, err := c.ResetServiceSpecificCredential(ctx, &iam.ResetServiceSpecificCredentialInput{
		UserName:                    aws.String(user),
		ServiceSpecificCredentialId: aws.String(credID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(reset.ServiceSpecificCredential.ServicePassword))
	assert.NotEqual(t, origPassword, aws.ToString(reset.ServiceSpecificCredential.ServicePassword))

	_, err = c.UpdateServiceSpecificCredential(ctx, &iam.UpdateServiceSpecificCredentialInput{
		UserName:                    aws.String(user),
		ServiceSpecificCredentialId: aws.String(credID),
		Status:                      types.StatusTypeInactive,
	})
	require.NoError(t, err)
	list2, err := c.ListServiceSpecificCredentials(ctx, &iam.ListServiceSpecificCredentialsInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, list2.ServiceSpecificCredentials, 1)
	assert.Equal(t, "Inactive", string(list2.ServiceSpecificCredentials[0].Status))

	_, err = c.DeleteServiceSpecificCredential(ctx, &iam.DeleteServiceSpecificCredentialInput{
		UserName:                    aws.String(user),
		ServiceSpecificCredentialId: aws.String(credID),
	})
	require.NoError(t, err)
}
