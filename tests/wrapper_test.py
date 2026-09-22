#!/usr/bin/env python3
"""Verify preset arguments emitted by the real wrapper, without scanning."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix='soapht-wrapper-') as folder:
    out = Path(folder)
    scanner = out / 'scanimage'
    scanner.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
if args == ['-L']:
    print("device `hpaio:/usb/mock' is a mock scanner")
elif '-A' in args:
    print('  --resolution ' + os.environ.get('MOCK_RESOLUTIONS', '150|300') + 'dpi [300]')
    print('  -x 0..215.9mm [215.9]')
    print('  -y 0..' + os.environ.get('MOCK_HEIGHT', '296.926') + 'mm [296.926]')
elif any(arg.startswith('--batch=') for arg in args):
    pattern = next(arg.split('=',1)[1] for arg in args if arg.startswith('--batch='))
    Path(pattern % 1).write_bytes(bytes([255,216,255,217]))
    sys.exit(int(os.environ.get('MOCK_BATCH_EXIT', '9')))
else:
    Path(os.environ['SCAN_ARGS_LOG']).write_text(json.dumps(args))
    Path(args[args.index('-o')+1]).write_bytes(bytes([255,216,255,217]))
''')
    scanner.chmod(0o755)
    env = dict(os.environ, PATH=str(out)+os.pathsep+os.environ['PATH'], SCAN_ARGS_LOG=str(out/'args.json'))
    cases = [(None, None), ('A4', ('210', '297')), ('Letter', ('215.9', '279.4')), ('Legal', ('215.9', '355.6'))]
    for name, dimensions in cases:
        cmd = ['/bin/bash', str(root/'bin/hp-scan'), '--source', 'ADF', '--resolution', '300', '--mode', 'Gray']
        if name: cmd += ['--page-size', name]
        cmd += [str(out/'test.jpg')]
        subprocess.run(cmd, env=env, capture_output=True, check=True)
        args = json.loads((out/'args.json').read_text())
        if dimensions:
            assert args[args.index('-x')+1] == dimensions[0]
            assert args[args.index('-y')+1] == dimensions[1]
            assert args[args.index('-l')+1] == '0' and args[args.index('-t')+1] == '0'
            assert args.index('--source') < args.index('-x')
        else:
            assert '-x' not in args and '-y' not in args
    # The same wrapper must allow capable models without weakening M127 checks.
    for source, dpi, page, overrides, expected in [
        ('ADF', 600, None, {}, 2),
        ('ADF', 600, None, {'MOCK_RESOLUTIONS': '150|300|600'}, 0),
        ('Flatbed', 300, 'Legal', {}, 2),
        ('Flatbed', 300, 'Legal', {'MOCK_HEIGHT': '355.6'}, 0),
    ]:
        cmd = ['/bin/bash', str(root/'bin/hp-scan'), '--device', 'hpaio:/usb/selected',
               '--source', source, '--resolution', str(dpi)]
        if page: cmd += ['--page-size', page]
        cmd += [str(out/'test.jpg')]
        result = subprocess.run(cmd, env=dict(env, **overrides), capture_output=True, text=True)
        assert result.returncode == expected, result.stderr
        if not expected:
            args = json.loads((out/'args.json').read_text())
            assert args[args.index('-d')+1] == 'hpaio:/usb/selected'
    result = subprocess.run(['/bin/bash', str(root/'bin/hp-scan'), '--device', 'hpaio:/usb/selected',
                             '--source', 'ADF', '--batch', str(out/'failed.pdf')],
                            env=dict(env, TMPDIR=str(out), MOCK_BATCH_EXIT='9'), capture_output=True, text=True)
    assert result.returncode == 9 and not (out/'failed.pdf').exists()
    retained = next(line.split(': ',1)[1] for line in result.stderr.splitlines()
                    if line.startswith('Retained scan files'))
    assert (Path(retained)/'page-001.jpg').read_bytes() == bytes([255,216,255,217])
    print('Wrapper geometry, device selection, per-device limits and failed-batch retention passed')
