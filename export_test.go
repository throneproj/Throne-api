package throne

import (
	"context"
	"time"
)

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

// EventsWait and LogsWait return how long the call that Events and Logs make for q lets
// Throne hold it, which a ctx without a deadline allows for; they send nothing.
func EventsWait(q EventsQuery) time.Duration {
	return callWait(func(a api) { a.Events(context.Background(), q) })
}

func LogsWait(q LogQuery) time.Duration {
	return callWait(func(a api) { a.Logs(context.Background(), q) })
}

func callWait(f func(api)) (wait time.Duration) {
	f(api{func(_ context.Context, r *call, _ any) error {
		wait = r.wait
		return nil
	}})
	return wait
}
