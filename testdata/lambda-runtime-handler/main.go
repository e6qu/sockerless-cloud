// lambda-runtime-handler is a minimal test handler for the AWS Lambda
// Runtime API. Polls /next, echoes the payload back via /response, or
// emits /error when the payload contains `"cause":"error"`.
//
// Implements the real AWS Lambda Runtime API contract
// (docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html), so it
// works against both real Lambda and the simulator's Runtime API slice.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if api == "" {
		fmt.Fprintln(os.Stderr, "AWS_LAMBDA_RUNTIME_API not set")
		os.Exit(1)
	}

	base := "http://" + api

	// Single-pass handler (one invocation per container in the
	// simulator today). Real Lambda's bootstrap loops indefinitely;
	// this testdata fixture is single-shot so the sim's ECS-style
	// "start a container per invocation" wiring works against it.
	fmt.Fprintf(os.Stderr, "lambda-runtime-handler: polling %s/2018-06-01/runtime/invocation/next\n", base)

	client := &http.Client{Timeout: 0}
	resp, err := client.Get(base + "/2018-06-01/runtime/invocation/next")
	if err != nil {
		fmt.Fprintf(os.Stderr, "GET /next: %v\n", err)
		postInitError(base, err.Error())
		os.Exit(1)
	}
	defer resp.Body.Close()

	requestID := resp.Header.Get("Lambda-Runtime-Aws-Request-Id")
	if requestID == "" {
		fmt.Fprintln(os.Stderr, "no Lambda-Runtime-Aws-Request-Id header")
		os.Exit(1)
	}
	deadline := resp.Header.Get("Lambda-Runtime-Deadline-Ms")
	functionArn := resp.Header.Get("Lambda-Runtime-Invoked-Function-Arn")

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read payload: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "invocation %s arn=%s deadline=%s payload=%s\n",
		requestID, functionArn, deadline, string(payload))

	// If payload contains `"cause":"error"`, report as error. Other
	// keywords trigger test-specific branches: "sleep" delays, "echo"
	// round-trips.
	payloadStr := string(payload)
	switch {
	case strings.Contains(payloadStr, `"action":"http-get"`):
		var request struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(payload, &request); err != nil || request.URL == "" {
			postError(base, requestID, []byte(`{"errorMessage":"http-get requires a valid url","errorType":"HandlerError"}`))
			break
		}
		httpClient := &http.Client{Timeout: 2 * time.Second}
		targetResponse, err := httpClient.Get(request.URL)
		if err != nil {
			errorPayload, _ := json.Marshal(map[string]string{
				"errorMessage": err.Error(),
				"errorType":    "HandlerError",
			})
			postError(base, requestID, errorPayload)
			break
		}
		targetBody, readErr := io.ReadAll(targetResponse.Body)
		targetResponse.Body.Close()
		if readErr != nil {
			errorPayload, _ := json.Marshal(map[string]string{
				"errorMessage": readErr.Error(),
				"errorType":    "HandlerError",
			})
			postError(base, requestID, errorPayload)
			break
		}
		responsePayload, _ := json.Marshal(map[string]any{
			"statusCode": targetResponse.StatusCode,
			"body":       string(targetBody),
		})
		postResponse(base, requestID, responsePayload)
	case strings.Contains(payloadStr, `"cause":"error"`):
		errPayload := []byte(`{"errorMessage":"test error from handler","errorType":"HandlerError"}`)
		postError(base, requestID, errPayload)
	case strings.Contains(payloadStr, `"action":"sleep"`):
		time.Sleep(2 * time.Second)
		postResponse(base, requestID, payload)
	default:
		postResponse(base, requestID, payload)
	}
}

func postResponse(base, id string, body []byte) {
	resp, err := http.Post(
		fmt.Sprintf("%s/2018-06-01/runtime/invocation/%s/response", base, id),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "POST /response: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "unexpected /response status: %d\n", resp.StatusCode)
	}
}

func postError(base, id string, body []byte) {
	resp, err := http.Post(
		fmt.Sprintf("%s/2018-06-01/runtime/invocation/%s/error", base, id),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "POST /error: %v\n", err)
		return
	}
	defer resp.Body.Close()
}

func postInitError(base, msg string) {
	body := []byte(fmt.Sprintf(`{"errorMessage":%q,"errorType":"InitError"}`, msg))
	resp, err := http.Post(
		base+"/2018-06-01/runtime/init/error",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}
