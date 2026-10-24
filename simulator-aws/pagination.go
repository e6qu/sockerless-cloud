package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// awsBadToken writes the error a service answers a page token it never
// issued with.
type awsBadToken func(w http.ResponseWriter, token string)

// awsKeyPage sorts all by key, which must be unique, and pages it with a
// token that names the last key returned, so a create or delete between two
// calls neither repeats nor skips an item.
func awsKeyPage[T any](w http.ResponseWriter, bad awsBadToken, all []T, key func(T) string, token string, size, def int) ([]T, string, bool) {
	page, next, err := listq.KeyPage(sortBy(all, key), key, token, size, def, def)
	if err != nil {
		bad(w, token)
		return nil, "", false
	}
	return page, next, true
}

// awsSeqPage pages a list kept in the order it grew, such as an event
// history, keying each item by its position in that order so an item appended
// between two calls lands after the token. desc pages the newest item first.
func awsSeqPage[T any](w http.ResponseWriter, bad awsBadToken, all []T, desc bool, token string, size, def int) ([]T, string, bool) {
	type seq struct {
		pos  int
		item T
	}
	wrapped := make([]seq, len(all))
	for i, item := range all {
		wrapped[i] = seq{i, item}
	}
	key := func(s seq) string {
		if desc {
			return awsDescKey(listq.IntKey(int64(s.pos)))
		}
		return listq.IntKey(int64(s.pos))
	}
	page, next, ok := awsKeyPage(w, bad, wrapped, key, token, size, def)
	if !ok {
		return nil, "", false
	}
	out := make([]T, len(page))
	for i, s := range page {
		out[i] = s.item
	}
	return out, next, true
}

// awsFloatKey spells f so that string order matches numeric order.
func awsFloatKey(f float64) string {
	b := math.Float64bits(f)
	if b>>63 == 1 {
		b = ^b
	} else {
		b |= 1 << 63
	}
	return fmt.Sprintf("%020d", b)
}

// awsDescKey spells s so that ascending string order lists it descending:
// it inverts every byte and ends on 0xff, which sorts a prefix after every
// longer string it starts.
func awsDescKey(s string) string {
	b := make([]byte, len(s)+1)
	for i := 0; i < len(s); i++ {
		b[i] = ^s[i]
	}
	b[len(s)] = 0xff
	return string(b)
}

// awsFieldKey keys a JSON-shaped item by one of its string fields.
func awsFieldKey(field string) func(map[string]any) string {
	return func(m map[string]any) string {
		s, _ := m[field].(string)
		return s
	}
}

// awsNamedOrKeyPage pages a list a request named item by item in the
// request's own order, duplicates included, keyed by position; the request
// fixes that list. Any other list pages by key.
func awsNamedOrKeyPage[T any](w http.ResponseWriter, bad awsBadToken, all []T, named bool, key func(T) string, token string, size, def int) ([]T, string, bool) {
	if named {
		return awsSeqPage(w, bad, all, false, token, size, def)
	}
	return awsKeyPage(w, bad, all, key, token, size, def)
}

func awsBadTokenMessage(token string) string {
	return fmt.Sprintf("The pagination token %q is not valid.", token)
}

// awsJSONBadToken answers with a JSON error document under code.
func awsJSONBadToken(code string) awsBadToken {
	return func(w http.ResponseWriter, token string) {
		AWSError(w, code, awsBadTokenMessage(token), http.StatusBadRequest)
	}
}

// Each service's answer to a foreign token is the error its Smithy model
// declares for the list operations, or the service's documented validation
// error where the model declares none.
var (
	acmBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		acmWriteError(w, "ValidationException", awsBadTokenMessage(token))
	}
	amplifyBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		amplifyWriteError(w, http.StatusBadRequest, "BadRequestException", awsBadTokenMessage(token))
	}
	appASBadToken             = awsJSONBadToken("InvalidNextTokenException")
	asBadToken    awsBadToken = func(w http.ResponseWriter, token string) {
		asError(w, "InvalidNextToken", awsBadTokenMessage(token), http.StatusBadRequest)
	}
	batchBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		batchWriteError(w, http.StatusBadRequest, awsBadTokenMessage(token))
	}
	cloudMapBadToken             = awsJSONBadToken("InvalidInput")
	cbBadToken       awsBadToken = func(w http.ResponseWriter, token string) {
		cbWriteError(w, "InvalidInputException", awsBadTokenMessage(token))
	}
	ddbBadToken             = awsJSONBadToken("ValidationException")
	ec2BadToken awsBadToken = func(w http.ResponseWriter, token string) {
		ec2ErrorXML(w, "InvalidPaginationToken", awsBadTokenMessage(token), http.StatusBadRequest)
	}
	ecrBadToken              = awsJSONBadToken("InvalidParameterException")
	ecsBadToken              = awsJSONBadToken("InvalidParameterException")
	efsBadToken              = awsJSONBadToken("BadRequest")
	ebBadToken               = awsJSONBadToken("ValidationException")
	glueBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		glueWriteError(w, "InvalidInputException", awsBadTokenMessage(token))
	}
	iamBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		iamErrorXML(w, "InvalidInput", awsBadTokenMessage(token), http.StatusBadRequest)
	}
	kmsBadToken                = awsJSONBadToken("InvalidMarkerException")
	lambdaBadToken             = awsJSONBadToken("InvalidParameterValueException")
	logsBadToken               = awsJSONBadToken("InvalidParameterException")
	r53BadToken    awsBadToken = func(w http.ResponseWriter, token string) {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", awsBadTokenMessage(token))
	}
	sfnBadToken awsBadToken = func(w http.ResponseWriter, token string) { sfnWriteError(w, "InvalidToken", awsBadTokenMessage(token)) }
	smBadToken              = awsJSONBadToken("InvalidNextTokenException")
	sqsBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		sqsErrorJSON(w, "InvalidParameterValue", awsBadTokenMessage(token), http.StatusBadRequest)
	}
	ssmBadToken             = awsJSONBadToken("InvalidNextToken")
	wafBadToken awsBadToken = func(w http.ResponseWriter, token string) {
		wafWriteError(w, "WAFInvalidParameterException", awsBadTokenMessage(token))
	}
)

// snsBadToken answers in the Amazon SNS query protocol, whose error document
// carries the request's id.
func snsBadToken(r *http.Request) awsBadToken {
	return func(w http.ResponseWriter, token string) {
		snsErrorXML(w, "InvalidParameter", awsBadTokenMessage(token), http.StatusBadRequest, sim.RequestID(r.Context()))
	}
}

// awsMaxResults reads an optional *int32 page-size param, treating nil and
// non-positive values as "no page size requested".
func awsMaxResults(v *int32) int {
	if v == nil || *v <= 0 {
		return 0
	}
	return int(*v)
}

// sortBy is a convenience wrapper that sorts a slice in-place by a string key
// and returns it (for chaining).
func sortBy[T any](s []T, key func(T) string) []T {
	sort.Slice(s, func(i, j int) bool { return key(s[i]) < key(s[j]) })
	return s
}
