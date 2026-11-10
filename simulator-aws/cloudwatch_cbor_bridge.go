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
// the Go SDK speaks only that. Serve an operation without a hand-written CBOR
// route from its awsJson1_0 handler, converting by the model's shapes: the
// protocols differ in timestamps, blobs and numbers.

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

// Record the operations cwCBOR mounted so the bridge serves the rest and never
// mounts a route twice.
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

// Each answers UnknownOperationException.
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
	return awsCBORBridge(awsCBORModel{model: "cloudwatch", target: "GraniteServiceVersion20100801", contentType: "application/x-amz-json-1.0",
		operations: cwCBOROperations, shapes: cwCBORShapes}, op, handler)
}

// awsCBORModel is what a bridge needs of a model that declares rpc-v2-cbor
// beside an awsJson protocol: its JSON coordinates and its shape table.
type awsCBORModel struct {
	model, target, contentType string
	operations                 map[string]cwCBOROperation
	shapes                     map[string]cwCBORShape
}

// awsCBORWriteError writes a model's error in rpc-v2-cbor.
func awsCBORWriteError(w http.ResponseWriter, model, code, message string, status int) {
	data, err := cwEncMode.Marshal(awsErrorBody(model, code, message))
	if err != nil {
		AWSError(w, "InternalFailure", "Failed to encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// awsCBORBridge serves op's rpc-v2-cbor surface from its awsJson handler.
func awsCBORBridge(m awsCBORModel, op string, handler http.HandlerFunc) http.HandlerFunc {
	shapes := m.operations[op]
	fail := func(w http.ResponseWriter, code, message string, status int) {
		awsCBORWriteError(w, m.model, code, message, status)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			fail(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
			return
		}
		input := map[string]any{}
		if len(raw) > 0 {
			var decoded any
			if err := cwCBORDecMode.Unmarshal(raw, &decoded); err != nil {
				fail(w, "SerializationException", "The request body is not valid CBOR: "+err.Error(), http.StatusBadRequest)
				return
			}
			converted, ok := cwCBORToJSON(m.shapes, shapes.Input, decoded).(map[string]any)
			if !ok {
				fail(w, "SerializationException", "The request body is not a CBOR map", http.StatusBadRequest)
				return
			}
			input = converted
		}
		body, err := json.Marshal(input)
		if err != nil {
			fail(w, "InternalFailure", err.Error(), http.StatusInternalServerError)
			return
		}
		forwarded := r.Clone(r.Context())
		forwarded.Body = io.NopCloser(bytes.NewReader(body))
		forwarded.ContentLength = int64(len(body))
		forwarded.Header.Set("Content-Type", m.contentType)
		forwarded.Header.Set("X-Amz-Target", m.target+"."+op)
		recorder := httptest.NewRecorder()
		handler(recorder, forwarded)

		decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
		decoder.UseNumber()
		var answer any = map[string]any{}
		if recorder.Body.Len() > 0 {
			if err := decoder.Decode(&answer); err != nil {
				fail(w, "InternalFailure", fmt.Sprintf("%s answered a body that is not JSON: %v", op, err), http.StatusInternalServerError)
				return
			}
		}
		if recorder.Code >= 300 {
			code, message := awsJSONError(recorder.Body.Bytes())
			if code == "" {
				code = "InternalFailure"
			}
			fail(w, code, message, recorder.Code)
			return
		}
		converted, err := cwJSONToCBOR(m.shapes, shapes.Output, answer)
		if err != nil {
			fail(w, "InternalFailure", fmt.Sprintf("%s answered %v", op, err), http.StatusInternalServerError)
			return
		}
		cwWriteCBOR(w, converted)
	}
}

// awsJson1_0 carries timestamps as epoch seconds and blobs as base64.
func cwCBORToJSON(shapes map[string]cwCBORShape, shapeName string, value any) any {
	shape := shapes[shapeName]
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
				out[key] = cwCBORToJSON(shapes, shape.Members[key], field)
			}
			return out
		}
	case cwCBORMap:
		if entries, ok := value.(map[string]any); ok {
			out := make(map[string]any, len(entries))
			for key, entry := range entries {
				out[key] = cwCBORToJSON(shapes, shape.Element, entry)
			}
			return out
		}
	case cwCBORList:
		if items, ok := value.([]any); ok {
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = cwCBORToJSON(shapes, shape.Element, item)
			}
			return out
		}
	}
	return value
}

// A value the shape does not admit is an error, never passed on.
func cwJSONToCBOR(shapes map[string]cwCBORShape, shapeName string, value any) (any, error) {
	shape := shapes[shapeName]
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
			converted, err := cwJSONToCBOR(shapes, member, field)
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
			converted, err := cwJSONToCBOR(shapes, shape.Element, entry)
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
			converted, err := cwJSONToCBOR(shapes, shape.Element, item)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", shapeName, i, err)
			}
			out[i] = converted
		}
		return out, nil
	}
	return value, nil
}
