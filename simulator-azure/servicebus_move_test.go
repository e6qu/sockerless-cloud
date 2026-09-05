package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// newServiceBusMoveTestStores wires the stores the Microsoft.ServiceBus move
// hook re-keys, so the hook can be driven directly against the same
// verification path the data plane uses.
func newServiceBusMoveTestStores(t *testing.T) {
	t.Helper()
	sbNamespaces = sim.MakeStore[SBNamespace](nil, "test_sb_namespaces")
	sbQueues = sim.MakeStore[SBQueue](nil, "test_sb_queues")
	sbTopics = sim.MakeStore[SBTopic](nil, "test_sb_topics")
	sbSubscriptions = sim.MakeStore[SBSubscription](nil, "test_sb_subscriptions")
	sbRules = sim.MakeStore[SBRule](nil, "test_sb_rules")
	sbAuthRules = sim.MakeStore[SBAuthorizationRule](nil, "test_sb_auth_rules")
	sbNetworkRules = sim.MakeStore[SBNetworkRuleSet](nil, "test_sb_network_rules")
	sbPrivateConns = sim.MakeStore[SBPrivateEndpointConnection](nil, "test_sb_private_conns")
	sbDRConfigs = sim.MakeStore[SBDisasterRecovery](nil, "test_sb_dr")
	sbMigrations = sim.MakeStore[SBMigrationConfig](nil, "test_sb_migrations")
	ehAuthRules = sim.MakeStore[EHAuthorizationRule](nil, "test_eh_auth_rules")
	azureKeyGens = sim.MakeStore[azureKeyGenRow](nil, "test_key_gens")
	t.Cleanup(func() {
		sbNamespaces, sbQueues, sbTopics, sbSubscriptions, sbRules = nil, nil, nil, nil, nil
		sbAuthRules, sbNetworkRules, sbPrivateConns, sbDRConfigs, sbMigrations = nil, nil, nil, nil, nil
		ehAuthRules, azureKeyGens = nil, nil
	})
}

// seedServiceBusMoveNamespace provisions a namespace with the root
// authorization rule real Service Bus auto-provisions, a queue, and a
// queue-scoped rule, and returns the namespace resource ID.
func seedServiceBusMoveNamespace(sub, rg, name string) string {
	id := sbNamespaceID(sub, rg, name)
	sbNamespaces.Put(id, SBNamespace{
		ID: id, Name: name, Type: "Microsoft.ServiceBus/namespaces", Location: "eastus",
		Properties: map[string]any{"provisioningState": "Succeeded"},
	})
	rootID := id + "/authorizationRules/RootManageSharedAccessKey"
	sbAuthRules.Put(rootID, SBAuthorizationRule{
		ID: rootID, Name: "RootManageSharedAccessKey",
		Type:       "Microsoft.ServiceBus/namespaces/authorizationRules",
		Properties: SBAuthorizationRuleSpec{Rights: []string{"Listen", "Send", "Manage"}},
	})
	queueID := id + "/queues/orders"
	sbQueues.Put(queueID, SBQueue{ID: queueID, Name: "orders", Type: "Microsoft.ServiceBus/namespaces/queues"})
	queueRuleID := queueID + "/authorizationRules/sender"
	sbAuthRules.Put(queueRuleID, SBAuthorizationRule{
		ID: queueRuleID, Name: "sender",
		Type:       "Microsoft.ServiceBus/namespaces/queues/authorizationRules",
		Properties: SBAuthorizationRuleSpec{Rights: []string{"Send"}},
	})
	return id
}

// serviceBusMoveTestToken signs the Shared Access Signature a client presents,
// exactly as the official SDKs do.
func serviceBusMoveTestToken(keyName, key, audience string) string {
	escaped := strings.ToLower(url.QueryEscape(audience))
	expiry := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(escaped + "\n" + expiry))
	signature := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return "SharedAccessSignature sr=" + escaped + "&sig=" + signature + "&se=" + expiry + "&skn=" + url.QueryEscape(keyName)
}

