package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Amazon CloudWatch model declares rpc-v2-cbor for every operation, and
// the Go SDK speaks only that. An operation whose CBOR route is not written by
// hand is served from its awsJson1_0 handler, converting the request and the
// answer by the model's shapes (cloudwatch_cbor_shapes_gen.go): the two
// protocols differ in how they carry timestamps, blobs and numbers.

type cwCBORKind uint8

const (
	cwCBORScalar cwCBORKind = iota
	cwCBORStructure
	cwCBORList
	cwCBORMap
	cwCBORTimestamp
	cwCBORBlob
	cwCBORInteger
	cwCBORFloat
)

type cwCBOROperation struct {
	Input, Output string
}

type cwCBORShape struct {
	Kind    cwCBORKind
	Element string
	Members map[string]string
}

// cwCBORRoutes records, per server, the operations cwCBOR mounted, so the
// bridge serves the rest and never mounts a route twice.
var (
	cwCBORRoutesMu sync.Mutex
	cwCBORRoutes   = map[*sim.Server]map[string]bool{}
)

func cwCBORRecordRoute(srv *sim.Server, op string) {
	cwCBORRoutesMu.Lock()
	defer cwCBORRoutesMu.Unlock()
	if cwCBORRoutes[srv] == nil {
		cwCBORRoutes[srv] = map[string]bool{}
	}
	cwCBORRoutes[srv][op] = true
}

func cwCBORRouted(srv *sim.Server, op string) bool {
	cwCBORRoutesMu.Lock()
	defer cwCBORRoutesMu.Unlock()
	return cwCBORRoutes[srv][op]
}

// cwCBORUnimplemented lists the model's operations that have no handler on any
// protocol; each answers UnknownOperationException.
var cwCBORUnimplemented []string

var cwCBORDecMode, _ = cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any(nil))}.DecMode()

func registerCloudWatchCBORBridge(srv *sim.Server, router *AWSRouter) {
	operations := make([]string, 0, len(cwCBOROperations))
	for op := range cwCBOROperations {
		operations = append(operations, op)
	}
	sort.Strings(operations)
	for _, op := range operations {
		if cwCBORRouted(srv, op) {
			continue
		}
		handler, ok := router.Handler("GraniteServiceVersion20100801." + op)
		if !ok {
			cwCBORUnimplemented = append(cwCBORUnimplemented, op)
			cwCBOR(srv, op, func(w http.ResponseWriter, _ *http.Request) {
				cwWriteCBORError(w, "UnknownOperationException",
					"Amazon CloudWatch operation "+op+" is not implemented by this simulator", http.StatusBadRequest)
			})
			continue
		}
		cwCBOR(srv, op, cwCBORBridge(op, handler))
	}
}

func cwCBORBridge(op string, handler http.HandlerFunc) http.HandlerFunc {
	shapes := cwCBOROperations[op]
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
			return
		}
		input := map[string]any{}
		if len(raw) > 0 {
			var decoded any
			if err := cwCBORDecMode.Unmarshal(raw, &decoded); err != nil {
				cwWriteCBORError(w, "SerializationException", "The request body is not valid CBOR: "+err.Error(), http.StatusBadRequest)
				return
			}
			converted, ok := cwCBORToJSON(shapes.Input, decoded).(map[string]any)
			if !ok {
				cwWriteCBORError(w, "SerializationException", "The request body is not a CBOR map", http.StatusBadRequest)
				return
			}
			input = converted
		}
		body, err := json.Marshal(input)
		if err != nil {
			cwWriteCBORError(w, "InternalFailure", err.Error(), http.StatusInternalServerError)
			return
		}
		forwarded := r.Clone(r.Context())
		forwarded.Body = io.NopCloser(bytes.NewReader(body))
		forwarded.ContentLength = int64(len(body))
		forwarded.Header.Set("Content-Type", "application/x-amz-json-1.0")
		forwarded.Header.Set("X-Amz-Target", "GraniteServiceVersion20100801."+op)
		recorder := httptest.NewRecorder()
		handler(recorder, forwarded)

		decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
		decoder.UseNumber()
		var answer any = map[string]any{}
		if recorder.Body.Len() > 0 {
			if err := decoder.Decode(&answer); err != nil {
				cwWriteCBORError(w, "InternalFailure", fmt.Sprintf("%s answered a body that is not JSON: %v", op, err), http.StatusInternalServerError)
				return
			}
		}
		if recorder.Code >= 300 {
			code, message := awsJSONError(recorder.Body.Bytes())
			if code == "" {
				code = "InternalFailure"
			}
			cwWriteCBORError(w, code, message, recorder.Code)
			return
		}
		converted, err := cwJSONToCBOR(shapes.Output, answer)
		if err != nil {
			cwWriteCBORError(w, "InternalFailure", fmt.Sprintf("%s answered %v", op, err), http.StatusInternalServerError)
			return
		}
		cwWriteCBOR(w, converted)
	}
}

