"""Verify the public ZIP payload, checksum and MCP handshake without AMP access."""
import argparse
import hashlib
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--version', required=True)
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
archive = root / 'dist' / 'amp-mcp-windows-x64.zip'
expected = {
    'amp-mcp.exe', 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.txt',
    'docs/setup.md', 'docs/tools.md', 'docs/troubleshooting.md',
}
digest = hashlib.sha256(archive.read_bytes()).hexdigest()
assert (archive.parent / 'SHA256SUMS').read_text(encoding='utf-8') == f'{digest}  {archive.name}\n'
with zipfile.ZipFile(archive) as bundle:
    names = [entry.filename.replace('\\', '/') for entry in bundle.infolist() if not entry.is_dir()]
    assert len(names) == len(set(names)) and set(names) == expected, names
    # Relative user-guide links must work in the archive, not only on GitHub.
    for name in sorted(expected):
        if not name.endswith('.md'):
            continue
        content = bundle.read(name).decode('utf-8')
        for target in re.findall(r'\[[^\]]+\]\(([^)]+)\)', content):
            if target.startswith(('https://', 'http://', '#')):
                continue
            from posixpath import normpath, join, dirname
            destination = normpath(join(dirname(name), target.split('#')[0]))
            assert destination in expected, (name, target)
    with tempfile.TemporaryDirectory(prefix='amp-mcp-release-') as directory:
        # Extraction is safe only after the exact allowlist above is checked.
        bundle.extractall(directory)
        server = Path(directory) / 'amp-mcp.exe'
        version = subprocess.check_output([str(server), 'version'], text=True).strip()
        assert version == f'amp-mcp {args.version}', version
        subprocess.run([sys.executable, str(root / 'scripts' / 'smoke.py'), '--server', str(server), '--expected-version', args.version], check=True)
print('PASS: release payload, user-guide links, SHA-256, embedded version and packaged MCP server')
