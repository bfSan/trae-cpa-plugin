#!/usr/bin/env python3
"""Offline release identity and channel/scheduler boundary guard."""
import json
import re
from pathlib import Path
root = Path(__file__).resolve().parents[1]
version = (root / 'plugins/trae/VERSION').read_text().strip()
assert re.fullmatch(r'\d+\.\d+\.\d+', version)
reg = json.loads((root / 'registry.json').read_text())
assert [p['id'] for p in reg['plugins']] == ['trae']
p = reg['plugins'][0]
assert p['version'] == version
assert p['repository'] == 'https://github.com/bfSan/trae-cpa-plugin'
assert p['homepage'] == 'https://github.com/bfSan/trae-cpa-plugin'
assert (root / 'README.md').read_text().startswith('# trae-cpa-plugin\n')
mod = (root / 'plugins/trae/go.mod').read_text()
assert 'module github.com/bfSan/trae-cpa-plugin/plugins/trae\n' in mod
assert f'var version = "{version}"' in (root / 'plugins/trae/main.go').read_text()
for folder in ('pool', 'scheduler'):
    assert not (root / 'plugins/trae' / folder).exists(), 'routing-policy package must not remain'
for f in (root / 'plugins/trae').rglob('*.go'):
    if f.name.endswith('_test.go'):
        continue
    text = f.read_text()
    assert '"github.com/mmqz/cpa-multi-plugins/plugins/trae' not in text, f
    assert 'accountPool' not in text, f
    assert not re.search(r'"[^"\n]*/(?:pool|scheduler)"', text), f
workflow = (root / '.github/workflows/release.yml').read_text()
assert 'cpa-multi-plugins-' not in workflow
assert 'PLUGINS=' not in workflow
print('Trae version, repository identity and staged ownership guards passed:', version)
