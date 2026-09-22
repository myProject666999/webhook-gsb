#!/usr/bin/env python3
"""Reference verifier for callbacks produced by this service.

Usage:
  # capture a request with netcat, then:
  python3 scripts/verify-signature.py <secret> <timestamp> <hex_signature> < body.json
"""
import hashlib
import hmac
import sys
import time


def main() -> None:
    secret, ts, v1 = sys.argv[1], sys.argv[2], sys.argv[3]
    body = sys.stdin.buffer.read()
    mac = hmac.new(secret.encode(), f"{ts}.".encode() + body, hashlib.sha256).hexdigest()
    age = abs(time.time() - int(ts))
    print(f"signature match: {hmac.compare_digest(mac, v1)} (computed {mac})")
    print(f"timestamp age:   {age:.0f}s (reject if older than your window, default 300s)")
    print("also remember/reject repeated X-Webhook-Nonce values within the window")


if __name__ == "__main__":
    main()
