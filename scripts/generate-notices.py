#!/usr/bin/env python3
"""Collect dependency license texts without copying module source or local paths."""
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
raw = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=root, text=True)
decoder = json.JSONDecoder()
modules = []
while raw.strip():
    value, end = decoder.raw_decode(raw.lstrip())
    modules.append(value)
    raw = raw.lstrip()[end:]
parts = ['# Third-party notices\n\nThe following Go dependencies retain their original licenses.\n', '\n## Platform adapter contract library (pkg/provider)\n\n```text\n' + (root / 'pkg/provider/LICENSE').read_text() + '```\n']
for module in sorted(modules, key=lambda m: m['Path']):
    if module.get('Main'):
        continue
    if 'Dir' not in module:
        raise SystemExit('Run go mod download before generating notices')
    directory = Path(module['Dir'])
    licenses = [p for p in sorted(directory.iterdir()) if p.is_file() and
                (p.name.upper().startswith(('LICENSE', 'LICENCE', 'COPYING', 'NOTICE')) or p.name == 'AUTHORS')]
    if not licenses:
        raise SystemExit(f"No license found for {module['Path']}")
    parts.append(f"\n## {module['Path']} {module['Version']}\n")
    for path in licenses:
        parts.append(f'\n### {path.name}\n\n```text\n{path.read_text().rstrip()}\n```\n')
(root / 'THIRD_PARTY_NOTICES.md').write_text(''.join(parts))

(root / 'internal/web/assets/third-party.txt').write_text(''.join(parts))
