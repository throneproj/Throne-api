package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/web"
	"golang.org/x/term"
)

// webContext returns the context that ends thronectl web: on SIGINT or SIGTERM.
var webContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// readHidden reads a line from the terminal r without echoing it.
var readHidden = func(r io.Reader) (string, error) {
	f, ok := r.(*os.File)
	if !ok {
		return "", errors.New("standard input is not a terminal")
	}
	b, err := term.ReadPassword(int(f.Fd()))
	return string(b), err
}

type webFlags struct {
	listen, stateDir, tls, cert, key, allow, passwordFile string
}

func cmdWeb(c *cli, fs *flag.FlagSet, args []string) error {
	var f webFlags
	fs.StringVar(&f.listen, "listen", "", "listen on `ADDR`: PORT, IP:PORT, [IPv6]:PORT, or *:PORT for every address "+
		"(default 127.0.0.1:9096; $THRONE_WEB_LISTEN)")
	fs.StringVar(&f.stateDir, "state-dir", "", "keep the password hash and the TLS certificate in `DIR` (default "+
		"$STATE_DIRECTORY, else thronectl/web in the user configuration directory; $THRONE_WEB_STATE_DIR)")
	fs.StringVar(&f.tls, "tls", "", "serve HTTPS: `MODE` auto, on or off; auto is on unless the listen address is a "+
		"loopback one (default auto; $THRONE_WEB_TLS)")
	fs.StringVar(&f.cert, "tls-cert", "", "TLS certificate chain `FILE` in PEM, with --tls-key, instead of a self-signed "+
		"certificate ($THRONE_WEB_TLS_CERT)")
	fs.StringVar(&f.key, "tls-key", "", "TLS private key `FILE` in PEM ($THRONE_WEB_TLS_KEY)")
	fs.StringVar(&f.allow, "allow-host", "", "also accept these host `names`, separated by commas or spaces, in the Host "+
		"header; on a network address only they, loopback names and IP addresses are then accepted ($THRONE_WEB_ALLOW_HOST)")
	fs.StringVar(&f.passwordFile, "password-file", "", "take the password in plain text from `FILE`, such as a Docker "+
		"secret, instead of the stored hash ($THRONE_WEB_PASSWORD_FILE)")
	pos, err := c.parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		if pos[0] != "passwd" {
			return c.usageErr("unexpected argument %q: the only subcommand is passwd", pos[0])
		}
		var other []string
		fs.Visit(func(fl *flag.Flag) {
			if fl.Name != "state-dir" && !globalFlags[fl.Name] {
				other = append(other, "--"+fl.Name)
			}
		})
		if len(other) > 0 {
			return c.usageErr("%s does not go with passwd", strings.Join(other, ", "))
		}
		return c.webPasswd(f.stateDir)
	}
	key, err := c.key()
	if err != nil {
		return err
	}
	cfg, err := c.webConfig(f)
	if err != nil {
		return err
	}
	cfg.Throne, cfg.ThroneAddr, cfg.Version = throne.New(c.addr, key), c.addr, versionString()
	var mu sync.Mutex
	cfg.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(c.stderr, "thronectl web: "+format+"\n", args...)
	}
	srv, err := web.New(cfg)
	var cerr *web.ConfigError
	if errors.As(err, &cerr) {
		return c.usageErr("%s", cerr.Msg)
	}
	if err != nil {
		return err
	}
	ctx, stop := webContext()
	defer stop()
	if err := srv.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("%w; --listen picks another address", err)
	}
	return nil
}

// webSetting returns a flag's value, else the environment variable's, and where it came
// from.
func (c *cli) webSetting(value, flagName, env string) (string, string) {
	if value != "" {
		return value, "--" + flagName
	}
	return c.getenv(env), env
}

