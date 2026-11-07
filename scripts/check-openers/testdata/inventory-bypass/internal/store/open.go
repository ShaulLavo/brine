package store

func OpenReadOnly()       {}
func ReadInventoryState() { OpenReadOnly() }
func ReadGeneration()     { ReadInventoryState() }
func ForwardedInventory() { ReadGeneration() }
