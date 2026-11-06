package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

var kmsAWSManagedKeyMu sync.Mutex

// kmsAWSManagedKey returns the AWS managed key that a service names
// alias/aws/<service>. AWS KMS creates it in the account the first time the
// service encrypts with its default key.
func kmsAWSManagedKey(service string) (KMSKey, error) {
	kmsAWSManagedKeyMu.Lock()
	defer kmsAWSManagedKeyMu.Unlock()
	alias := "alias/aws/" + service
	if keyId, ok := kmsAliases.Get(alias); ok {
		if key, ok := kmsKeys.Get(keyId); ok {
			return key, nil
		}
	}
	keyId := sim.NewUUID()
	key := KMSKey{
		KeyId:        keyId,
		Arn:          kmsKeyArn(keyId),
		KeyState:     "Enabled",
		KeyUsage:     "ENCRYPT_DECRYPT",
		KeyManager:   "AWS",
		Origin:       "AWS_KMS",
		Spec:         "SYMMETRIC_DEFAULT",
		CreationDate: float64(time.Now().Unix()),
	}
	kmsKeys.Put(keyId, key)
	if _, err := kmsGenerateKeyMaterial(keyId); err != nil {
		kmsKeys.Delete(keyId)
		return KMSKey{}, fmt.Errorf("generate material for %s: %w", alias, err)
	}
	kmsPutKeyPolicy(keyId, "")
	kmsAliases.Put(alias, keyId)
	kmsAliasNames.Put(alias, alias)
	return key, nil
}