// webConfig resolves the flags and the environment into a Config, without the client.
func (c *cli) webConfig(f webFlags) (web.Config, error) {
	var cfg web.Config
	listen, origin := c.webSetting(f.listen, "listen", "THRONE_WEB_LISTEN")
	if listen == "" {
		listen = web.DefaultListen
	}
	addr, loopback, err := web.ParseListen(listen)
	if err != nil {
		return cfg, c.usageErr("%s: %v", origin, err)
	}
	cfg.Listen = addr
	if cfg.StateDir, err = c.webStateDir(f.stateDir); err != nil {
		return cfg, err
	}
	cert, certOrigin := c.webSetting(f.cert, "tls-cert", "THRONE_WEB_TLS_CERT")
	key, keyOrigin := c.webSetting(f.key, "tls-key", "THRONE_WEB_TLS_KEY")
	if (cert == "") != (key == "") {
		if cert == "" {
			return cfg, c.usageErr("%s needs a certificate too: --tls-cert or THRONE_WEB_TLS_CERT", keyOrigin)
		}
		return cfg, c.usageErr("%s needs a key too: --tls-key or THRONE_WEB_TLS_KEY", certOrigin)
	}
	mode, modeOrigin := c.webSetting(f.tls, "tls", "THRONE_WEB_TLS")
	switch strings.ToLower(mode) {
	case "", "auto":
		cfg.TLS = !loopback || cert != ""
	case "on":
		cfg.TLS = true
	case "off":
		if cert != "" {
			return cfg, c.usageErr("%s gives a certificate, but %s turns TLS off", certOrigin, modeOrigin)
		}
	default:
		return cfg, c.usageErr("%s: want auto, on or off, not %q", modeOrigin, mode)
	}
	cfg.TLSCert, cfg.TLSKey = cert, key
	allow, _ := c.webSetting(f.allow, "allow-host", "THRONE_WEB_ALLOW_HOST")
	cfg.AllowHosts = strings.FieldsFunc(allow, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	cfg.PasswordFile, _ = c.webSetting(f.passwordFile, "password-file", "THRONE_WEB_PASSWORD_FILE")
	return cfg, nil
}

// webStateDir is --state-dir, else $THRONE_WEB_STATE_DIR, else systemd's
// $STATE_DIRECTORY, else thronectl/web in the user configuration directory.
func (c *cli) webStateDir(flagValue string) (string, error) {
	dir, _ := c.webSetting(flagValue, "state-dir", "THRONE_WEB_STATE_DIR")
	if dir == "" {
		if list := filepath.SplitList(c.getenv("STATE_DIRECTORY")); len(list) > 0 {
			dir = list[0]
		}
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", c.usageErr("no state directory: pass --state-dir (%v)", err)
		}
		dir = filepath.Join(base, "thronectl", "web")
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir, nil
}

// webPasswd sets the web UI password: read twice without echo on a terminal, else one
// line of standard input.
func (c *cli) webPasswd(stateDirFlag string) error {
	if name := c.getenv("THRONE_WEB_PASSWORD_FILE"); name != "" {
		return c.usageErr("THRONE_WEB_PASSWORD_FILE is set, so the web UI takes its password from %s: change that "+
			"file instead", name)
	}
	dir, err := c.webStateDir(stateDirFlag)
	if err != nil {
		return err
	}
	var password string
	if isTerminal(c.stdin) {
		fmt.Fprint(c.stderr, "New web UI password: ")
		password, err = readHidden(c.stdin)
		fmt.Fprintln(c.stderr)
		if err != nil {
			return err
		}
		if err := web.CheckPassword(password); err != nil {
			return c.usageErr("%v", err)
		}
		fmt.Fprint(c.stderr, "Repeat it: ")
		again, err := readHidden(c.stdin)
		fmt.Fprintln(c.stderr)
		if err != nil {
			return err
		}
		if again != password {
			return c.usageErr("the passwords differ")
		}
	} else {
		line, err := bufio.NewReader(c.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read standard input: %w", err)
		}
		line = strings.TrimSuffix(line, "\n")
		password = strings.TrimSuffix(line, "\r")
	}
	if err := web.CheckPassword(password); err != nil {
		return c.usageErr("%v", err)
	}
	var cerr *web.ConfigError
	if err := web.StorePassword(dir, password); errors.As(err, &cerr) {
		return c.usageErr("%s", cerr.Msg)
	} else if err != nil {
		return fmt.Errorf("save the password: %w", err)
	}
	fmt.Fprintf(c.stderr, "thronectl web: saved the new password in %s; a running thronectl web signs every session "+
		"out within 5 seconds\n", dir)
	return nil
}