// TestServiceBusMovePinsAuthorizationRuleKeys drives the Microsoft.ServiceBus
// move hook against the data plane's own Shared Access Signature verification.
// A namespace's SAS keys are derived from each authorization rule's resource
// ID, which embeds the resource group, so the move has to pin them: a
// credential minted before the move must still authenticate after it, at both
// the namespace and the queue rule.
func TestServiceBusMovePinsAuthorizationRuleKeys(t *testing.T) {
	newServiceBusMoveTestStores(t)

	const sub, srcRG, dstRG, name = "sub", "sb-move-src", "sb-move-dst", "sb-move-ns"
	oldID := seedServiceBusMoveNamespace(sub, srcRG, name)
	newID := sbNamespaceID(sub, dstRG, name)

	rootRuleID := oldID + "/authorizationRules/RootManageSharedAccessKey"
	queueRuleID := oldID + "/queues/orders/authorizationRules/sender"
	rootKeyBefore := azureKeyMaterial32(rootRuleID, "primary")
	queueKeyBefore := azureKeyMaterial32(queueRuleID, "primary")

	namespaceToken := serviceBusMoveTestToken("RootManageSharedAccessKey", rootKeyBefore,
		"https://"+name+".servicebus.localhost/")
	queueToken := serviceBusMoveTestToken("sender", queueKeyBefore,
		"https://"+name+".servicebus.localhost/orders")

	if _, err := verifyMessagingSAS(name, namespaceToken); err != nil {
		t.Fatalf("the pre-move namespace credential must authenticate before the move: %v", err)
	}

	moveServiceBusNamespaceARM(oldID, newID)

	// The ARM subtree re-homed.
	if _, ok := sbNamespaces.Get(newID); !ok {
		t.Fatalf("the namespace did not re-key onto %s", newID)
	}
	if _, ok := sbNamespaces.Get(oldID); ok {
		t.Errorf("the namespace still answers under the source group id %s", oldID)
	}
	for _, id := range []string{
		newID + "/queues/orders",
	} {
		if _, ok := sbQueues.Get(id); !ok {
			t.Errorf("the queue did not re-key onto %s", id)
		}
	}
	movedRootRule := newID + "/authorizationRules/RootManageSharedAccessKey"
	if _, ok := sbAuthRules.Get(movedRootRule); !ok {
		t.Errorf("the root authorization rule did not re-key onto %s", movedRootRule)
	}

	// listKeys serves the same material at the destination coordinates.
	if got := azureKeyMaterial32(movedRootRule, "primary"); got != rootKeyBefore {
		t.Errorf("the namespace rule's primary key rotated across the move: %q != %q", got, rootKeyBefore)
	}
	movedQueueRule := newID + "/queues/orders/authorizationRules/sender"
	if got := azureKeyMaterial32(movedQueueRule, "primary"); got != queueKeyBefore {
		t.Errorf("the queue rule's primary key rotated across the move: %q != %q", got, queueKeyBefore)
	}
	if got := azureKeyMaterial32(movedRootRule, "secondary"); got != azureKeyMaterial32(rootRuleID, "secondary") {
		t.Error("the namespace rule's secondary key rotated across the move")
	}

	// The credentials minted before the move still authenticate against the
	// data plane's own verification.
	if _, err := verifyMessagingSAS(name, namespaceToken); err != nil {
		t.Errorf("the pre-move namespace credential stopped authenticating after the move: %v", err)
	}
	if _, err := verifyMessagingSAS(name, queueToken); err != nil {
		t.Errorf("the pre-move queue credential stopped authenticating after the move: %v", err)
	}

	// A rotation after the move clears the pin and yields genuinely new
	// material, so the move does not freeze the rotation contract.
	azureBumpKeyGen(movedRootRule, "primary", "")
	if got := azureKeyMaterial32(movedRootRule, "primary"); got == rootKeyBefore {
		t.Error("regenerating the primary key after the move served the pinned material again")
	}
	if _, err := verifyMessagingSAS(name, namespaceToken); err == nil {
		t.Error("a token signed with the key a rotation replaced must stop authenticating")
	}
}

// TestServiceBusMoveWithoutKeyPinningBreaksTheCredential is the negative
// control for the pin. It re-keys the same namespace the way a move that only
// re-homed the ARM records would — the records land at the destination, but
// nothing carries the derived key material — and shows the credential minted
// before the move no longer authenticates. That failure is what the pin in
// moveServiceBusNamespaceARM exists to prevent.
func TestServiceBusMoveWithoutKeyPinningBreaksTheCredential(t *testing.T) {
	newServiceBusMoveTestStores(t)

	const sub, srcRG, dstRG, name = "sub", "sb-nopin-src", "sb-nopin-dst", "sb-nopin-ns"
	oldID := seedServiceBusMoveNamespace(sub, srcRG, name)
	newID := sbNamespaceID(sub, dstRG, name)

	rootRuleID := oldID + "/authorizationRules/RootManageSharedAccessKey"
	token := serviceBusMoveTestToken("RootManageSharedAccessKey",
		azureKeyMaterial32(rootRuleID, "primary"),
		"https://"+name+".servicebus.localhost/")
	if _, err := verifyMessagingSAS(name, token); err != nil {
		t.Fatalf("the credential must authenticate before the move: %v", err)
	}

	// The move, minus the pin: the namespace and its subtree re-key and
	// nothing else happens.
	ns, ok := sbNamespaces.Get(oldID)
	if !ok {
		t.Fatal("seeded namespace missing")
	}
	sbNamespaces.Delete(oldID)
	ns.ID = newID
	sbNamespaces.Put(ns.ID, ns)
	rekeyRowsByPrefix(sbQueues, oldID+"/", newID+"/", func(q *SBQueue) *string { return &q.ID })
	rekeyRowsByPrefix(sbAuthRules, oldID+"/", newID+"/", func(a *SBAuthorizationRule) *string { return &a.ID })

	movedRootRule := newID + "/authorizationRules/RootManageSharedAccessKey"
	if got := azureKeyMaterial32(movedRootRule, "primary"); got == azureKeyMaterial32(rootRuleID, "primary") {
		t.Fatal("the unpinned control did not actually change the derived key material, so it proves nothing")
	}
	if _, err := verifyMessagingSAS(name, token); err == nil {
		t.Error("without the pin the pre-move credential still authenticated; the control cannot detect a rotation")
	}
}
