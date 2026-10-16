package throne

import "time"

// SetPollInterval changes how often RunTest, Backup and UploadRestore poll until restore
// is called.
func SetPollInterval(d time.Duration) (restore func()) {
	old := pollInterval
	pollInterval = d
	return func() { pollInterval = old }
}

// SetBackupChunk changes how many bytes Backup asks for at a time until restore is called.
func SetBackupChunk(n int) (restore func()) {
	old := backupChunk
	backupChunk = n
	return func() { backupChunk = old }
}
