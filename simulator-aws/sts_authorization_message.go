package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// stsAuthorizationMessages holds the decoded document behind every encoded
// authorization message a refusal returned, under the message itself. AWS
// encrypts the document into the message; holding it here keeps the message
// as opaque to its reader as AWS's is.
var stsAuthorizationMessages sim.Store[string]

type stsDecodedPrincipal struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Arn  string `json:"arn"`
}

type stsDecodedCondition struct {
	Item struct {
		Key    string   `json:"key"`
		Values []string `json:"values"`
	} `json:"item"`
}

// stsDecodedMessage is the document the DecodeAuthorizationMessage API
// reference's sample response shows.
type stsDecodedMessage struct {
	Allowed           string `json:"allowed"`
	ExplicitDeny      string `json:"explicitDeny"`
	MatchedStatements string `json:"matchedStatements"`
	Failures          string `json:"failures"`
	Context           struct {
		Principal  stsDecodedPrincipal   `json:"principal"`
		Action     string                `json:"action"`
		Resource   string                `json:"resource"`
		Conditions []stsDecodedCondition `json:"conditions"`
	} `json:"context"`
}

// stsEncodeAuthorizationMessage records the refusal d of request r and
// returns the encoded message that DecodeAuthorizationMessage opens.
func stsEncodeAuthorizationMessage(r *http.Request, d iamDenial) string {
	var doc stsDecodedMessage
	doc.Allowed = "false"
	doc.ExplicitDeny = fmt.Sprint(d.Explicit)
	arn, id, _ := stsCallerIdentity(r)
	if arn == "" {
		arn = d.Principal
	}
	doc.Context.Principal = stsDecodedPrincipal{ID: id, Name: arn[strings.LastIndexByte(arn, '/')+1:], Arn: arn}
	doc.Context.Action = d.Action
	doc.Context.Resource = d.Resource
	keys := make([]string, 0, len(d.Context))
	for key := range d.Context {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	doc.Context.Conditions = make([]stsDecodedCondition, 0, len(keys))
	for _, key := range keys {
		var c stsDecodedCondition
		c.Item.Key, c.Item.Values = key, d.Context[key]
		doc.Context.Conditions = append(doc.Context.Conditions, c)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("encode authorization message: %v", err))
	}
	raw := make([]byte, 96)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("draw an authorization message: %v", err))
	}
	message := base64.RawURLEncoding.EncodeToString(raw)
	stsAuthorizationMessages.Put(message, string(body))
	return message
}

// handleSTSDecodeAuthorizationMessage opens a message a refusal returned.
func handleSTSDecodeAuthorizationMessage(w http.ResponseWriter, r *http.Request) {
	encoded := r.FormValue("EncodedMessage")
	if encoded == "" {
		stsErrorXML(w, "ValidationError", "EncodedMessage is required", http.StatusBadRequest)
		return
	}
	decoded, ok := stsAuthorizationMessages.Get(encoded)
	if !ok {
		// The model's InvalidAuthorizationMessageException documentation.
		stsErrorXML(w, "InvalidAuthorizationMessageException",
			"The message passed to DecodeAuthorizationMessage was invalid.", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DecodeAuthorizationMessageResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <DecodeAuthorizationMessageResult><DecodedMessage>%s</DecodedMessage></DecodeAuthorizationMessageResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DecodeAuthorizationMessageResponse>`, xmlEscape(decoded), sim.NewUUID())
}
