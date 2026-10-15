package devserver

import (
	"net/http"
	"strconv"
	"time"
)

// GET /v1/analytics/{report} mock (analytics.md): made-up numbers in the hub's shapes, so the
// board's Analytics screen has something to draw. ?days= and ?seat= are honoured (a seat
// scales the numbers down); from/to are not.

func (s *Server) analyticsRoutes() {
	s.mux.HandleFunc("GET /v1/analytics/{report}", s.guard(func(w http.ResponseWriter, r *http.Request, _ *caller) {
		days := 30
		if v := r.URL.Query().Get("days"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 366 {
				writeErr(w, 400, "bad_request", "days must be 1-366")
				return
			}
			days = n
		}
		seat := r.URL.Query().Get("seat")
		scale := 1.0
		if seat != "" {
			scale = 0.4
		}
		now := s.opts.Now().UTC()
		head := func(report string) M {
			var sp any
			if seat != "" {
				sp = seat
			}
			return M{"report": report, "from": now.AddDate(0, 0, 1-days).Format("2006-01-02"), "to": now.Format("2006-01-02"), "tz": "UTC", "seat": sp}
		}
		tok := func(usd float64, replies int) M {
			return M{"input": int(usd * 90000), "output": int(usd * 12000), "cache_5m": int(usd * 30000), "cache_1h": 0, "cache_read": int(usd * 700000),
				"replies": replies, "api_usd": usd, "unpriced_replies": 0}
		}
		with := func(base, extra M) M {
			for k, v := range extra {
				base[k] = v
			}
			return base
		}
		var out M
		switch r.PathValue("report") {
		case "daily":
			var list []M
			total := 0.0
			replies := 0
			for i := 0; i < days; i++ {
				usd := scale * float64((i*37)%23) * 0.9
				n := int(usd * 4)
				total += usd
				replies += n
				d := with(tok(usd, n), M{"day": now.AddDate(0, 0, i+1-days).Format("2006-01-02"), "source": "transcripts"})
				d["total_tokens"] = d["input"].(int) + d["output"].(int) + d["cache_5m"].(int) + d["cache_read"].(int)
				list = append(list, d)
			}
			out = with(head("daily"), M{"totals": tok(total, replies), "days": list, "seats": []M{
				with(tok(total*0.7, replies*7/10), M{"seat": "seat_work", "name": "work"}),
				with(tok(total*0.3, replies*3/10), M{"seat": nil, "name": "Unattributed"})}})
		case "models":
			out = with(head("models"), M{"models": []M{
				with(tok(310*scale, 900), M{"model": "claude-opus-4-6", "label": "Opus 4.6", "priced": true}),
				with(tok(95*scale, 1400), M{"model": "claude-sonnet-5", "label": "Sonnet 5", "priced": true}),
				with(tok(0, 12), M{"model": "mystery-9", "label": "mystery-9", "priced": false})}})
		case "projects":
			out = with(head("projects"), M{"projects": []M{
				with(tok(240*scale, 700), M{"project": "/home/u/switchyard", "name": "switchyard", "sessions": 9, "last_ts": ts(now.Add(-2 * time.Hour))}),
				with(tok(120*scale, 500), M{"project": "/home/u/api", "name": "api", "sessions": 4, "last_ts": ts(now.Add(-30 * time.Hour))})}})
		case "heatmap":
			var usd [7][24]float64
			var replies [7][24]int
			for wd := 1; wd <= 5; wd++ {
				for hr := 9; hr <= 18; hr++ {
					usd[wd][hr] = scale * float64((wd*hr)%11+1)
					replies[wd][hr] = (wd*hr)%11 + 1
				}
			}
			out = with(head("heatmap"), M{"api_usd": usd, "replies": replies})
		case "sessions":
			out = with(head("sessions"), M{"sessions": []M{
				with(tok(80*scale, 200), M{"session_id": "s_1", "seat": "seat_work", "seat_name": "work", "project": "/home/u/switchyard",
					"models": []string{"claude-opus-4-6"}, "first_ts": ts(now.Add(-5 * time.Hour)), "last_ts": ts(now.Add(-time.Hour))})}})
		case "limits":
			out = with(head("limits"), M{"windows": []M{{"seat": "seat_work", "seat_name": "work", "key": "five_hour", "label": "5-hour",
				"started_at": ts(now.Add(-9 * time.Hour)), "resets_at": ts(now.Add(-4 * time.Hour)), "peak_pct": 92.0, "final_pct": 88.0, "unused_pct": 12.0, "samples": 40}}})
		case "hits":
			out = with(head("hits"), M{"hits": []M{{"ts": ts(now.Add(-6 * time.Hour)), "seat": "seat_work", "seat_name": "work", "session_id": "s_1",
				"type": "five_hour", "status": "rejected", "resets_at": ts(now.Add(-4 * time.Hour)), "wait_s": 7200}}})
		default:
			writeErr(w, 404, "not_found", "unknown report")
			return
		}
		writeJSON(w, 200, out)
	}, "read"))
}
