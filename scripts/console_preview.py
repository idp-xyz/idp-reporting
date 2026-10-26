#!/usr/bin/env python3
"""Run the existing guarded Go demo for local console preview, never production."""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--host', default='127.0.0.1')
    parser.add_argument('--port', default=os.environ.get('PORT', '3000'))
    parser.add_argument('--strictPort', action='store_true')
    args = parser.parse_args()
    local = {}
    path = ROOT / '.preview.local.json'
    if path.exists():
        if path.is_symlink() or stat.S_IMODE(path.stat().st_mode) != 0o600:
            raise SystemExit('private preview configuration must be a regular 0600 file')
        local = json.loads(path.read_text())
    go = os.environ.get('REPORTING_GO') or local.get('go') or shutil.which('go')
    token = os.environ.get('REPORTING_DEMO_TOKEN') or local.get('token', '')
    prebuilt = ROOT / 'bin/reporting-demo'
    if (not prebuilt.is_file() and not go) or len(token) < 32:
        raise SystemExit('Build bin/reporting-demo or set REPORTING_GO; set a random REPORTING_DEMO_TOKEN (32+ characters).')
    port = args.port
    if not port.isdigit() or not 1024 <= int(port) <= 65535:
        raise SystemExit('valid preview port required')
    env = {**os.environ, 'GOTOOLCHAIN': 'local', 'REPORTING_DEMO_TOKEN': token}
    with tempfile.TemporaryDirectory(prefix='idp-reporting-console-') as directory:
        binary = prebuilt
        if not prebuilt.is_file():
            binary = Path(directory) / 'reporting-demo'
            subprocess.run([go, 'build', '-o', str(binary), './cmd/reporting-demo'], cwd=ROOT, env=env, check=True, timeout=120)
        child = subprocess.Popen([str(binary), '-profile', 'contract-simulator', '-memory', '-listen', '127.0.0.1:' + port], cwd=ROOT, env=env)
        def stop(_number, _frame):
            if child.poll() is None:
                child.terminate()
        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        raise SystemExit(child.wait())


if __name__ == '__main__':
    main()
