package data

// PrepareSpacetimeDirectory allocates only the native binding's private empty
// hierarchy. It creates no SQLite descriptor, database file, or native process.
func PrepareSpacetimeDirectory(binding SpacetimeDBBinding) (RootEvidence, error) {
	if binding.Validate() != nil {
		return RootEvidence{}, ErrInvalid
	}
	return prepareOwnedDirectory(binding.Root, binding.RelativeDirectory)
}

func CaptureSpacetimeAllocation(binding SpacetimeDBBinding) (AllocationReceipt, error) {
	if binding.Validate() != nil {
		return AllocationReceipt{}, ErrInvalid
	}
	return captureDirectoryAllocation(binding.Root, binding.RelativeDirectory, binding.IncarnationID, binding.DatabaseID)
}
