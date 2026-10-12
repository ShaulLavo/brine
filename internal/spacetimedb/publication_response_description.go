package spacetimedb

// CheckNativeUpdateResponse checks syntax without manufacturing an opaque
// request receipt, endpoint or one-use transport token.
func CheckNativeUpdateResponse(raw []byte, expected DatabaseIdentity) error {
	_, err := decodeNativePublicationReceipt(raw, expected)
	return err
}

// NativeCreatedDatabase reads the identity in a syntactically valid response.
// It does not prove that a database was actually created.
func NativeCreatedDatabase(raw []byte, name string) (DatabaseIdentity, error) {
	return decodeNativeCreateReceipt(raw, name)
}
