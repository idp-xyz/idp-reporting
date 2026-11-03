package pdf

import "idp.local/reporting/doctemplates"

// The A4 safe region, from the preset table, as the tests written before page presets name it.
var (
	bandX      = doctemplates.DefaultPreset.Left
	safeTop    = doctemplates.DefaultPreset.Top
	safeBottom = doctemplates.DefaultPreset.Bottom
)
