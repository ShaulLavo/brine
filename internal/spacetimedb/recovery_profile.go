package spacetimedb

// SupportsConfirmedRecoveryPairs identifies the finite fixture profiles whose
// fixed confirmed-state contract is supported. It grants no source authority.
func SupportsConfirmedRecoveryPairs(artifactWasmSHA256, canonicalSchemaSHA256 string) bool {
	var expected string
	switch artifactWasmSHA256 {
	case "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc",
		"5f27a1c57f24e1d682233dc122ed85b041826a081cd2bcfc428217be18722bd8",
		"7cc9e994314a20c0a38f6b3cfc72c2ab937fcd05d47ae6ff1c6a258f7457e3db":
		expected = "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45"
	case "5f1de728e240ff189fdb47fae0af5f18cfa3c772b86a198220115a6869215ffb",
		"95e91b07e63ec82ac66528091bc245dfd10f94da69186eb90bfefc4ef4b386eb":
		expected = "23a8b7ac1656a127dbb519d45a0a35d88a114806e8933b554bede465b589cd7f"
	case "40960700aea4b6dd4d201539e91ff6fc27fcd96c050c92e182bf4c0240a03269",
		"354d3d4a607063d08956c84ba476589814861eedcabf6b762cd38b457eac9852":
		expected = "95ac506b0911df63146d79cdba646d1e059e122240c1f0349a34335e9749d07b"
	default:
		return false
	}
	return canonicalSchemaSHA256 == expected
}
