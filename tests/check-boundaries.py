#!/usr/bin/env python3
"""Core code AND its tests must work without university integrations."""
import subprocess
import sys
from pathlib import Path

module = subprocess.check_output(['go', 'list', '-m'], text=True).strip()
packages = ['./internal/...','./pkg/...','./cmd/raspd','./cmd/botd','./cmd/webd']
deps = subprocess.check_output(['go','list','-deps','-test',*packages],text=True).splitlines()
leaked = [d for d in deps if '/adapters/' in d or '/apeks' in d or '/kgupolicy' in d]
if leaked: sys.exit(f'University dependencies in platform: {leaked}')
if Path('cmd/adapter-kgu').exists():
    deps = subprocess.check_output(['go','list','-deps','./cmd/adapter-kgu'],text=True).splitlines()
    leaked = [d for d in deps if module + '/internal/' in d]
    if leaked: sys.exit(f'KGU adapter depends on core internals: {leaked}')
print('OK: platform tests are university-independent; adapters have no core runtime dependency')
