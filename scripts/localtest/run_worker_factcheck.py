#!/usr/bin/env python3
"""Run local Gemini fact checks with the deployed key without printing it."""

import argparse
import json
import os
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROFILE = "watchdog-admin"
REGION = "eu-west-3"


def aws_json(*args):
    result = subprocess.run(
        ["aws", *args, "--profile", PROFILE, "--region", REGION, "--output", "json"],
        capture_output=True, text=True,
    )
    if result.returncode:
        raise SystemExit(f"AWS read failed ({' '.join(args[:2])}): {result.stderr.strip()}")
    return json.loads(result.stdout)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--newsletter-fixture", type=Path,
                        help="Generate and fact-check a newsletter from this local data.json")
    parser.add_argument("--newsletter-stage-test", action="store_true",
                        help="Test approval wording with real newsletter fact checker")
    args = parser.parse_args()
    # CDK injects GEMINI_API_KEY from CI into the deployed Lambda environment.
    # Keep the key in process memory and never print the configuration response.
    functions = aws_json("lambda", "list-functions")["Functions"]
    worker_names = [f["FunctionName"] for f in functions if "-Worker" in f["FunctionName"]]
    if len(worker_names) != 1:
        raise SystemExit(f"Expected one Worker Lambda, found {len(worker_names)}")
    config = aws_json("lambda", "get-function-configuration", "--function-name", worker_names[0])
    key = config.get("Environment", {}).get("Variables", {}).get("GEMINI_API_KEY", "")
    if not key:
        raise SystemExit("Gemini key is empty")
    env = os.environ.copy()
    env["GEMINI_API_KEY"] = key
    if args.newsletter_stage_test:
        env["WATCHDOG_LIVE_FACTCHECK"] = "1"
        subprocess.run(
            ["go", "test", "-run", "^TestLiveNewsletterFactCheckRejectsCompletedClaims$", "-count=1", "-v", "./..."],
            cwd=ROOT / "lambdas" / "shared", env=env, check=True,
        )
    elif args.newsletter_fixture:
        subprocess.run(
            ["go", "run", ".", "newsletter", "--fixtures", str(args.newsletter_fixture.resolve())],
            cwd=ROOT / "scripts" / "localtest", env=env, check=True,
        )
    else:
        env["WATCHDOG_LIVE_FACTCHECK"] = "1"
        subprocess.run(
            ["go", "test", "-run", "^TestLiveFactCheckRejectsWrongTiming$", "-count=1", "-v", "./..."],
            cwd=ROOT / "lambdas" / "worker", env=env, check=True,
        )


if __name__ == "__main__":
    main()
