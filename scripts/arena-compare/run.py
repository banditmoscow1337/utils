#!/usr/bin/env python3
"""Compare the shared allocator with historical sources in temporary packages."""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--adaptive-repo', type=Path, required=True)
parser.add_argument('--utils-ref', default='e3ac1ed35203a9a65cecb5a2eac4a62916d9125e')
parser.add_argument('--adaptive-ref', default='e91e7d889988420bd5b36381fe598e4910a1d31f')
parser.add_argument('--output', type=Path, required=True)
parser.add_argument('--count', type=int, default=8)
parser.add_argument('--benchtime', default='150ms')
parser.add_argument('--cpu', default='1,4')
parser.add_argument('--bench', default='.')
args = parser.parse_args()

def historical(repo, ref, path):
    return subprocess.check_output(['git', '-C', str(repo), 'show', f'{ref}:{path}'])

with tempfile.TemporaryDirectory(prefix='arena-comparison-') as scratch:
    work = Path(scratch)
    for name, repo, ref, files in [
        ('original', ROOT, args.utils_ref,
         ['arena.go', 'arch_amd64.go', 'arch_arm64.go', 'arch_riscv64.go']),
        ('experimental', args.adaptive_repo.resolve(), args.adaptive_ref,
         ['internal/arena/arena.go', 'internal/arena/stats.go', 'internal/arena/payload.go']),
    ]:
        target = work / name
        target.mkdir()
        for path in files:
            (target / Path(path).name).write_bytes(historical(repo, ref, path))
    (work / 'go.mod').write_text(
        'module arena-bench\n\ngo 1.26.5\n\nrequire (\n'
        '\tgithub.com/banditmoscow1337/utils v0.0.0\n'
        '\tgolang.org/x/sys v0.47.0\n)\n')
    env = {**os.environ, 'GOWORK': 'off'}
    subprocess.run(['go', 'mod', 'edit',
                    f'-replace=github.com/banditmoscow1337/utils={ROOT}'],
                   cwd=work, env=env, check=True)
    (work / 'comparison_test.go').write_bytes(
        (Path(__file__).parent / 'comparison_test.go.txt').read_bytes())
    subprocess.run(['gofmt', '-w', 'comparison_test.go'], cwd=work, check=True)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('w') as output:
        subprocess.run(['go', 'version'], stdout=output, check=True)
        subprocess.run(['go', 'test', '-mod=mod', '-run', '^TestHistoricalFiles$',
                        '-bench', args.bench, '-benchmem',
                        f'-benchtime={args.benchtime}', f'-count={args.count}',
                        f'-cpu={args.cpu}'], cwd=work, env=env, stdout=output,
                       stderr=subprocess.STDOUT, check=True)
print(args.output)
