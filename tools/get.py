#!/usr/bin/env python3
"""get.py <remote_path> <local_path> - download a file from the server.

Counterpart of put.py: SFTP is chrooted on this host, so stream base64 over an
exec channel instead. Reads SERVER_IP / SERVER_USER / SERVER_PASS from env.
"""
import base64
import os
import sys

import paramiko


def main() -> None:
    remote, local = sys.argv[1], sys.argv[2]
    host = os.environ["SERVER_IP"]
    user = os.environ.get("SERVER_USER", "root")
    pw = os.environ.get("SERVER_PASS")
    key = os.environ.get("SSH_KEY")  # path to private key; preferred over password

    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    kw = {"username": user, "timeout": 30}
    if key and os.path.exists(key):
        kw["key_filename"] = key
    else:
        kw["password"] = pw
    c.connect(host, **kw)
    try:
        _, stdout, stderr = c.exec_command(f"base64 '{remote}'", timeout=3600)
        os.makedirs(os.path.dirname(os.path.abspath(local)), exist_ok=True)
        n = 0
        with open(local, "wb") as f:
            buf = b""
            while True:
                chunk = stdout.read(1 << 20)
                if not chunk:
                    break
                buf += chunk.replace(b"\n", b"").replace(b"\r", b"")
                # decode in multiples of 4 (base64 quantum), keep remainder
                cut = len(buf) - (len(buf) % 4)
                if cut:
                    f.write(base64.b64decode(buf[:cut]))
                    n += cut * 3 // 4
                    buf = buf[cut:]
            if buf:
                f.write(base64.b64decode(buf))
        err = stderr.read().decode().strip()
        if err:
            print(f"stderr: {err}", file=sys.stderr)
        print(f"{os.path.getsize(local)} bytes -> {local}")
    finally:
        c.close()


if __name__ == "__main__":
    main()
