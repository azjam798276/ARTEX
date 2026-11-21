#!/usr/bin/env python3
"""Mint an ARTEX API token for the MCP server, using the instance's own login endpoint.

`artex -mcp-serve` authenticates as a bearer-token caller, not as the console
cookie. A token is a 7-day HS256 JWT signed with the instance's key, so the only
supported ways to obtain one are the login endpoint (what this script does) or
copying `localStorage.artex_token` out of a signed-in console.

Both are used here because they answer different needs: the login path is
scriptable and is what CI or a fresh deployment wants, while the console path
suits an operator who is already signed in and does not want to re-type the
password into a command line.

Usage:
    ./mint-token.py --url http://127.0.0.1:8787                 # prompts for the password
    ARTEX_PASSWORD=... ./mint-token.py --url http://127.0.0.1:8787 --print-env

The password is read from ARTEX_PASSWORD or prompted for (never from argv, where
it would land in the process list and shell history). The token is written to
stdout or, with --print-env, as a ready-to-source `export ARTEX_API_TOKEN=...`.

Exit codes: 0 success; 2 the instance is not reachable or not set up; 3 the
login was refused; 4 the response did not carry a token.
"""

import argparse
import getpass
import json
import os
import sys
import urllib.error
import urllib.request

# Documented in the module docstring; sys.exit(str) would always exit 1.
EXIT_UNREACHABLE = 2
EXIT_REFUSED = 3
EXIT_SETUP = 2
EXIT_NO_TOKEN = 4


def fail(code, message):
    """Print one diagnostic line and exit with the documented code."""
    print(message, file=sys.stderr)
    sys.exit(code)


def login(url, password, timeout):
    """POST the login endpoint and return the token, or raise SystemExit."""
    body = json.dumps({"username": "ARTEX", "password": password}).encode()
    request = urllib.request.Request(
        url.rstrip("/") + "/api/auth/login",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as error:
        detail = ""
        try:
            detail = (json.load(error) or {}).get("error") or ""
        except (ValueError, AttributeError):
            pass
        if error.code == 403:
            fail(EXIT_SETUP, f"this instance has no admin password yet; open /setup and claim it first")
        if error.code in (401, 429):
            fail(EXIT_REFUSED, f"login refused (HTTP {error.code}){': ' + detail if detail else ''}")
        fail(EXIT_REFUSED, f"login failed: HTTP {error.code}{': ' + detail if detail else ''}")
    except (urllib.error.URLError, OSError) as error:
        fail(EXIT_UNREACHABLE, f"cannot reach {url}: {error}")

    token = payload.get("token") if isinstance(payload, dict) else None
    if not isinstance(token, str) or not token:
        fail(EXIT_NO_TOKEN, "the login response carried no token")
    return token


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--url", default=os.environ.get("ARTEX_URL", "http://127.0.0.1:8787"),
                        help="base URL of the running instance (default: %(default)s)")
    parser.add_argument("--print-env", action="store_true",
                        help="print `export ARTEX_API_TOKEN=...` instead of the bare token")
    parser.add_argument("--timeout", type=float, default=10.0)
    args = parser.parse_args()

    password = os.environ.get("ARTEX_PASSWORD")
    if not password:
        try:
            password = getpass.getpass("ARTEX password: ")
        except EOFError:
            # No controlling terminal (a pipe, cron, CI): prompting would hang
            # or raise, so say what the caller must set instead.
            fail(EXIT_REFUSED, "no password: set ARTEX_PASSWORD or run from a terminal")
    if not password:
        fail(EXIT_REFUSED, "no password given")

    token = login(args.url, password, args.timeout)
    print(f'export ARTEX_API_TOKEN="{token}"' if args.print_env else token)
    return 0


if __name__ == "__main__":
    sys.exit(main())
