package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// entraPasswordIterations is the PBKDF2-SHA256 work factor of a stored user
// password verifier. Microsoft Entra stores a derived verifier, never the
// password, and so does the simulator.
const entraPasswordIterations = 100_000

// entraHashPassword derives the verifier stored for a user's password, as
// "pbkdf2-sha256$<iterations>$<salt>$<key>".
func entraHashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, entraPasswordIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", entraPasswordIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// entraPasswordMatches reports whether password derives the stored verifier.
// A user with no verifier has no password to sign in with.
func entraPasswordMatches(verifier, password string) bool {
	parts := strings.Split(verifier, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// entraInvalidPasswordProfile is Graph's description of a passwordProfile it
// cannot accept.
const entraInvalidPasswordProfile = "Invalid value specified for property 'passwordProfile' of resource 'User'."

// entraPasswordProfile reads the passwordProfile member of a Graph user write:
// whether the body carried one, and whether it names a password.
func entraPasswordProfile(props map[string]json.RawMessage) (password string, present, valid bool) {
	raw, ok := props["passwordProfile"]
	if !ok {
		return "", false, true
	}
	var profile struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil || profile.Password == "" {
		return "", true, false
	}
	return profile.Password, true, true
}

// entraSTSError writes the Microsoft Entra security token service's error
// body: the OAuth 2.0 error, an AADSTS-numbered description carrying the trace
// and correlation identifiers and timestamp, and the same identifiers as
// members.
func entraSTSError(w http.ResponseWriter, status int, code string, aadsts int, message string) {
	now := time.Now().UTC()
	traceID, correlationID := newGraphID(), newGraphID()
	timestamp := now.Format("2006-01-02 15:04:05Z")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": code,
		"error_description": fmt.Sprintf("AADSTS%d: %s Trace ID: %s Correlation ID: %s Timestamp: %s",
			aadsts, message, traceID, correlationID, timestamp),
		"error_codes":    []int{aadsts},
		"timestamp":      timestamp,
		"trace_id":       traceID,
		"correlation_id": correlationID,
		"error_uri":      fmt.Sprintf("https://login.microsoftonline.com/error?code=%d", aadsts),
	})
}
