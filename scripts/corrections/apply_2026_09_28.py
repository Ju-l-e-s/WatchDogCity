#!/usr/bin/env python3
"""Review and conditionally apply PDF-sourced corrections to the 2026-09-28 council.

Default mode only checks current DynamoDB rows. --fixture checks a public
data.json snapshot offline. --apply writes only when every row passed preflight.
Each DynamoDB update compares the old field value to avoid overwriting later work.
"""

import argparse
import json
import subprocess
import sys
from pathlib import Path

MANIFEST = Path(__file__).with_name("2026-09-28.json")
REGION = "eu-west-3"
DELIBERATIONS_TABLE = "watchdog-deliberations"
COUNCILS_TABLE = "watchdog-councils"


def aws(profile, service, operation, **kwargs):
    cmd = ["aws", service, operation, "--profile", profile, "--region", REGION]
    for key, value in kwargs.items():
        flag = "--" + key.replace("_", "-")
        if isinstance(value, bool):
            if value:
                cmd.append(flag)
        else:
            cmd += [flag, json.dumps(value, ensure_ascii=False) if isinstance(value, dict) else str(value)]
    cmd += ["--output", "json"]
    result = subprocess.run(cmd, check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def from_attribute(value):
    if value is None:
        return None
    if "S" in value:
        return value["S"]
    if "N" in value:
        return float(value["N"])
    if "BOOL" in value:
        return value["BOOL"]
    if "M" in value:
        return {k: from_attribute(v) for k, v in value["M"].items()}
    return value


def get_path(row, path):
    current = row
    for part in path.split("."):
        if not isinstance(current, dict):
            return None
        current = current.get(part)
    return current


def update_input(entry, council_id):
    names = {}
    values = {":cid": {"S": council_id}}
    sets = []
    conditions = ["council_id = :cid"]
    for index, (path, patch) in enumerate(entry["changes"].items()):
        aliases = []
        for segment_index, segment in enumerate(path.split(".")):
            alias = f"#f{index}_{segment_index}"
            # DynamoDB stores the Go AnalysisData fields with capitalized
            # names; Publisher's public JSON uses lowercase field names.
            names[alias] = segment.capitalize() if path.startswith("analysis_data.") and segment_index == 1 else segment
            aliases.append(alias)
        target = ".".join(aliases)
        replacement_key = f":replacement{index}"
        values[replacement_key] = {"S": patch["replacement"]}
        sets.append(f"{target} = {replacement_key}")
        if patch["expected"] is None:
            conditions.append(f"attribute_not_exists({target})")
        else:
            expected_key = f":expected{index}"
            values[expected_key] = {"S": patch["expected"]}
            conditions.append(f"{target} = {expected_key}")
    return {
        "table_name": DELIBERATIONS_TABLE,
        "key": {"id": {"S": entry["id"]}},
        "update_expression": "SET " + ", ".join(sets),
        "condition_expression": " AND ".join(conditions),
        "expression_attribute_names": names,
        "expression_attribute_values": values,
        "return_values": "UPDATED_NEW",
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixture", type=Path, help="Check a public data.json snapshot without AWS")
    parser.add_argument("--profile", default="watchdog-admin")
    parser.add_argument("--apply", action="store_true", help="Apply checked updates to DynamoDB")
    args = parser.parse_args()
    if args.fixture and args.apply:
        parser.error("--apply cannot be combined with --fixture")

    manifest = json.loads(MANIFEST.read_text())
    entries = [e for e in manifest["entries"] if e["changes"]]
    if args.fixture:
        public = json.loads(args.fixture.read_text())
        council = next(c for c in public["councils"] if c["id"] == manifest["council_id"])
        rows = {row["id"]: row for row in council["deliberations"]}
    else:
        council_item = aws(args.profile, "dynamodb", "get-item", table_name=COUNCILS_TABLE,
                           key={"council_id": {"S": manifest["council_id"]}}, consistent_read=True).get("Item", {})
        if council_item.get("qc_status", {}).get("S") != "APPROVED":
            raise SystemExit("Council is not APPROVED; refusing corrections")
        rows = {}
        for entry in entries:
            item = aws(args.profile, "dynamodb", "get-item", table_name=DELIBERATIONS_TABLE,
                       key={"id": {"S": entry["id"]}}, consistent_read=True).get("Item", {})
            rows[entry["id"]] = {k: from_attribute(v) for k, v in item.items()}
            if isinstance(rows[entry["id"]].get("analysis_data"), dict):
                rows[entry["id"]]["analysis_data"] = {
                    k.lower(): v for k, v in rows[entry["id"]]["analysis_data"].items()
                }

    errors = []
    pending = []
    already = 0
    for entry in entries:
        row = rows.get(entry["id"], {})
        if not row or row.get("council_id", manifest["council_id"]) != manifest["council_id"] or row.get("pdf_url") != entry["pdf_url"]:
            errors.append(f"{entry['id']}: source row or PDF URL mismatch")
            continue
        states = []
        for path, patch in entry["changes"].items():
            current = get_path(row, path)
            if current == patch["expected"]:
                states.append("pending")
            elif current == patch["replacement"]:
                states.append("applied")
            else:
                errors.append(f"{entry['id']} {path}: unexpected current value")
        if states and all(s == "pending" for s in states):
            pending.append(entry)
        elif states and all(s == "applied" for s in states):
            already += 1
        elif states:
            errors.append(f"{entry['id']}: partially applied row, review manually")
    if errors:
        print("Preflight failed:", *errors, sep="\n- ", file=sys.stderr)
        raise SystemExit(1)
    print(f"Preflight OK: {len(pending)} rows pending, {already} already applied; {sum(len(e['changes']) for e in pending)} fields pending")
    if not args.apply:
        return
    for entry in pending:
        aws(args.profile, "dynamodb", "update-item", **update_input(entry, manifest["council_id"]))
        print("Updated", entry["id"])


if __name__ == "__main__":
    main()
