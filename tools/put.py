#!/usr/bin/env python3
"""Upload a local file to the server (base64 over the SSH exec channel).

SFTP is chrooted on this host, so we stream base64 through a normal exec
channel instead. Reads SERVER_IP / SERVER_USER / SERVER_PASS from the env.
Usage: python tools/put.py <local> <remote-absolute-path>
"""
import base64
import os
import sys

import paramiko

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

HOST = os.environ["SERVER_IP"]
USER = os.environ.get("SERVER_USER", "root")
PASS = os.environ.get("SERVER_PASS")
KEY = os.environ.get("SSH_KEY")  # path to private key; preferred over password


def main():
    local, remote = sys.argv[1], sys.argv[2]
    with open(local, "rb") as f:
        b64 = base64.b64encode(f.read()).decode()
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    kw = {"username": USER, "timeout": 20}
    if KEY and os.path.exists(KEY):
        kw["key_filename"] = KEY
    else:
        kw["password"] = PASS
    c.connect(HOST, **kw)
    cmd = (
        "cat > /tmp/_upload.b64 <<'__EOF__'\n" + b64 + "\n__EOF__\n"
        f"mkdir -p \"$(dirname '{remote}')\" && base64 -d /tmp/_upload.b64 > '{remote}' "
        f"&& rm -f /tmp/_upload.b64 && wc -c '{remote}'"
    )
    _, stdout, stderr = c.exec_command(cmd, timeout=60)
    print(stdout.read().decode(errors="replace"), end="")
    err = stderr.read().decode(errors="replace")
    if err:
        print("[stderr]", err, end="", file=sys.stderr)
    rc = stdout.channel.recv_exit_status()
    c.close()
    sys.exit(rc)


if __name__ == "__main__":
    main()
