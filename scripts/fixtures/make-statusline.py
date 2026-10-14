#!/usr/bin/env python3
"""Write SYNTHETIC status line stdin fixtures (from the documented schema; not captured).
usage: python3 -I make-statusline.py <out dir>
Headless `claude -p` never runs the status line, and an interactive capture needs a trusted folder
and a real turn, so these are built from the documented fields (and the fields the user's own
statusline script reads). Values are invented; ids are fake.
"""
import json, os, sys
out = sys.argv[1]; os.makedirs(out, exist_ok=True)
sid = "11111111-2222-4333-8444-555555555555"
base = {
    "session_id": sid,
    "transcript_path": f"/home/user/.claude-2/projects/-home-user-projects-demo/{sid}.jsonl",
    "cwd": "/home/user/projects/demo",
    "model": {"id": "claude-opus-5-5", "display_name": "Opus 5.5"},
    "workspace": {"current_dir": "/home/user/projects/demo", "project_dir": "/home/user/projects/demo"},
    "version": "2.1.296",
    "output_style": {"name": "default"},
    "effort": {"level": "high"},
    "cost": {"total_cost_usd": 0.4172, "total_duration_ms": 245000, "total_api_duration_ms": 61000,
             "total_lines_added": 42, "total_lines_removed": 7},
    "exceeds_200k_tokens": False,
    "context_window": {"total_input_tokens": 18500, "total_output_tokens": 4200, "context_window_size": 200000,
                       "used_percentage": 9, "remaining_percentage": 91,
                       "current_usage": {"input_tokens": 8, "output_tokens": 312,
                                         "cache_creation_input_tokens": 1500, "cache_read_input_tokens": 16000}},
    "rate_limits": {"five_hour": {"used_percentage": 23.5, "resets_at": 1791824400},
                    "seven_day": {"used_percentage": 41.2, "resets_at": 1792256400}},
}
full = json.loads(json.dumps(base)); full["vim"] = {"mode": "NORMAL"}
early = json.loads(json.dumps(base))   # first render of a session: no usage yet, no rate limits (API-key / not yet fetched)
early["cost"] = {"total_cost_usd": 0, "total_duration_ms": 0, "total_api_duration_ms": 0, "total_lines_added": 0, "total_lines_removed": 0}
early["context_window"].update({"total_input_tokens": 0, "total_output_tokens": 0, "used_percentage": None, "remaining_percentage": None, "current_usage": None})
del early["rate_limits"], early["effort"]
over = json.loads(json.dumps(base)); over["exceeds_200k_tokens"] = True
over["context_window"].update({"context_window_size": 1000000, "used_percentage": 21, "remaining_percentage": 79, "total_input_tokens": 210000})
over["rate_limits"]["five_hour"] = {"used_percentage": 100, "resets_at": 1791824400}
over["model"] = {"id": "claude-sonnet-5-5", "display_name": "Sonnet 5.5"}
for name, d in (("full", full), ("early-session", early), ("limit-reached-1m-context", over)):
    json.dump(d, open(f"{out}/{name}.json", "w"), indent=2); open(f"{out}/{name}.json", "a").write("\n")
