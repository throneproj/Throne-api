package throne

import "time"

// SetPollInterval changes how often RunTest polls until restore is called.
func SetPollInterval(d time.Duration) (restore func()) {
	old := pollInterval
	pollInterval = d
	return func() { pollInterval = old }
}
