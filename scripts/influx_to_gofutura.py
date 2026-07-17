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

# Map payload key -> InfluxDB query configuration.
# Each key maps to: full InfluxQL query text.
# Example:
# PAYLOAD_KEY_TO_INFLUX = {
#     "ExtSensTemp1": {
#         "query": 'SELECT last("temperature") FROM "atc_thermometer" WHERE "mac" = \'AA:BB:CC:DD:EE:FF\' AND time > now() - 1d',
#     },
# }
PAYLOAD_KEY_TO_INFLUX: Dict[str, Dict[str, str]] = {
    "ExtSensTemp1": {
        "query": 'SELECT last("temperature") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:cb:ca:c0\' AND time > now() - 1d',
    },
    "ExtSensRH1": {
        "query": 'SELECT last("humidity") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:cb:ca:c0\' AND time > now() - 1d',
    },
    "ExtSensTemp2": {
        "query": 'SELECT last("temperature") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:57:a4:87\' AND time > now() - 1d',
    },
    "ExtSensRH2": {
        "query": 'SELECT last("humidity") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:57:a4:87\' AND time > now() - 1d',
    },
    "ExtSensTemp3": {
        "query": 'SELECT last("temperature") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:a4:86:84\' AND time > now() - 1d',
    },
    "ExtSensRH3": {
        "query": 'SELECT last("humidity") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:a4:86:84\' AND time > now() - 1d',
    },
    "ExtSensTemp4": {
        "query": 'SELECT last("temperature") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:a4:86:84\' AND time > now() - 1d',
    },
    "ExtSensRH4": {
        "query": 'SELECT last("humidity") FROM "atc_thermometer" WHERE "mac"::tag = \'a4:c1:38:a4:86:84\' AND time > now() - 1d',
    },
}


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
    parser.add_argument("--influx-url", required=True, help="InfluxDB base URL, e.g. http://localhost:8086")
    parser.add_argument("--db", required=True, help="InfluxDB database name")
    parser.add_argument("--user", default=None, help="InfluxDB username")
    parser.add_argument("--password", default=None, help="InfluxDB password")
    # Use latest recorded values; no time bounds required.
    parser.add_argument("--gofutura-url", required=True, help="gofutura base URL, e.g. http://localhost:9090")
    parser.add_argument("--interval-seconds", type=int, default=30, help="Polling interval in seconds (default: 30)")
    parser.add_argument("--dry-run", action="store_true", help="Do not write to gofutura, just print payload")

    args = parser.parse_args()

    if not PAYLOAD_KEY_TO_INFLUX:
        print("PAYLOAD_KEY_TO_INFLUX is empty. Fill the mapping in scripts/influx_to_gofutura.py", file=sys.stderr)
        return 2

    if args.interval_seconds <= 0:
        print("--interval-seconds must be > 0", file=sys.stderr)
        return 2

    while True:
        try:
            payload: Dict[str, float] = {}

            for payload_key, config in PAYLOAD_KEY_TO_INFLUX.items():
                query = config["query"]
                result = influx_query(args.influx_url, args.db, query, args.user, args.password)
                series_list = (result.get("results") or [{}])[0].get("series", []) or []
                
                if series_list and len(series_list) > 0:
                    values = series_list[0].get("values", []) or []
                    if values and len(values) > 0 and len(values[0]) > 1:
                        value = values[0][1]
                        if value is not None:
                            payload[payload_key] = float(value)

            if not payload:
                print("No values found for configured payload keys; nothing to write.")
            else:
                # Send single-field writes to avoid bulk write restrictions.
                for key, value in payload.items():
                    result = post_gofutura(args.gofutura_url, {key: value}, args.dry_run)
                    print(json.dumps(result, indent=2, sort_keys=True))
        except (URLError, socket.gaierror) as exc:
            print(f"Connection error: {exc}. Retrying in {args.interval_seconds}s...", file=sys.stderr)
        except Exception as exc:  # Keep loop alive on transient failures
            print(f"Unexpected error: {exc}. Retrying in {args.interval_seconds}s...", file=sys.stderr)

        time.sleep(args.interval_seconds)


if __name__ == "__main__":
    raise SystemExit(main())
