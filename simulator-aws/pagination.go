package main

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// awsBadToken writes the error a service answers a page token it never
// issued with.
type awsBadToken func(w http.ResponseWriter, token string)

// awsPage pages a deterministically sorted list by the decimal offset token
// the AWS slices hand out. A size of zero or less takes def, and a positive
// def also caps the page; def 0 returns every remaining item when the caller
// names no size. A token the list never issued writes bad's error and reports
// false.
func awsPage[T any](w http.ResponseWriter, bad awsBadToken, all []T, token string, size, def int) ([]T, string, bool) {
	page, next, err := listq.OffsetPage(all, token, size, def, def)
	if err != nil {
		bad(w, token)
		return nil, "", false
	}
	return page, next, true
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
