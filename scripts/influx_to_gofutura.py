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

# Map gofutura external sensor ID (1..8) -> list of InfluxDB MACs.
# This allows using a single MAC for multiple zones.
# Example:
# EXT_SENSOR_TO_MACS = {
#     1: ["AA:BB:CC:DD:EE:FF"],
#     2: ["AA:BB:CC:DD:EE:FF", "11:22:33:44:55:66"],
# }
EXT_SENSOR_TO_MACS: Dict[int, list[str]] = {
    1: ["a4:c1:38:cb:ca:c0"],  # vymyslene
    2: ["a4:c1:38:57:a4:87"],  # vymyslene
    3: ["a4:c1:38:a4:86:84"],
    4: ["a4:c1:38:a4:86:84"],
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


def extract_last_by_mac(result: Dict[str, Any]) -> Dict[str, float]:
    out: Dict[str, float] = {}
    series_list = result.get("series", []) or []

    for series in series_list:
        mac = (series.get("tags") or {}).get("mac")
        values = series.get("values", []) or []
        last_val: Optional[float] = None

        # Find last non-null value in this series
        for row in reversed(values):
            if not row or len(row) < 2:
                continue
            v = row[1]
            if v is None:
                continue
            last_val = float(v)
            break

        if mac and last_val is not None:
            out[mac] = last_val

    return out


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

    if not EXT_SENSOR_TO_MACS:
        print("EXT_SENSOR_TO_MACS is empty. Fill the mapping in scripts/influx_to_gofutura.py", file=sys.stderr)
        return 2

    temp_query = (
        "SELECT last(\"temperature\") FROM \"atc_thermometer\" "
        "WHERE time > now() - 1d "
        "GROUP BY \"mac\"::tag"
    )
    humi_query = (
        "SELECT last(\"humidity\") FROM \"atc_thermometer\" "
        "WHERE time > now() - 1d "
        "GROUP BY \"mac\"::tag"
    )

    if args.interval_seconds <= 0:
        print("--interval-seconds must be > 0", file=sys.stderr)
        return 2

    while True:
        temp_resp = influx_query(args.influx_url, args.db, temp_query, args.user, args.password)
        humi_resp = influx_query(args.influx_url, args.db, humi_query, args.user, args.password)

        temp_series = (temp_resp.get("results") or [{}])[0]
        humi_series = (humi_resp.get("results") or [{}])[0]

        last_temp_by_mac = extract_last_by_mac(temp_series)
        last_humi_by_mac = extract_last_by_mac(humi_series)

        payload: Dict[str, float] = {}
        for ext_id, macs in EXT_SENSOR_TO_MACS.items():
            for mac in macs:
                if mac in last_temp_by_mac:
                    payload[f"ExtSensTemp{ext_id}"] = last_temp_by_mac[mac]
                if mac in last_humi_by_mac:
                    payload[f"ExtSensRH{ext_id}"] = last_humi_by_mac[mac]

        if not payload:
            print("No values found for configured MACs; nothing to write.")
        else:
            result = post_gofutura(args.gofutura_url, payload, args.dry_run)
            print(json.dumps(result, indent=2, sort_keys=True))

        time.sleep(args.interval_seconds)


if __name__ == "__main__":
    raise SystemExit(main())
