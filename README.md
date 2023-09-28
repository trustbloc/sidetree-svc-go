# sidetree-svc-go

This library implements components of a service conforming to the [Sidetree Protocol](https://github.com/decentralized-identity/sidetree/blob/master/docs/protocol.md>).

Below are the main components:

## Batch Writer
The batch writer batches multiple document operations(create, update, delete, recover) in a single batch file. Batch files are stored in a distributed content-addressable storage (DCAS or CAS). A reference to the operation batch is then anchored on a blockchain as a Sidetree transaction.

## Operation Processor
All document processing is deferred to resolution time. Resolution of the given ID to its document is done by iterating over all operations in blockchain-time order (starts with ‘create’). Each operation is checked for validity before applying a JSON patch to document.

## Document Handler
The document handler performs document operation processing and document resolution. It supports both DID documents and generic documents.

### Operation Processing
Upon successful validation against a configured validator, an operation will be added to the batch.

### Resolution
Document resolution is based on ID or initial state values.

- DID: The latest document is returned if found.

- Long Form DID: Can be requested in the following format:
did:METHOD:<unique-portion>:Base64url(JCS({suffix-data, delta}))

## Contributing
Thank you for your interest in contributing. Please see our [community contribution guidelines](https://github.com/trustbloc/community/blob/main/CONTRIBUTING.md) for more information.

## License
Apache License, Version 2.0 (Apache-2.0). See the [LICENSE](LICENSE) file.
