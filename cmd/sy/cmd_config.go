package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/paths"
)

// runConfig implements `sy config get <key>` and `sy config set <key> <value...>`.
func runConfig(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { configUsage(stderr) }
	insecure := fs.Bool("insecure-lan", false, "with set hub.url: allow plain http to a non-loopback, non-Tailscale host")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	rest := fs.Args()
	// The flag is also accepted after the key and value, as in `sy connect <url> --insecure-lan`.
	kept := rest[:0:0]
	for _, a := range rest {
		if a == "--insecure-lan" || a == "-insecure-lan" {
			*insecure = true
		} else {
			kept = append(kept, a)
		}
	}
	rest = kept
	if len(rest) == 0 {
		configUsage(stderr)
		return errUsage
	}
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return err
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		return err
	}
	switch rest[0] {
	case "get":
		if len(rest) != 2 {
			configUsage(stderr)
			return errUsage
		}
		v, err := f.Get(rest[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, format(v))
		return nil
	case "set":
		if len(rest) < 3 {
			configUsage(stderr)
			return errUsage
		}
		vals := rest[2:]
		if rest[1] == "hub.url" && len(vals) == 1 && vals[0] != "" {
			// The same check as `sy connect`: no plain http to a non-loopback, non-Tailscale
			// host unless insecure_lan is recorded (now or already in the config).
			already, _ := f.Get("hub.insecure_lan")
			allow, _ := already.(bool)
			norm, warn, err := client.CheckURL(vals[0], *insecure || allow)
			if err != nil {
				return err
			}
			if warn != "" {
				fmt.Fprintf(stderr, "sy config: note: %s\n", warn)
			}
			vals = []string{norm}
			if *insecure && !allow {
				if err := f.Set("hub.insecure_lan", []string{"true"}); err != nil {
					return err
				}
			}
		} else if *insecure {
			fmt.Fprintln(stderr, "sy config: --insecure-lan only means something with `set hub.url`")
			return errUsage
		}
		if err := f.Set(rest[1], vals); err != nil {
			return err
		}
		return f.Save()
	}
	fmt.Fprintf(stderr, "sy config: unknown subcommand %q\n", rest[0])
	configUsage(stderr)
	return errUsage
}

func configUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: sy config get <key>")
	fmt.Fprintln(w, "       sy config set <key> <value>...")
	fmt.Fprintln(w, "       sy config set hub.url <url> [--insecure-lan]   (same URL rules as sy connect)")
	fmt.Fprintln(w, "Keys: "+strings.Join(config.Known(), ", "))
}

// format prints scalars bare and lists/tables as JSON.
func format(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool, int64, int, float64:
		return fmt.Sprint(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
