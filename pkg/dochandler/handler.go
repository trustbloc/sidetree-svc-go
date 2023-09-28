/*
Copyright Gen Digital Inc. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package dochandler performs document operation processing and document resolution.
//
// During operation processing it will use configured validator to validate document operation and then it will call
// batch writer to add it to the batch.
//
// Document resolution is based on ID or encoded original document.
// 1) ID - the latest document will be returned if found.
//
// 2) Encoded original document - The encoded document is hashed using the current supported hashing algorithm to
// compute ID, after which the resolution is done against the computed ID. If a document cannot be found,
// the supplied document is used directly to generate and return a resolved document. In this case the supplied document
// is subject to the same validation as an original document in a create operation.
package dochandler

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trustbloc/logutil-go/pkg/log"
	coreoperation "github.com/trustbloc/sidetree-go/pkg/api/operation"
	coreprotocol "github.com/trustbloc/sidetree-go/pkg/api/protocol"
	"github.com/trustbloc/sidetree-go/pkg/canonicalizer"
	"github.com/trustbloc/sidetree-go/pkg/document"
	"github.com/trustbloc/sidetree-go/pkg/docutil"

	"github.com/trustbloc/sidetree-svc-go/pkg/api/operation"
	"github.com/trustbloc/sidetree-svc-go/pkg/api/protocol"
	logfields "github.com/trustbloc/sidetree-svc-go/pkg/internal/log"
)

var logger = log.New("sidetree-svc-dochandler")

const (
	keyID = "id"

	badRequest = "bad request"
)

// DocumentHandler implements document handler.
type DocumentHandler struct {
	protocol  protocol.Client
	processor operationProcessor
	decorator operationDecorator
	writer    batchWriter
	namespace string
	aliases   []string // namespace aliases
	domain    string
	label     string

	unpublishedOperationStore unpublishedOperationStore
	unpublishedOperationTypes []coreoperation.Type

	metrics metricsProvider
}

type unpublishedOperationStore interface {
	// Put saves operation into unpublished operation store.
	Put(op *coreoperation.AnchoredOperation) error
	// Delete deletes operation from unpublished operation store.
	Delete(op *coreoperation.AnchoredOperation) error
}

// operationDecorator is an interface for validating/pre-processing operations.
type operationDecorator interface {
	Decorate(operation *coreoperation.Operation) (*coreoperation.Operation, error)
}

// operationProcessor is an interface which resolves the document based on the unique suffix.
type operationProcessor interface {
	Resolve(uniqueSuffix string, opts ...document.ResolutionOption) (*coreprotocol.ResolutionModel, error)
}

// batchWriter is an interface to add an operation to the batch.
type batchWriter interface {
	Add(operation *operation.QueuedOperation, protocolVersion uint64) error
}

// Option is an option for document handler.
type Option func(opts *DocumentHandler)

// WithDomain sets optional domain hint for unpublished/interim documents.
func WithDomain(domain string) Option {
	return func(opts *DocumentHandler) {
		opts.domain = domain
	}
}

// WithLabel sets optional label for unpublished/interim documents.
func WithLabel(label string) Option {
	return func(opts *DocumentHandler) {
		opts.label = label
	}
}

// WithUnpublishedOperationStore stores unpublished operation into unpublished operation store.
func WithUnpublishedOperationStore(store unpublishedOperationStore, operationTypes []coreoperation.Type) Option {
	return func(opts *DocumentHandler) {
		opts.unpublishedOperationStore = store
		opts.unpublishedOperationTypes = operationTypes
	}
}

// WithOperationDecorator sets an optional operation decorator (used for additional business validation/pre-processing).
func WithOperationDecorator(decorator operationDecorator) Option {
	return func(opts *DocumentHandler) {
		opts.decorator = decorator
	}
}

type metricsProvider interface {
	ProcessOperation(duration time.Duration)
	GetProtocolVersionTime(since time.Duration)
	ParseOperationTime(since time.Duration)
	ValidateOperationTime(since time.Duration)
	DecorateOperationTime(since time.Duration)
	AddUnpublishedOperationTime(since time.Duration)
	AddOperationToBatchTime(since time.Duration)
	GetCreateOperationResultTime(since time.Duration)
}

// New creates a new document handler with the context.
func New(namespace string, aliases []string, pc protocol.Client, writer batchWriter, processor operationProcessor,
	metrics metricsProvider, opts ...Option) *DocumentHandler {
	dh := &DocumentHandler{
		protocol:                  pc,
		processor:                 processor,
		decorator:                 &defaultOperationDecorator{processor: processor},
		writer:                    writer,
		namespace:                 namespace,
		aliases:                   aliases,
		metrics:                   metrics,
		unpublishedOperationStore: &noopUnpublishedOpsStore{},
		unpublishedOperationTypes: []coreoperation.Type{},
	}

	// apply options
	for _, opt := range opts {
		opt(dh)
	}

	return dh
}

// Namespace returns the namespace of the document handler.
func (r *DocumentHandler) Namespace() string {
	return r.namespace
}

// ProcessOperation validates operation and adds it to the batch.
func (r *DocumentHandler) ProcessOperation(operationBuffer []byte, protocolVersion uint64) (*document.ResolutionResult, error) {
	startTime := time.Now()

	defer func() {
		r.metrics.ProcessOperation(time.Since(startTime))
	}()

	getProtocolVersionTime := time.Now()

	pv, err := r.protocol.Get(protocolVersion)
	if err != nil {
		return nil, err
	}

	r.metrics.GetProtocolVersionTime(time.Since(getProtocolVersionTime))

	parseOperationStartTime := time.Now()

	op, err := pv.OperationParser().Parse(r.namespace, operationBuffer)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	r.metrics.ParseOperationTime(time.Since(parseOperationStartTime))

	validateOperationStartTime := time.Now()

	// perform validation for operation request
	err = r.validateOperation(op, pv)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	r.metrics.ValidateOperationTime(time.Since(validateOperationStartTime))

	decorateOperationStartTime := time.Now()

	op, err = r.decorator.Decorate(op)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	r.metrics.DecorateOperationTime(time.Since(decorateOperationStartTime))

	unpublishedOp := r.getUnpublishedOperation(op, pv)

	addUnpublishedOperationStartTime := time.Now()

	err = r.addOperationToUnpublishedOpsStore(unpublishedOp)
	if err != nil {
		return nil, fmt.Errorf("failed to add operation for suffix[%s] to unpublished operation store: %s", op.UniqueSuffix, err.Error())
	}

	r.metrics.AddUnpublishedOperationTime(time.Since(addUnpublishedOperationStartTime))

	addToBatchStartTime := time.Now()

	// validated operation will be added to the batch
	if err := r.addToBatch(op, pv.Protocol().GenesisTime); err != nil {
		logger.Error("Failed to add operation to batch", log.WithError(err))

		r.deleteOperationFromUnpublishedOpsStore(unpublishedOp)

		return nil, err
	}

	r.metrics.AddOperationToBatchTime(time.Since(addToBatchStartTime))

	logger.Debug("Operation added to the batch", logfields.WithOperationID(op.ID))

	// create operation will also return document
	if op.Type == coreoperation.TypeCreate {
		return r.getCreateResponse(op, pv)
	}

	return nil, nil
}

func (r *DocumentHandler) getUnpublishedOperation(op *coreoperation.Operation, pv coreprotocol.Version) *coreoperation.AnchoredOperation {
	if !contains(r.unpublishedOperationTypes, op.Type) {
		return nil
	}

	return &coreoperation.AnchoredOperation{
		Type:             op.Type,
		UniqueSuffix:     op.UniqueSuffix,
		OperationRequest: op.OperationRequest,
		TransactionTime:  uint64(time.Now().Unix()),
		ProtocolVersion:  pv.Protocol().GenesisTime,
		AnchorOrigin:     op.AnchorOrigin,
	}
}

func (r *DocumentHandler) addOperationToUnpublishedOpsStore(unpublishedOp *coreoperation.AnchoredOperation) error {
	if unpublishedOp == nil {
		// nothing to do
		return nil
	}

	return r.unpublishedOperationStore.Put(unpublishedOp)
}

func (r *DocumentHandler) deleteOperationFromUnpublishedOpsStore(unpublishedOp *coreoperation.AnchoredOperation) {
	if unpublishedOp == nil {
		// nothing to do
		return
	}

	err := r.unpublishedOperationStore.Delete(unpublishedOp)
	if err != nil {
		logger.Warn("Failed to delete operation from unpublished store", log.WithError(err))
	}
}

func contains(values []coreoperation.Type, value coreoperation.Type) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}

	return false
}

func (r *DocumentHandler) getCreateResponse(op *coreoperation.Operation, pv coreprotocol.Version) (*document.ResolutionResult, error) {
	startTime := time.Now()

	defer func() {
		r.metrics.GetCreateOperationResultTime(time.Since(startTime))
	}()

	rm, err := docutil.GetCreateResult(op, pv)
	if err != nil {
		return nil, err
	}

	ti := docutil.GetTransformationInfoForUnpublished(r.namespace, r.domain, r.label, op.UniqueSuffix, "")

	return pv.DocumentTransformer().TransformDocument(rm, ti)
}

// ResolveDocument fetches the latest DID Document of a DID. Two forms of string can be passed in the URI:
//
// 1. Standard DID format: did:METHOD:<did-suffix>
//
// 2. Long Form DID format:
// did:METHOD:<did-suffix>:Base64url(JCS({suffix-data-object, delta-object}))
//
// Standard resolution is performed if the DID is found to be registered on the anchoring system.
// If the DID Document cannot be found, the <suffix-data-object> and <delta-object> are used
// to generate and return resolved DID Document. In this case the supplied delta and suffix objects
// are subject to the same validation as during processing create operation.
func (r *DocumentHandler) ResolveDocument(shortOrLongFormDID string,
	opts ...document.ResolutionOption) (*document.ResolutionResult, error) {
	ns, err := r.getNamespace(shortOrLongFormDID)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	pv, err := r.protocol.Current()
	if err != nil {
		return nil, err
	}

	// extract did and optional initial document value
	shortFormDID, createReq, err := pv.OperationParser().ParseDID(ns, shortOrLongFormDID)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	uniquePortion, err := getSuffix(ns, shortFormDID)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	// resolve document from the blockchain
	doc, err := r.resolveRequestWithID(shortFormDID, uniquePortion, pv, opts...)
	if err == nil {
		return doc, nil
	}

	// if document was not found on the blockchain and initial value has been provided resolve using initial value
	if createReq != nil && strings.Contains(err.Error(), "not found") {
		return r.resolveRequestWithInitialState(uniquePortion, shortOrLongFormDID, createReq, pv)
	}

	return nil, err
}

func (r *DocumentHandler) getNamespace(shortOrLongFormDID string) (string, error) {
	// check aliases first (if configured)
	for _, ns := range r.aliases {
		if strings.HasPrefix(shortOrLongFormDID, ns+docutil.NamespaceDelimiter) {
			return ns, nil
		}
	}

	// check namespace
	if strings.HasPrefix(shortOrLongFormDID, r.namespace+docutil.NamespaceDelimiter) {
		return r.namespace, nil
	}

	return "", fmt.Errorf("did must start with configured namespace[%s] or aliases%v", r.namespace, r.aliases)
}

func (r *DocumentHandler) resolveRequestWithID(shortFormDid, uniquePortion string, pv coreprotocol.Version,
	opts ...document.ResolutionOption) (*document.ResolutionResult, error) {
	internalResult, err := r.processor.Resolve(uniquePortion, opts...)
	if err != nil {
		logger.Debug("Failed to resolve uniquePortion", logfields.WithSuffix(uniquePortion), log.WithError(err))

		return nil, err
	}

	var ti coreprotocol.TransformationInfo

	if len(internalResult.PublishedOperations) == 0 {
		hint, err := GetHint(shortFormDid, r.namespace, uniquePortion)
		if err != nil {
			return nil, err
		}

		ti = docutil.GetTransformationInfoForUnpublished(r.namespace, r.domain, hint, uniquePortion, "")
	} else {
		ti = docutil.GetTransformationInfoForPublished(r.namespace, shortFormDid, uniquePortion, internalResult)
	}

	return pv.DocumentTransformer().TransformDocument(internalResult, ti)
}

// GetHint returns hint from id.
func GetHint(id, namespace, suffix string) (string, error) {
	posSuffix := strings.LastIndex(id, suffix)
	if posSuffix == -1 {
		return "", fmt.Errorf("invalid ID [%s]", id)
	}

	if len(namespace)+1 > posSuffix-1 {
		return "", nil
	}

	hint := id[len(namespace)+1 : posSuffix-1]

	return hint, nil
}

func (r *DocumentHandler) resolveRequestWithInitialState(uniqueSuffix, longFormDID string, initialBytes []byte,
	pv protocol.Version) (*document.ResolutionResult, error) {
	op, err := pv.OperationParser().Parse(r.namespace, initialBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", badRequest, err.Error())
	}

	if uniqueSuffix != op.UniqueSuffix {
		return nil, fmt.Errorf("%s: provided did doesn't match did created from initial state", badRequest)
	}

	rm, err := docutil.GetCreateResult(op, pv)
	if err != nil {
		return nil, err
	}

	docBytes, err := canonicalizer.MarshalCanonical(rm.Doc)
	if err != nil {
		return nil, err
	}

	err = pv.DocumentValidator().IsValidOriginalDocument(docBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: validate initial document: %s", badRequest, err.Error())
	}

	createRequestJCS := longFormDID[strings.LastIndex(longFormDID, docutil.NamespaceDelimiter)+1:]

	ti := docutil.GetTransformationInfoForUnpublished(r.namespace, r.domain, r.label, uniqueSuffix, createRequestJCS)

	externalResult, err := pv.DocumentTransformer().TransformDocument(rm, ti)
	if err != nil {
		return nil, fmt.Errorf("failed to transform create with initial state to external document: %s", err.Error())
	}

	return externalResult, nil
}

// helper for adding operations to the batch.
func (r *DocumentHandler) addToBatch(op *coreoperation.Operation, versionTime uint64) error {
	return r.writer.Add(
		&operation.QueuedOperation{
			Type:             op.Type,
			Namespace:        r.namespace,
			UniqueSuffix:     op.UniqueSuffix,
			OperationRequest: op.OperationRequest,
			AnchorOrigin:     op.AnchorOrigin,
			Properties:       op.Properties,
		}, versionTime)
}

func (r *DocumentHandler) validateOperation(op *coreoperation.Operation, pv protocol.Version) error {
	if op.Type == coreoperation.TypeCreate {
		return r.validateCreateDocument(op, pv)
	}

	return pv.DocumentValidator().IsValidPayload(op.OperationRequest)
}

func (r *DocumentHandler) validateCreateDocument(op *coreoperation.Operation, pv protocol.Version) error {
	rm, err := docutil.GetCreateResult(op, pv)
	if err != nil {
		return err
	}

	docBytes, err := canonicalizer.MarshalCanonical(rm.Doc)
	if err != nil {
		return err
	}

	return pv.DocumentValidator().IsValidOriginalDocument(docBytes)
}

// getSuffix fetches unique portion of ID which is string after namespace.
func getSuffix(namespace, idOrDocument string) (string, error) {
	ns := namespace + docutil.NamespaceDelimiter
	pos := strings.Index(idOrDocument, ns)
	if pos == -1 {
		return "", errors.New("did must start with configured namespace")
	}

	lastDelimiter := strings.LastIndex(idOrDocument, docutil.NamespaceDelimiter)

	adjustedPos := lastDelimiter + 1
	if adjustedPos >= len(idOrDocument) {
		return "", errors.New("did suffix is empty")
	}

	return idOrDocument[adjustedPos:], nil
}

type noopUnpublishedOpsStore struct {
}

func (noop *noopUnpublishedOpsStore) Put(_ *coreoperation.AnchoredOperation) error {
	return nil
}

func (noop *noopUnpublishedOpsStore) Delete(_ *coreoperation.AnchoredOperation) error {
	return nil
}

type defaultOperationDecorator struct {
	processor operationProcessor
}

func (d *defaultOperationDecorator) Decorate(op *coreoperation.Operation) (*coreoperation.Operation, error) {
	if op.Type != coreoperation.TypeCreate {
		internalResult, err := d.processor.Resolve(op.UniqueSuffix)
		if err != nil {
			logger.Debug("Failed to resolve suffix for operation", logfields.WithSuffix(op.UniqueSuffix),
				logfields.WithOperationType(string(op.Type)), log.WithError(err))

			return nil, err
		}

		logger.Debug("Processor returned internal result for suffix", logfields.WithSuffix(op.UniqueSuffix),
			logfields.WithOperationType(string(op.Type)), logfields.WithResolutionModel(internalResult))

		if internalResult.Deactivated {
			return nil, fmt.Errorf("document has been deactivated, no further operations are allowed")
		}

		if op.Type == coreoperation.TypeUpdate || op.Type == coreoperation.TypeDeactivate {
			op.AnchorOrigin = internalResult.AnchorOrigin
		}
	}

	return op, nil
}
