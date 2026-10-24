package main

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// protoEnumTable names a proto enum's values by number, as the API's
// descriptor declares them.
type protoEnumTable interface {
	enumNames() map[int32]string
}

// protoEnum is a proto enum field. The REST transport of the Go clients sends
// an enum as its number and other clients send its name; the API accepts
// both and answers with the name, so a number maps through T's table, and
// the zero value is the unset field.
type protoEnum[T protoEnumTable] string

func (e *protoEnum[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*e = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return err
		}
		*e = protoEnum[T](name)
		return nil
	}
	number, err := strconv.ParseInt(string(data), 10, 32)
	if err != nil {
		return fmt.Errorf("enum value %s is neither a name nor a number", data)
	}
	if number == 0 {
		*e = ""
		return nil
	}
	var table T
	name, ok := table.enumNames()[int32(number)]
	if !ok {
		return fmt.Errorf("enum value %d is not declared", number)
	}
	*e = protoEnum[T](name)
	return nil
}

type functionsEnvironment struct{}

func (functionsEnvironment) enumNames() map[int32]string {
	return map[int32]string{1: "GEN_1", 2: "GEN_2"}
}

type functionsVPCConnectorEgress struct{}

func (functionsVPCConnectorEgress) enumNames() map[int32]string {
	return map[int32]string{1: "PRIVATE_RANGES_ONLY", 2: "ALL_TRAFFIC"}
}

type functionsIngress struct{}

func (functionsIngress) enumNames() map[int32]string {
	return map[int32]string{1: "ALLOW_ALL", 2: "ALLOW_INTERNAL_ONLY", 3: "ALLOW_INTERNAL_AND_GCLB"}
}

type runRestartPolicy struct{}

func (runRestartPolicy) enumNames() map[int32]string {
	return map[int32]string{1: "ALWAYS", 2: "ON_FAILURE", 3: "NEVER"}
}

type runEncryptionKeyRevocationAction struct{}

func (runEncryptionKeyRevocationAction) enumNames() map[int32]string {
	return map[int32]string{1: "PREVENT_NEW", 2: "SHUTDOWN"}
}

type runConditionState struct{}

func (runConditionState) enumNames() map[int32]string {
	return map[int32]string{1: "CONDITION_PENDING", 2: "CONDITION_RECONCILING", 3: "CONDITION_FAILED", 4: "CONDITION_SUCCEEDED"}
}

type runScalingMode struct{}

func (runScalingMode) enumNames() map[int32]string {
	return map[int32]string{1: "AUTOMATIC", 2: "MANUAL"}
}

type runExecutionEnvironment struct{}

func (runExecutionEnvironment) enumNames() map[int32]string {
	return map[int32]string{1: "EXECUTION_ENVIRONMENT_GEN1", 2: "EXECUTION_ENVIRONMENT_GEN2"}
}
