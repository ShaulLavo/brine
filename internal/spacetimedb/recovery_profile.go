package spacetimedb

// SupportsConfirmedRecoveryPairs identifies the finite fixture profiles whose
// fixed confirmed-state contract is supported. It grants no source authority.
func SupportsConfirmedRecoveryPairs(artifactWasmSHA256, canonicalSchemaSHA256 string) bool {
	var expected string
	switch artifactWasmSHA256 {
	case "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc":
		expected = "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45"
	case "5f1de728e240ff189fdb47fae0af5f18cfa3c772b86a198220115a6869215ffb":
		expected = "23a8b7ac1656a127dbb519d45a0a35d88a114806e8933b554bede465b589cd7f"
	case "40960700aea4b6dd4d201539e91ff6fc27fcd96c050c92e182bf4c0240a03269":
		expected = "95ac506b0911df63146d79cdba646d1e059e122240c1f0349a34335e9749d07b"
	default:
		return false
	}
	return canonicalSchemaSHA256 == expected
}
