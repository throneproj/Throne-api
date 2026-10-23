package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	throne "github.com/throneproj/Throne-api"
)

const (
	// followWait is how long Throne may hold a request of logs -f.
	followWait = 25 * time.Second
	// maxLogLines is the most lines Throne returns at a time.
	maxLogLines = 1000
)

// followRetry is how often logs -f tries again after a request failed.
var followRetry = 2 * time.Second

// untilInterrupted returns a context that ends with SIGINT or SIGTERM, for commands that
// run until interrupted.
var untilInterrupted = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}

func cmdLogs(c *cli, fs *flag.FlagSet, args []string) error {
	n := fs.Int("n", 200, "show the last `N` lines, 1 to 1000")
	follow := fs.Bool("f", false, "then print new lines as Throne logs them, until interrupted")
	if _, err := c.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if *n < 1 || *n > maxLogLines {
		return c.usageErr("-n takes 1 to %d, not %d", maxLogLines, *n)
	}
	cl, ctx, err := c.open()
	if err != nil {
		return err
	}
	if *follow {
		return c.followLogs(cl, *n)
	}
	logs, err := cl.Logs(ctx, throne.LogQuery{Limit: *n})
	if err != nil {
		return err
	}
	return c.writeLogs(logs.Lines)
}

// followLogs prints the last n lines, then the lines that follow, until interrupted.
// Once the first lines are out, it carries on across a lost connection and a restarting Throne.
func (c *cli) followLogs(cl *throne.Client, n int) error {
	ctx, stop := untilInterrupted(context.Background())
	defer stop()
	var s *throne.Session
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	q := throne.LogQuery{Limit: n}
	started, failing := false, false
	for {
		logs, err := c.logsPage(ctx, cl, &s, q)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && (!started || !followRetryable(err)):
			return err
		case err != nil:
			if !failing {
				fmt.Fprintf(c.stderr, "thronectl: %s; trying again every %v\n", clean(followErrorText(err)), followRetry)
				failing = true
			}
			if followPause(ctx, followRetry) != nil {
				return nil
			}
			continue
		}
		failing = false
		dropped := logs.Dropped
		if started && logs.Instance != q.Instance {
			fmt.Fprintln(c.stderr, "thronectl: Throne restarted")
			if len(logs.Lines) > 0 {
				dropped = logs.Lines[0].Seq - 1
			}
		}
		if dropped == 1 {
			fmt.Fprintln(c.stderr, "thronectl: 1 line was dropped")
		} else if dropped > 1 {
			fmt.Fprintf(c.stderr, "thronectl: %d lines were dropped\n", dropped)
		}
		if err := c.writeLogs(logs.Lines); err != nil {
			return err
		}
		started = true
		q = throne.LogQuery{Instance: logs.Instance, After: logs.Cursor, Limit: maxLogLines, Wait: followWait}
	}
}

// logsPage asks for a page of the log on the Session in *s, which it opens first when there
// is none and drops after a failed exchange. --timeout bounds each request on top of the
// time Throne may hold it.
func (c *cli) logsPage(ctx context.Context, cl *throne.Client, s **throne.Session, q throne.LogQuery) (*throne.Logs, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Wait+c.timeout)
		defer cancel()
	}
	if *s == nil {
		var err error
		if *s, err = cl.Dial(ctx); err != nil {
			return nil, err
		}
	}
	logs, err := (*s).Logs(ctx, q)
	var apiErr *throne.APIError
	if err != nil && !errors.As(err, &apiErr) {
		(*s).Close()
		*s = nil
	}
	return logs, err
}

// followRetryable says whether logs -f tries again after err: after any error but a
// response of Throne's other than a 503, which a restarting Throne gives.
func followRetryable(err error) bool {
	var apiErr *throne.APIError
	return !errors.As(err, &apiErr) || apiErr.Status == 503
}

func followErrorText(err error) string {
	var apiErr *throne.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code + ": " + apiErr.Message
	}
	return strings.TrimPrefix(err.Error(), "throne: ")
}

// followPause waits d, or returns ctx's error when ctx ends first.
func followPause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// logEscape matches the escape sequences that color the core's output.
var logEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// writeLogs prints lines of the log: their text, or with --json each line as a JSON object.
func (c *cli) writeLogs(lines []throne.LogLine) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, l := range lines {
		if c.json {
			if err := enc.Encode(l); err != nil {
				return err
			}
			continue
		}
		b.WriteString(clean(logEscape.ReplaceAllString(l.Text, "")))
		b.WriteByte('\n')
	}
	_, err := c.stdout.Write(b.Bytes())
	return err
}
