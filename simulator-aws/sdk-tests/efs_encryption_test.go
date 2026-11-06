package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEFS_FileSystemEncryption expects CreateFileSystem to keep Encrypted and
// KmsKeyId as the vendored model describes: an encrypted file system without
// a key uses the AWS managed key aws/elasticfilesystem, a named key reads back
// as its ARN, KmsKeyId requires Encrypted, only symmetric keys are accepted,
// and a creation token already in use answers FileSystemAlreadyExists.
func TestEFS_FileSystemEncryption(t *testing.T) {
	t.Parallel()
	client := efsClient()
	kmsc := kmsClient()
	create := func(in *efs.CreateFileSystemInput) *efs.CreateFileSystemOutput {
		t.Helper()
		out, err := client.CreateFileSystem(ctx, in)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: out.FileSystemId})
		})
		return out
	}
	describe := func(id *string) efstypes.FileSystemDescription {
		t.Helper()
		out, err := client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{FileSystemId: id})
		require.NoError(t, err)
		require.Len(t, out.FileSystems, 1)
		return out.FileSystems[0]
	}

	plain := create(&efs.CreateFileSystemInput{CreationToken: aws.String(uniquename.New("efs-plain"))})
	assert.False(t, aws.ToBool(plain.Encrypted))
	assert.Nil(t, plain.KmsKeyId)

	token := uniquename.New("efs-default-key")
	byDefault := create(&efs.CreateFileSystemInput{CreationToken: aws.String(token), Encrypted: aws.Bool(true)})
	managed, err := kmsc.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String("alias/aws/elasticfilesystem")})
	require.NoError(t, err)
	assert.Equal(t, kmstypes.KeyManagerTypeAws, managed.KeyMetadata.KeyManager)
	got := describe(byDefault.FileSystemId)
	assert.True(t, aws.ToBool(got.Encrypted))
	assert.Equal(t, aws.ToString(managed.KeyMetadata.Arn), aws.ToString(got.KmsKeyId))

	again := create(&efs.CreateFileSystemInput{CreationToken: aws.String(uniquename.New("efs-default-key")), Encrypted: aws.Bool(true)})
	assert.Equal(t, aws.ToString(managed.KeyMetadata.Arn), aws.ToString(again.KmsKeyId), "one AWS managed key serves the account")

	_, err = client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{CreationToken: aws.String(token)})
	var exists *efstypes.FileSystemAlreadyExists
	require.True(t, errors.As(err, &exists), "%v", err)
	assert.Equal(t, aws.ToString(byDefault.FileSystemId), aws.ToString(exists.FileSystemId))

	key, err := kmsc.CreateKey(ctx, &kms.CreateKeyInput{})
	require.NoError(t, err)
	alias := "alias/" + uniquename.New("efs-key")
	_, err = kmsc.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(alias), TargetKeyId: key.KeyMetadata.KeyId})
	require.NoError(t, err)
	aliasArn := strings.Replace(aws.ToString(key.KeyMetadata.Arn), "key/"+aws.ToString(key.KeyMetadata.KeyId), alias, 1)
	named := create(&efs.CreateFileSystemInput{
		CreationToken: aws.String(uniquename.New("efs-named-key")), Encrypted: aws.Bool(true), KmsKeyId: aws.String(aliasArn),
	})
	assert.Equal(t, aws.ToString(key.KeyMetadata.Arn), aws.ToString(describe(named.FileSystemId).KmsKeyId))

	_, err = client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String(uniquename.New("efs-key-unencrypted")), KmsKeyId: key.KeyMetadata.KeyId,
	})
	var bad *efstypes.BadRequest
	require.True(t, errors.As(err, &bad), "KmsKeyId without Encrypted: %v", err)

	signing, err := kmsc.CreateKey(ctx, &kms.CreateKeyInput{KeySpec: kmstypes.KeySpecEccNistP256, KeyUsage: kmstypes.KeyUsageTypeSignVerify})
	require.NoError(t, err)
	_, err = client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String(uniquename.New("efs-asymmetric")), Encrypted: aws.Bool(true), KmsKeyId: signing.KeyMetadata.KeyId,
	})
	require.True(t, errors.As(err, &bad), "asymmetric key: %v", err)

	_, err = kmsc.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String("alias/aws/" + uniquename.New("reserved")), TargetKeyId: key.KeyMetadata.KeyId})
	var reserved *kmstypes.InvalidAliasNameException
	require.True(t, errors.As(err, &reserved), "alias/aws/ is reserved for AWS managed keys: %v", err)
}
