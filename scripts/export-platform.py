#!/usr/bin/env python3
"""Export public source without installation files, build outputs or Git history."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import tempfile

MARKER = '.platform-export.json'
ROOT = Path(__file__).resolve().parent.parent


def inventory(root):
    return {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(root.rglob('*')) if p.is_file() and p != root / MARKER}


def check_owned(out):
    if out.is_symlink() or any(p.is_symlink() for p in out.rglob('*')):
        raise ValueError('Destination contains symbolic links')
    if any(p.name == '.git' for p in out.rglob('*')):
        raise ValueError('Destination is a Git checkout; export to a separate directory')
    try:
        manifest = json.loads((out / MARKER).read_text())
    except (OSError, ValueError) as e:
        raise ValueError('Destination is not a managed export') from e
    if manifest.get('format') != 1 or manifest.get('files') != inventory(out):
        raise ValueError('Destination has changed; preserve its edits before replacing it')


def sources():
    files = [ROOT / p for p in ['go.mod', 'go.sum', 'Dockerfile', '.dockerignore',
             '.gitignore', 'Makefile', '.env.example', 'README.md', 'compose.yaml',
             'compose.admin-host.yaml', 'profiles/example.json', 'LICENSE', 'LICENSING.md',
             'THIRD_PARTY_NOTICES.md', 'pkg/provider/LICENSE']]
    # Only source formats and known trees. Local envs, databases, hidden paths and
    # symlinks cannot hitchhike inside an otherwise public directory.
    suffixes = {'.go', '.sql', '.html', '.css', '.js', '.mjs', '.cjs', '.py',
                '.json', '.svg', '.png', '.webmanifest', '.md', '.txt'}
    for directory in ['internal', 'pkg', 'examples', 'tests']:
        for p in (ROOT / directory).rglob('*'):
            rel = p.relative_to(ROOT)
            if p.is_file() and not any(x.startswith('.') or x == '__pycache__' for x in rel.parts):
                if p.suffix in suffixes or p.name == 'Dockerfile':
                    files.append(p)
    files.extend(ROOT / p for p in ['scripts/export-platform.py', 'scripts/generate-notices.py'])
    files.extend((ROOT / 'templates/platform/.github/workflows').glob('*.yml'))
    for name in ['raspd', 'botd', 'webd', 'check-adapter', 'provider-schema']:
        files.extend((ROOT / 'cmd' / name).glob('*.go'))
    files.extend((ROOT / 'docs').glob('*.md'))
    files.extend((ROOT / 'docs').glob('*.json'))
    files.extend(p for p in (ROOT / 'deploy').glob('*.example') if p.name != 'adapter-kgu.env.example')
    files.extend(ROOT / 'deploy' / p for p in ['raspd.service', 'botd.service', 'webd.service'])
    for p in sorted(set(files)):
        if p.resolve() != p.absolute():
            raise ValueError(f'Symlink in public sources: {p.relative_to(ROOT)}')
        yield p


def export(out, replace=False, module=None):
    if out.is_symlink():
        raise ValueError('Destination must not be a symbolic link')
    out = out.resolve()
    if out == ROOT or out in ROOT.parents or (ROOT in out.parents and ROOT / 'dist' not in out.parents):
        raise ValueError('Export outside the source tree or inside dist/')
    if module and not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._~-]*(?:/[A-Za-z0-9][A-Za-z0-9._~-]*)+', module):
        raise ValueError('Invalid Go module path')
    if out.exists():
        if not replace:
            raise ValueError('Destination exists; use --replace for an unchanged managed export')
        check_owned(out)
    out.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.platform-export-', dir=out.parent))
    try:
        old_module = re.search(r'^module (\S+)', (ROOT / 'go.mod').read_text(), re.M)[1]
        for path in sources():
            dest = stage / path.relative_to(ROOT)
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, dest)
            if path.suffix not in {'.png'}:
                text = dest.read_text()
                if path.suffix == '.md':
                    text = re.sub(r'<!-- installation-only -->.*?<!-- /installation-only -->', '', text, flags=re.S)
                if module:
                    text = re.sub(r'(?<!https://)(?<!http://)' + re.escape(old_module) + r"(?=$|[/\s\"'`])", lambda _: module, text)
                dest.write_text(text)
        ci = stage / '.github/workflows'
        ci.mkdir(parents=True)
        for template in (stage / 'templates/platform/.github/workflows').glob('*.yml'):
            shutil.copy2(template, ci / template.name)
        (stage / MARKER).write_text(json.dumps({'format': 1, 'files': inventory(stage)}, indent=2) + '\n')
        if out.exists():
            check_owned(out)
            # Preserve the previous export if installing the completed stage fails.
            backup = Path(tempfile.mkdtemp(prefix='.platform-previous-', dir=out.parent))
            backup.rmdir()
            out.rename(backup)
            try:
                stage.rename(out)
            except BaseException:
                backup.rename(out)
                raise
            shutil.rmtree(backup)
        else:
            stage.rename(out)
    finally:
        if stage.exists():
            shutil.rmtree(stage)
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output', nargs='?', type=Path, default=ROOT / 'dist/platform')
    parser.add_argument('--replace', action='store_true', help='replace an unchanged managed export')
    parser.add_argument('--module', help='Go module path of the future public repository')
    args = parser.parse_args()
    try:
        print(export(args.output, args.replace, args.module))
    except (ValueError, OSError) as e:
        parser.exit(1, f'{e}\n')


if __name__ == '__main__':
    main()
