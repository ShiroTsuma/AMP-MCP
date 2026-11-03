"""Verify the public archive payload, checksum and MCP handshake without AMP access."""
import argparse
import hashlib
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--version', required=True)
parser.add_argument('--platform', choices=['windows', 'linux'], default='windows')
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
linux = args.platform == 'linux'
archive = root / 'dist' / ('amp-mcp-linux-x64.tar.gz' if linux else 'amp-mcp-windows-x64.zip')
executable = 'amp-mcp' if linux else 'amp-mcp.exe'
expected = {
    executable, 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.txt',
    'docs/setup.md', 'docs/tools.md', 'docs/troubleshooting.md',
}
digest = hashlib.sha256(archive.read_bytes()).hexdigest()
sums = (archive.parent / 'SHA256SUMS').read_text(encoding='utf-8').splitlines()
assert f'{digest}  {archive.name}' in sums, sums


def verify(names, read, extract):
    assert len(names) == len(set(names)) and set(names) == expected, names
    # Relative user-guide links must work in the archive, not only on GitHub.
    for name in sorted(expected):
        if not name.endswith('.md'):
            continue
        content = read(name).decode('utf-8')
        for target in re.findall(r'\[[^\]]+\]\(([^)]+)\)', content):
            if target.startswith(('https://', 'http://', '#')):
                continue
            from posixpath import normpath, join, dirname
            destination = normpath(join(dirname(name), target.split('#')[0]))
            assert destination in expected, (name, target)
    with tempfile.TemporaryDirectory(prefix='amp-mcp-release-') as directory:
        # Extraction is safe only after the exact allowlist above is checked.
        extract(directory)
        server = Path(directory) / executable
        version = subprocess.check_output([str(server), 'version'], text=True).strip()
        assert version == f'amp-mcp {args.version}', version
        subprocess.run([sys.executable, str(root / 'scripts' / 'smoke.py'), '--server', str(server), '--expected-version', args.version], check=True)


if linux:
    with tarfile.open(archive) as bundle:
        members = [m for m in bundle.getmembers() if m.isfile()]
        assert all(m.isfile() or m.isdir() for m in bundle.getmembers())
        names = [m.name.removeprefix('./') for m in members]
        assert next(m for m in members if m.name.removeprefix('./') == executable).mode & 0o111, 'executable bit missing'
        verify(names, lambda n: bundle.extractfile('./' + n).read(), lambda d: bundle.extractall(d, filter='data'))
else:
    with zipfile.ZipFile(archive) as bundle:
        names = [entry.filename.replace('\\', '/') for entry in bundle.infolist() if not entry.is_dir()]
        verify(names, bundle.read, bundle.extractall)
print(f'PASS: {args.platform} release payload, user-guide links, SHA-256, embedded version and packaged MCP server')
