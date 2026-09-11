#!/usr/bin/env python3
"""Fetch last values from InfluxDB v1 and write to gofutura.

Queries are based on the user's existing InfluxQL patterns and then the
last non-null value per MAC is written to gofutura external sensor fields.
"""

from __future__ import annotations

import argparse
import time
import json
import sys
from typing import Dict, Any, Optional
import base64
from urllib.parse import urlencode
from urllib.request import Request, urlopen
from urllib.error import URLError
import socket
import yaml


def load_config(config_file: str) -> Dict[str, Any]:
    """Load payload key mappings and runtime settings from a YAML config file."""
    with open(config_file, "r", encoding="utf-8") as f:
        config = yaml.safe_load(f) or {}

    return {
        "payload_key_to_influx": config.get("payload_key_to_influx", {}),
        "settings": config.get("settings", {}),
    }


def get_setting(settings: Dict[str, Any], *keys: str, default: Optional[Any] = None) -> Any:
    """Return the first matching setting from the YAML config."""
    for key in keys:
        if key in settings and settings[key] is not None:
            return settings[key]
    return default

def influx_query(base_url: str, db: str, query: str, user: Optional[str], password: Optional[str]) -> Dict[str, Any]:
    params = {"db": db, "q": query, "epoch": "ms"}
    url = base_url.rstrip("/") + "/query?" + urlencode(params)
    headers = {}
    if user or password:
        raw = f"{user or ''}:{password or ''}".encode("utf-8")
        headers["Authorization"] = "Basic " + base64.b64encode(raw).decode("ascii")
    req = Request(url, headers=headers)
    with urlopen(req, timeout=15) as resp:
        data = json.loads(resp.read().decode("utf-8"))

    if "error" in data:
        raise RuntimeError(f"InfluxDB error: {data['error']}")
    return data


def post_gofutura(base_url: str, payload: Dict[str, float], dry_run: bool) -> Dict[str, Any]:
    if dry_run:
        return {"success": True, "dry_run": True, "payload": payload}

    url = base_url.rstrip("/") + "/api/write-holding"
    body = json.dumps(payload).encode("utf-8")
    req = Request(url, data=body, headers={"Content-Type": "application/json"})
    with urlopen(req, timeout=15) as resp:
        return json.loads(resp.read().decode("utf-8"))


def main() -> int:
    parser = argparse.ArgumentParser(description="Copy last InfluxDB values to gofutura external sensors")
    parser.add_argument("--config", default="influx_to_gofutura.yaml", help="YAML config file with payload key to query mapping and runtime settings")
    parser.add_argument("--influx-url", default=None, help="InfluxDB base URL, e.g. http://localhost:8086")
    parser.add_argument("--db", default=None, help="InfluxDB database name")
    parser.add_argument("--user", default=None, help="InfluxDB username")
    parser.add_argument("--password", default=None, help="InfluxDB password")
    parser.add_argument("--gofutura-url", default=None, help="gofutura base URL, e.g. http://localhost:9090")
    parser.add_argument("--interval-seconds", type=int, default=None, help="Polling interval in seconds")
    parser.add_argument("--dry-run", action="store_true", default=None, help="Do not write to gofutura, just print payload")

    args = parser.parse_args()

    # Load config from YAML file
    try:
        config_data = load_config(args.config)
    except FileNotFoundError:
        print(f"Config file not found: {args.config}", file=sys.stderr)
        return 2
    except Exception as exc:
        print(f"Error loading config file: {exc}", file=sys.stderr)
        return 2

    payload_key_to_influx = config_data["payload_key_to_influx"]
    settings = config_data["settings"]

    influx_url = args.influx_url if args.influx_url is not None else get_setting(settings, "influx_url", "influx-url")
    db_name = args.db if args.db is not None else get_setting(settings, "db")
    user = args.user if args.user is not None else get_setting(settings, "user")
    password = args.password if args.password is not None else get_setting(settings, "password")
    gofutura_url = args.gofutura_url if args.gofutura_url is not None else get_setting(settings, "gofutura_url", "gofutura-url")
    interval_seconds = args.interval_seconds if args.interval_seconds is not None else get_setting(settings, "interval_seconds", "interval-seconds", default=30)
    dry_run = args.dry_run if args.dry_run is not None else bool(get_setting(settings, "dry_run", "dry-run", default=False))

    if not payload_key_to_influx:
        print("payload_key_to_influx is empty in config file. Fill the mapping in " + args.config, file=sys.stderr)
        return 2

    if not influx_url:
        print("InfluxDB URL is missing. Provide --influx-url or set influx_url in the YAML config.", file=sys.stderr)
        return 2
    if not db_name:
        print("InfluxDB database name is missing. Provide --db or set db in the YAML config.", file=sys.stderr)
        return 2
    if not gofutura_url:
        print("gofutura URL is missing. Provide --gofutura-url or set gofutura_url in the YAML config.", file=sys.stderr)
        return 2
    if interval_seconds <= 0:
        print("--interval-seconds must be > 0", file=sys.stderr)
        return 2

    while True:
        try:
            payload: Dict[str, float] = {}

            for payload_key, config in payload_key_to_influx.items():
                query = config.get("query")
                if not query:
                    print(f"[{payload_key}] Missing query in config; skipping.", file=sys.stderr)
                    continue

                try:
                    result = influx_query(influx_url, db_name, query, user, password)
                except Exception as exc:
                    print(f"[{payload_key}] Influx query failed: {exc}\nQuery: {query}", file=sys.stderr)
                    continue

                results = result.get("results") or [{}]
                series_list = []
                for item in results:
                    if isinstance(item, dict):
                        series_list.extend(item.get("series", []) or [])

                if not series_list:
                    print(f"[{payload_key}] Query returned no series.\nQuery: {query}", file=sys.stderr)
                    continue

                values = series_list[0].get("values", []) or []
                if not values or len(values[0]) < 2:
                    print(f"[{payload_key}] Query returned a series but no values.\nQuery: {query}", file=sys.stderr)
                    continue

                value = values[0][1]
                if value is None:
                    print(f"[{payload_key}] Query returned a null value.\nQuery: {query}", file=sys.stderr)
                    continue

                payload[payload_key] = float(value)
                print(f"[{payload_key}] OK: {value}")

            if not payload:
                print("No values found for configured payload keys; nothing to write.")
            else:
                for key, value in payload.items():
                    result = post_gofutura(gofutura_url, {key: value}, dry_run)
                    print(json.dumps(result, indent=2, sort_keys=True))
        except (URLError, socket.gaierror) as exc:
            print(f"Connection error: {exc}. Retrying in {interval_seconds}s...", file=sys.stderr)
        except Exception as exc:
            print(f"Unexpected error: {exc}. Retrying in {interval_seconds}s...", file=sys.stderr)

        time.sleep(interval_seconds)


if __name__ == "__main__":
    raise SystemExit(main())