// cwCBORToJSON turns a decoded CBOR request value into the awsJson1_0 form of
// the same shape: epoch-second numbers for timestamps, base64 for blobs.
func cwCBORToJSON(shapeName string, value any) any {
	shape := cwCBORShapes[shapeName]
	switch shape.Kind {
	case cwCBORTimestamp:
		if t, ok := value.(time.Time); ok {
			return float64(t.UnixNano()) / 1e9
		}
	case cwCBORBlob:
		if b, ok := value.([]byte); ok {
			return base64.StdEncoding.EncodeToString(b)
		}
	case cwCBORStructure:
		if fields, ok := value.(map[string]any); ok {
			out := make(map[string]any, len(fields))
			for key, field := range fields {
				out[key] = cwCBORToJSON(shape.Members[key], field)
			}
			return out
		}
	case cwCBORMap:
		if entries, ok := value.(map[string]any); ok {
			out := make(map[string]any, len(entries))
			for key, entry := range entries {
				out[key] = cwCBORToJSON(shape.Element, entry)
			}
			return out
		}
	case cwCBORList:
		if items, ok := value.([]any); ok {
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = cwCBORToJSON(shape.Element, item)
			}
			return out
		}
	}
	return value
}

// cwJSONToCBOR turns an awsJson1_0 answer into the rpc-v2-cbor form of the
// same shape. A value the shape does not admit is an error, never passed on.
func cwJSONToCBOR(shapeName string, value any) (any, error) {
	shape := cwCBORShapes[shapeName]
	switch shape.Kind {
	case cwCBORTimestamp:
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("a timestamp that is not a number: %v", value)
		}
		seconds, err := number.Float64()
		if err != nil {
			return nil, err
		}
		whole, fraction := math.Modf(seconds)
		return time.Unix(int64(whole), int64(fraction*1e9)).UTC(), nil
	case cwCBORBlob:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("a blob that is not a string: %v", value)
		}
		return base64.StdEncoding.DecodeString(text)
	case cwCBORInteger:
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("an integer that is not a number: %v", value)
		}
		return number.Int64()
	case cwCBORFloat:
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("a float that is not a number: %v", value)
		}
		return number.Float64()
	case cwCBORStructure:
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is not an object: %v", shapeName, value)
		}
		out := make(map[string]any, len(fields))
		for key, field := range fields {
			member, known := shape.Members[key]
			if !known {
				return nil, fmt.Errorf("%s has no member %s", shapeName, key)
			}
			converted, err := cwJSONToCBOR(member, field)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", shapeName, key, err)
			}
			out[key] = converted
		}
		return out, nil
	case cwCBORMap:
		entries, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is not an object: %v", shapeName, value)
		}
		out := make(map[string]any, len(entries))
		for key, entry := range entries {
			converted, err := cwJSONToCBOR(shape.Element, entry)
			if err != nil {
				return nil, fmt.Errorf("%s[%s]: %w", shapeName, key, err)
			}
			out[key] = converted
		}
		return out, nil
	case cwCBORList:
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("%s is not an array: %v", shapeName, value)
		}
		out := make([]any, len(items))
		for i, item := range items {
			converted, err := cwJSONToCBOR(shape.Element, item)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", shapeName, i, err)
			}
			out[i] = converted
		}
		return out, nil
	}
	return value, nil
}
