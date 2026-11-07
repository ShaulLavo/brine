package inventory

import control "github.com/ShaulLavo/brine/internal/store"

func inventoryBypass() { control.ReadInventoryState() }

var generationBypass = control.ReadGeneration
var forwardedBypass = control.ForwardedInventory
