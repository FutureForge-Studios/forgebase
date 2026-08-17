#!/usr/bin/env python3
"""Run a command on the server over SSH (password auth).

Reads SERVER_IP / SERVER_USER / SERVER_PASS from the environment (source .env
first, or run via deploy.sh). Usage: python tools/rr.py "<remote command>"
Set RR_TIMEOUT (seconds) for long-running commands.
"""
import os
import sys

import paramiko

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
sys.stderr.reconfigure(encoding="utf-8", errors="replace")

HOST = os.environ["SERVER_IP"]
USER = os.environ.get("SERVER_USER", "root")
PASS = os.environ.get("SERVER_PASS")
KEY = os.environ.get("SSH_KEY")  # path to private key; preferred over password


def main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else "hostname"
    timeout = int(os.environ.get("RR_TIMEOUT", "300"))
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    kw = {"username": USER, "timeout": 20, "banner_timeout": 20}
    if KEY and os.path.exists(KEY):
        kw["key_filename"] = KEY
    else:
        kw["password"] = PASS
    c.connect(HOST, **kw)
    _, stdout, stderr = c.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode(errors="replace")
    err = stderr.read().decode(errors="replace")
    rc = stdout.channel.recv_exit_status()
    if out:
        print(out, end="")
    if err:
        print("[stderr]", err, end="", file=sys.stderr)
    c.close()
    sys.exit(rc)


if __name__ == "__main__":
    main()
