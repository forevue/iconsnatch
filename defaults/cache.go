package defaults

const (
	CacheEnabled  = "enabled"
	CacheDisabled = "disabled"
)

// Default build-time variables.
// These values are overridden by ldflags
var CacheStatus = "disabled"
