package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/localtime"
)

// `wy analytics [report] [--days N] [--from D] [--to D] [--seat S] [--json]` shows the hub's
// analytics reports (analytics.md) in the terminal. Read only. The numbers, cost included,
// are the hub's: nothing is recomputed here.
//
// The hub's reports need the read scope. This machine's own token (`wy connect`) is a machine
// token with the write scope only (tokens.md 3), so the command uses the board token that
// `wy board connect` saves (a desktop-kind device with read), the same token `wy board` uses.
//
// Exit codes: 0; 1 any other failure (the hub's 400 and 404 text is shown); 2 usage; 3 the hub
// refused the token (401, 403); 4 no hub.url or no read token yet; 5 hub unreachable.

var analyticsReports = []string{"daily", "models", "projects", "heatmap", "sessions", "limits", "hits"}

func runAnalytics(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("analytics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	days := fs.Int("days", 0, "length of the range in days, 1-366 (hub default 30)")
	from := fs.String("from", "", "first day, YYYY-MM-DD (hub time zone)")
	to := fs.String("to", "", "last day, YYYY-MM-DD, inclusive (default today)")
	seat := fs.String("seat", "", "one seat id, or \"unattributed\" (default every seat)")
	asJSON := fs.Bool("json", false, "print the hub's reply as JSON")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: wy analytics [%s] [--days N] [--from D] [--to D] [--seat S] [--json]\n", strings.Join(analyticsReports, "|"))
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	report := "daily"
	if len(pos) > 1 {
		fmt.Fprintf(stderr, "wy analytics: unexpected argument %q\n", pos[1])
		return errUsage
	}
	if len(pos) == 1 {
		report = pos[0]
		if !slices.Contains(analyticsReports, report) {
			fmt.Fprintf(stderr, "wy analytics: unknown report %q (want %s)\n", report, strings.Join(analyticsReports, ", "))
			return errUsage
		}
	}
	if *days < 0 || *days > 366 {
		fmt.Fprintln(stderr, "wy analytics: --days must be 1-366")
		return errUsage
	}
	q := url.Values{}
	if *days > 0 {
		q.Set("days", strconv.Itoa(*days))
	}
	for k, v := range map[string]string{"from": *from, "to": *to, "seat": *seat} {
		if v != "" {
			q.Set(k, v)
		}
	}

	me, err := osMachineEnv()
	if err != nil {
		return err
	}
	f, err := config.Load(me.dirs.Config.Path)
	if err != nil {
		return err
	}
	cfg, err := f.Typed()
	if err != nil {
		return err
	}
	if cfg.Hub.URL == "" {
		return exitWith(4, errors.New("no hub is set (hub.url); connect to one with `wy connect` first"))
	}
	hubURL, _, err := client.CheckURL(cfg.Hub.URL, cfg.Hub.InsecureLAN)
	if err != nil {
		return exitWith(4, fmt.Errorf("hub.url: %v", err))
	}
	tok, err := board.LoadToken(me.dirs.State.Path, cfg.Hub.URL)
	if err != nil {
		if !errors.Is(err, board.ErrNoToken) {
			return err
		}
		return exitWith(4, errors.New("this machine has no token that can read the hub: its own token (wy connect) may only write.\n"+
			"Run `wy board connect` to get a read token, then try again"))
	}
	hub, err := client.ForConfig(cfg, tok.Token)
	if err != nil {
		return fmt.Errorf("hub.dial: %w", err)
	}
	hub.BaseURL = hubURL

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	raw, err := hub.Analytics(ctx, report, q)
	if err != nil {
		return analyticsError(err, hubURL)
	}
	if *asJSON {
		var buf bytes.Buffer
		if json.Indent(&buf, raw, "", "  ") != nil {
			buf.Reset()
			buf.Write(raw)
		}
		buf.WriteByte('\n')
		_, err := stdout.Write(buf.Bytes())
		return err
	}
	return renderAnalytics(stdout, report, raw)
}

// analyticsError turns a client error into the message and exit code described above.
func analyticsError(err error, hubURL string) error {
	var se *client.StatusError
	if !errors.As(err, &se) {
		return exitWith(5, fmt.Errorf("the hub at %s did not answer: %v", hubURL, err))
	}
	switch se.Status {
	case http.StatusUnauthorized:
		return exitWith(3, errors.New("the hub refused the board token (revoked or unknown); run `wy board connect` again"))
	case http.StatusForbidden:
		return exitWith(3, errors.New("the board token has no read scope on this hub; run `wy board connect` again"))
	case http.StatusNotFound:
		msg := se.Message
		if msg == "" {
			msg = "not found"
		}
		return fmt.Errorf("%s (an unknown seat, or a hub too old to serve analytics)", msg)
	}
	return err
}

type anTokens struct {
	Input           int64   `json:"input"`
	Output          int64   `json:"output"`
	Cache5m         int64   `json:"cache_5m"`
	Cache1h         int64   `json:"cache_1h"`
	CacheRead       int64   `json:"cache_read"`
	Replies         int64   `json:"replies"`
	APIUSD          float64 `json:"api_usd"`
	UnpricedReplies int64   `json:"unpriced_replies"`
}

func (t anTokens) total() int64 { return t.Input + t.Output + t.Cache5m + t.Cache1h + t.CacheRead }

type anHead struct {
	From string  `json:"from"`
	To   string  `json:"to"`
	TZ   string  `json:"tz"`
	Seat *string `json:"seat"`
}

func (h anHead) line(report string) string {
	seat := "all seats"
	if h.Seat != nil {
		seat = "seat " + *h.Seat
	}
	return fmt.Sprintf("%s  %s to %s  (%s, %s)", report, h.From, h.To, h.TZ, seat)
}

func renderAnalytics(w io.Writer, report string, raw json.RawMessage) error {
	var head anHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("the hub's reply is not JSON: %w", err)
	}
	fmt.Fprintln(w, head.line(report))
	// Times print in the zone the header names (the hub's, which also cuts the days), with
	// the zone written; --json keeps RFC 3339 UTC.
	loc, lerr := time.LoadLocation(head.TZ)
	if lerr != nil || head.TZ == "" {
		loc = nil // localtime.Zone
	}
	at := func(s string) string { return localtime.StringIn(s, loc) }
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	var err error
	switch report {
	case "daily":
		var r struct {
			Totals anTokens `json:"totals"`
			Days   []struct {
				anTokens
				Day         string `json:"day"`
				Source      string `json:"source"`
				TotalTokens int64  `json:"total_tokens"`
			} `json:"days"`
			Seats []struct {
				anTokens
				Name string `json:"name"`
			} `json:"seats"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		fmt.Fprintf(w, "total  %s replies, %s tokens (in %s, out %s, cache write %s, cache read %s), %s API-equivalent\n",
			num(r.Totals.Replies), human(r.Totals.total()), human(r.Totals.Input), human(r.Totals.Output),
			human(r.Totals.Cache5m+r.Totals.Cache1h), human(r.Totals.CacheRead), usd(r.Totals.APIUSD))
		if r.Totals.UnpricedReplies > 0 {
			fmt.Fprintf(w, "       %d replies of unpriced models are not in the cost\n", r.Totals.UnpricedReplies)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(tw, "DAY\tREPLIES\tTOKENS\tINPUT\tOUTPUT\tCACHE W\tCACHE R\tAPI USD")
		for _, d := range r.Days {
			if d.Source == "history" { // only the total and the message count are known
				fmt.Fprintf(tw, "%s\t%s\t%s\t-\t-\t-\t-\t-\n", d.Day, num(d.Replies), human(d.TotalTokens))
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Day, num(d.Replies), human(d.total()),
				human(d.Input), human(d.Output), human(d.Cache5m+d.Cache1h), human(d.CacheRead), usd(d.APIUSD))
		}
		if len(r.Seats) > 0 {
			fmt.Fprintln(tw)
			fmt.Fprintln(tw, "SEAT\tREPLIES\tTOKENS\tAPI USD")
			for _, s := range r.Seats {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, num(s.Replies), human(s.total()), usd(s.APIUSD))
			}
		}
	case "models":
		var r struct {
			Models []struct {
				anTokens
				Model  string `json:"model"`
				Label  string `json:"label"`
				Priced bool   `json:"priced"`
			} `json:"models"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		fmt.Fprintln(tw, "MODEL\tREPLIES\tTOKENS\tAPI USD")
		for _, m := range r.Models {
			name := m.Label
			if name == "" {
				name = "(unknown)"
			}
			cost := usd(m.APIUSD)
			if !m.Priced {
				cost = "unpriced"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, num(m.Replies), human(m.total()), cost)
		}
	case "projects":
		var r struct {
			Projects []struct {
				anTokens
				Name     string  `json:"name"`
				Sessions int     `json:"sessions"`
				LastTS   *string `json:"last_ts"`
			} `json:"projects"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		fmt.Fprintln(tw, "PROJECT\tSESSIONS\tREPLIES\tTOKENS\tAPI USD\tLAST")
		for _, p := range r.Projects {
			last := "-"
			if p.LastTS != nil {
				last = at(*p.LastTS)
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", p.Name, p.Sessions, num(p.Replies), human(p.total()), usd(p.APIUSD), last)
		}
	case "sessions":
		var r struct {
			Sessions []struct {
				anTokens
				SessionID string `json:"session_id"`
				SeatName  string `json:"seat_name"`
				Project   string `json:"project"`
				LastTS    string `json:"last_ts"`
			} `json:"sessions"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		fmt.Fprintln(tw, "SESSION\tSEAT\tPROJECT\tREPLIES\tTOKENS\tAPI USD\tLAST")
		ids := make([]string, len(r.Sessions))
		for i, s := range r.Sessions {
			ids[i] = s.SessionID
		}
		short := shortIDs(ids, 8)
		for i, s := range r.Sessions {
			id := short[i]
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, s.SeatName, baseName(s.Project), num(s.Replies), human(s.total()), usd(s.APIUSD), at(s.LastTS))
		}
	case "limits":
		var r struct {
			Windows []struct {
				SeatName  string   `json:"seat_name"`
				Label     string   `json:"label"`
				ResetsAt  string   `json:"resets_at"`
				PeakPct   *float64 `json:"peak_pct"`
				FinalPct  *float64 `json:"final_pct"`
				UnusedPct *float64 `json:"unused_pct"`
			} `json:"windows"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		fmt.Fprintln(tw, "SEAT\tWINDOW\tRESETS\tPEAK\tFINAL\tUNUSED")
		for _, x := range r.Windows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.SeatName, x.Label, at(x.ResetsAt), pct(x.PeakPct), pct(x.FinalPct), pct(x.UnusedPct))
		}
	case "hits":
		var r struct {
			Hits []struct {
				TS       string  `json:"ts"`
				SeatName string  `json:"seat_name"`
				Type     *string `json:"type"`
				ResetsAt *string `json:"resets_at"`
				WaitS    *int64  `json:"wait_s"`
			} `json:"hits"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		fmt.Fprintln(tw, "WHEN\tSEAT\tLIMIT\tRESETS\tWAIT")
		for _, h := range r.Hits {
			typ, resets, wait := "-", "-", "-"
			if h.Type != nil {
				typ = *h.Type
			}
			if h.ResetsAt != nil {
				resets = at(*h.ResetsAt)
			}
			if h.WaitS != nil {
				wait = secondsDuration(*h.WaitS)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", at(h.TS), h.SeatName, typ, resets, wait)
		}
	case "heatmap":
		var r struct {
			APIUSD [][]float64 `json:"api_usd"`
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		renderHeatmap(w, r.APIUSD)
	}
	if err != nil {
		return fmt.Errorf("the hub's %s reply did not parse: %w", report, err)
	}
	return tw.Flush()
}

// renderHeatmap prints 7 weekdays by 24 hours as shade characters, the dearest cell darkest.
func renderHeatmap(w io.Writer, cells [][]float64) {
	shades := []rune(" ░▒▓█")
	hi := 0.0
	for _, row := range cells {
		for _, v := range row {
			if v > hi {
				hi = v
			}
		}
	}
	var ruler strings.Builder
	for h := 0; h < 24; h += 3 {
		fmt.Fprintf(&ruler, "%-6d", h)
	}
	fmt.Fprintln(w, "     "+strings.TrimRight(ruler.String(), " "))
	days := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	for i, row := range cells {
		var b strings.Builder
		for _, v := range row {
			n := 0
			if v > 0 && hi > 0 {
				n = 1 + int(v/hi*float64(len(shades)-2)+0.5)
				if n > len(shades)-1 {
					n = len(shades) - 1
				}
			}
			b.WriteRune(shades[n])
			b.WriteRune(shades[n])
		}
		name := ""
		if i < len(days) {
			name = days[i]
		}
		fmt.Fprintf(w, "%-4s %s\n", name, b.String())
	}
	fmt.Fprintf(w, "\nshade = API-equivalent cost per hour (hub time zone); darkest cell %s\n", usd(hi))
}

// human is a count as 1.2M, 34.5k or 812.
func human(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

func num(n int64) string { return strconv.FormatInt(n, 10) }

func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }

func pct(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		return "(unknown)"
	}
	return p
}

func secondsDuration(s int64) string {
	switch {
	case s >= 3600:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%ds", s)
}

// shortIDs cuts each id to min characters, longer where two would read the same, so every
// row's prefix still names one session (wy resume and the board take any unique prefix).
func shortIDs(ids []string, min int) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		n := min
		for ; n < len(id); n++ {
			clash := false
			for j, o := range ids {
				if j != i && o != id && len(o) >= n && o[:n] == id[:n] {
					clash = true
					break
				}
			}
			if !clash {
				break
			}
		}
		if n > len(id) {
			n = len(id)
		}
		out[i] = id[:n]
	}
	return out
}
