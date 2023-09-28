/*
Copyright Gen Digital Inc. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package operation

import "github.com/trustbloc/sidetree-go/pkg/api/operation"

// Reference holds minimum information about did operation (suffix and type).
type Reference struct {
	// UniqueSuffix defines document unique suffix.
	UniqueSuffix string

	// Type defines operation type.
	Type operation.Type

	// AnchorOrigin defines anchor origin.
	AnchorOrigin interface{}
}

// QueuedOperation stores minimum required operation info for operations queue.
type QueuedOperation struct {
	Type             operation.Type
	OperationRequest []byte
	UniqueSuffix     string
	Namespace        string
	AnchorOrigin     interface{}
	Properties       []operation.Property
}

// QueuedOperationAtTime contains queued operation info with protocol genesis time.
type QueuedOperationAtTime struct {
	QueuedOperation
	ProtocolVersion uint64
}

// QueuedOperationsAtTime contains a collection of queued operations with protocol genesis time.
type QueuedOperationsAtTime []*QueuedOperationAtTime

// QueuedOperations returns a collection of QueuedOperation.
func (o QueuedOperationsAtTime) QueuedOperations() []*QueuedOperation {
	ops := make([]*QueuedOperation, len(o))

	for i, op := range o {
		ops[i] = &op.QueuedOperation
	}

	return ops
}
