package spacetimedb

// Publication response descriptions check native syntax only. They cannot
// manufacture an opaque request receipt, endpoint or one-use transport token.
func CheckNativeUpdateResponse(raw []byte, expected DatabaseIdentity) error {
	_, err := decodeNativePublicationReceipt(raw, expected)
	return err
}

func NativeCreatedDatabase(raw []byte, name string) (DatabaseIdentity, error) {
	return decodeNativeCreateReceipt(raw, name)
}
