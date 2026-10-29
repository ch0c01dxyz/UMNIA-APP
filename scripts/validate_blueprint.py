#!/usr/bin/env python3
"""Validate UMNIA blueprint JSON schemas and task/module examples.

Requires: python3 -m pip install jsonschema (only for this architecture tooling).
No backend code is run. Reports structural compliance only.
"""
from __future__ import annotations
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

try:
    import jsonschema
except ImportError:
    print('ERROR jsonschema not installed; cannot claim schema validation', file=sys.stderr)
    raise SystemExit(2)


def read_json(path: Path) -> object:
    return json.loads(path.read_text(encoding='utf-8'))


def validate(schema_file: str, glob: str, id_key: str) -> int:
    schema = read_json(ROOT / schema_file)
    jsonschema.Draft202012Validator.check_schema(schema)
    validator = jsonschema.Draft202012Validator(schema)
    files = sorted(ROOT.glob(glob))
    if not files:
        raise RuntimeError(f'No examples for {glob}')
    seen = set()
    for item in files:
        data = read_json(item)
        validator.validate(data)
        ident = data[id_key]
        if ident in seen:
            raise RuntimeError(f'Duplicate ID {ident} in {item}')
        seen.add(ident)
        if glob.startswith('examples/') and not data['simulation_dataset']['required']:
            raise RuntimeError(f'Missing mandatory simulation dataset in {item}')
        if glob.startswith('tasks/') and not data['status'] == 'PLANNED':
            raise RuntimeError(f'Blueprint ticket falsely claims non-PLANNED status: {item}')
        print(f'PASS {item.relative_to(ROOT)}')
    return len(files)


def main() -> int:
    m = validate('schemas/module-manifest.schema.json', 'examples/*.module.json', 'id')
    t = validate('schemas/agent-task.schema.json', 'tasks/*.json', 'id')
    required = [
        'AGENTS.md', 'DESIGN.md', 'ADR-013-optional-redis-pgbouncer.md',
        'docs/09-advanced-anti-hallucination.md', 'docs/10-agent-orchestration.md',
        'docs/11-deployment-matrix.md', 'docs/12-design-system-ux.md',
        'docs/13-detailed-phase-execution.md',
        'prompts/FRONTIER_LEADER.md', 'prompts/FLASH_WORKER.md', 'prompts/INDEPENDENT_REVIEWER.md',
    ]
    for rel in required:
        if not (ROOT / rel).is_file() or not (ROOT / rel).read_text().strip():
            raise RuntimeError(f'Missing or empty: {rel}')
    print(f'VALIDATED: {m} module manifests, {t} agent tasks, {len(required)} required docs')
    print('SCOPE: structural JSON/docs only; NO runtime DB/Go/Nuxt/Docker/Apt tests executed')
    return 0


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except Exception as ex:
        print(f'FAIL: {type(ex).__name__}: {ex}', file=sys.stderr)
        raise SystemExit(1)
