/*
Copyright Gen Digital Inc. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package protocol

import (
	"github.com/trustbloc/sidetree-go/pkg/api/operation"
	"github.com/trustbloc/sidetree-go/pkg/api/protocol"

	coreoperation "github.com/trustbloc/sidetree-svc-go/pkg/api/operation"
	"github.com/trustbloc/sidetree-svc-go/pkg/api/txn"
)

//go:generate counterfeiter -o ../../mocks/txnprocessor.gen.go --fake-name TxnProcessor . TxnProcessor
//go:generate counterfeiter -o ../../mocks/protocolversion.gen.go --fake-name ProtocolVersion . Version
//go:generate counterfeiter -o ../../mocks/operationhandler.gen.go --fake-name OperationHandler . OperationHandler
//go:generate counterfeiter -o ../../mocks/operationprovider.gen.go --fake-name OperationProvider . OperationProvider

// TxnProcessor defines the functions for processing a Sidetree transaction.
type TxnProcessor interface {
	Process(sidetreeTxn txn.SidetreeTxn, suffixes ...string) (numProcessed int, err error)
}

// AnchorDocumentType defines valid values for anchor document type.
type AnchorDocumentType string

const (

	// TypePermanent captures "permanent" anchor document type.
	TypePermanent AnchorDocumentType = "permanent"

	// TypeProvisional captures "provisional" anchor document type.
	TypeProvisional AnchorDocumentType = "provisional"
)

// AnchorDocument describes Sidetree batch files.
type AnchorDocument struct {
	ID   string
	Desc string
	Type AnchorDocumentType
}

// AnchoringInfo contains anchoring info plus additional batch information.
type AnchoringInfo struct {
	AnchorString         string
	Artifacts            []*AnchorDocument
	OperationReferences  []*coreoperation.Reference
	AdditionalOperations []*coreoperation.QueuedOperation
	ExpiredOperations    []*coreoperation.QueuedOperation
}

// OperationHandler defines an interface for creating batch files.
type OperationHandler interface {
	// PrepareTxnFiles operations will create relevant batch files, store them in CAS and return anchor string.
	PrepareTxnFiles(ops []*coreoperation.QueuedOperation) (*AnchoringInfo, error)
}

// OperationProvider retrieves the anchored operations for the given Sidetree transaction.
type OperationProvider interface {
	GetTxnOperations(sidetreeTxn *txn.SidetreeTxn) ([]*operation.AnchoredOperation, error)
}

// Version contains the protocol and corresponding implementations that are compatible with the protocol version.
type Version interface {
	protocol.Version

	TransactionProcessor() TxnProcessor
	OperationHandler() OperationHandler
	OperationProvider() OperationProvider
	DocumentComposer() protocol.DocumentComposer
}

// Client defines interface for accessing protocol version/information.
type Client interface {
	// Current returns latest version of protocol.
	Current() (Version, error)

	// Get returns the version at the given transaction time.
	Get(transactionTime uint64) (Version, error)
}

// ClientProvider returns a protocol client for the given namespace.
type ClientProvider interface {
	ForNamespace(namespace string) (Client, error)
}
