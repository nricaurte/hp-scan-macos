#!/usr/bin/env python3
"""List scan-type=5 candidates from HPLIP models.dat; not a support claim."""
import argparse
import configparser
import json
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('models', type=Path, help='HPLIP data/models/models.dat')
parser.add_argument('--format', choices=['json', 'markdown'], default='json')
args = parser.parse_args()
# HPLIP 3.25.8 contains repeated sections; its later values take precedence.
models = configparser.ConfigParser(interpolation=None, strict=False)
with args.models.open() as file:
    models.read_file(file)
rows = [dict(model=model, scan_type=5,
             status='M127fn hardware baseline' if model == 'hp_laserjet_pro_mfp_m127fn'
                    else 'Candidate; hardware unverified')
        for model in sorted(models.sections())
        if models.get(model, 'scan-type', fallback='') == '5']
if args.format == 'json':
    print(json.dumps(rows, indent=2))
else:
    print('# HPLIP scan-type=5 candidates\n')
    print(f'Generated from HPLIP models.dat: **{len(rows)} entries**. '
          'Protocol classification does not establish macOS compatibility.\n')
    print('| HPLIP model identifier | Validation status |\n|---|---|')
    for row in rows:
        print(f"| `{row['model']}` | {row['status']} |")
