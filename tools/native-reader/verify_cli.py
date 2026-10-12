#!/usr/bin/env python3
import copy
import hashlib
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile


def seal(root):
    return [(str(path.relative_to(root)), hashlib.sha256(path.read_bytes()).hexdigest())
            for path in sorted(root.rglob('*')) if path.is_file()]


def run(image, fixture, output, request, alter=None, extra=None):
    directory = pathlib.Path(tempfile.mkdtemp(prefix='reader-cli-', dir=output))
    replica = directory / 'replica'
    shutil.copytree(fixture / 'replica', replica)
    replica.chmod(0o700)
    if alter:
        alter(replica)
    before = seal(replica)
    args = ['docker', 'run', '--rm', '--interactive', '--network=none', '--read-only',
            '--memory=512m', '--memory-swap=512m', '--pids-limit=16',
            '--user', str(__import__('os').getuid()) + ':' + str(__import__('os').getgid()),
            '--mount', 'type=bind,src=' + str(directory) + ',dst=/copy']
    if extra:
        args.extend(extra)
    args.append(image)
    result = subprocess.run(args, input=json.dumps(request), text=True, capture_output=True, timeout=70)
    if before != seal(replica):
        raise AssertionError('verifier changed private input')
    value = json.loads(result.stdout)
    if len(result.stdout.encode()) > 1 << 20:
        raise AssertionError('hard output ceiling')
    return result.returncode, value


def forged_length(root):
    path = sorted((root / 'clog').glob('*.stdb.log'))[-1]
    data = bytearray(path.read_bytes())
    data[28:32] = (0xffffffff).to_bytes(4, 'little')
    path.write_bytes(data)


def selected_object(root, request):
    selected = root / 'snapshots' / (f"{request['history']['offset']:020}.snapshot_dir")
    return next(path for path in sorted((selected / 'objects').rglob('*')) if path.is_file())


def damage_snapshot(root, request, action):
    path = selected_object(root, request)
    if action == 'missing':
        path.unlink()
        return
    value = bytearray(path.read_bytes())
    if action == 'torn':
        value.pop()
    else:
        value[-1] ^= 1
    path.write_bytes(value)


def empty_logs(root):
    shutil.rmtree(root / 'clog')
    (root / 'clog').mkdir()


def damaged_snapshot_only(root, request, action):
    empty_logs(root)
    damage_snapshot(root, request, action)


def main():
    image, fixture, output = sys.argv[1:]
    fixture = pathlib.Path(fixture)
    output = pathlib.Path(output)
    output.mkdir()
    request = json.loads((fixture / 'request.json').read_text())
    initial = seal(fixture)
    exit_code, baseline = run(image, fixture, output, request)
    assert exit_code == 0 and baseline['outcome'] == 'observed', (exit_code, baseline)
    assert baseline['observed_database_identity'] == request['expected_database_identity']
    assert baseline['observed_owner_identity'] == request['expected_owner_identity']
    assert next(table for table in baseline['tables'] if table['name'] == 'reader_fixture')['rows'] == 20
    results = [{'case': 'contained_native_replay', 'exit': exit_code, 'state': baseline['state_blake3']}]
    logs = copy.deepcopy(request)
    logs['history'] = {'kind': 'logs'}
    exit_code, observed = run(image, fixture, output, logs)
    assert exit_code == 0 and observed['state_blake3'] == baseline['state_blake3']
    results.append({'case': 'logs_snapshot_equality', 'exit': exit_code})
    code, snapshot = run(image, fixture, output, request, empty_logs)
    assert code == 0 and snapshot['framing'] == 'snapshot-only-v1' and snapshot['input']['commits'] == 0
    assert snapshot['committed_offset'] == snapshot['snapshot']['offset'] == baseline['committed_offset']
    for field in ['state_blake3', 'tables', 'current_program_hash', 'current_program_sha256',
                  'observed_database_identity', 'observed_owner_identity']:
        assert snapshot[field] == baseline[field], field
    results.append({'case': 'selected_snapshot_empty_log_suffix', 'exit': code, 'log_commits': 0})
    code, refusal = run(image, fixture, output, logs, empty_logs)
    assert code == 70 and refusal['code'] == 'missing_history'
    results.append({'case': 'empty_log_only_refused', 'exit': code, 'refusal': refusal['code']})
    for damage in ['missing', 'torn', 'corrupt']:
        code, refusal = run(image, fixture, output, request,
                            lambda root: damage_snapshot(root, request, damage))
        assert code == 70 and refusal['code'] == 'snapshot', (damage, code, refusal)
        results.append({'case': 'selected_snapshot_' + damage, 'exit': code, 'refusal': refusal['code']})
        code, refusal = run(image, fixture, output, request,
                            lambda root: damaged_snapshot_only(root, request, damage))
        assert code == 70 and refusal['code'] == 'snapshot', (damage, code, refusal)
        results.append({'case': 'snapshot_only_' + damage, 'exit': code, 'refusal': refusal['code']})
    for extra in [{'foreign': 1}, {'offset': request['history']['offset'], 'replica_id': 1}]:
        invalid = copy.deepcopy(logs)
        invalid['history'].update(extra)
        code, refusal = run(image, fixture, output, invalid)
        assert code == 70 and refusal['code'] == 'request'
        results.append({'case': 'logs_foreign_fields', 'exit': code, 'refusal': refusal['code']})
    for name, maximum in [('files', 1), ('stored_bytes', 1), ('expanded_bytes', 1), ('rows', 1),
                          ('row_bytes', 1), ('tables', 1), ('program_bytes', 1), ('output_bytes', 256),
                          ('allocation_bytes', 4096), ('deadline_ms', 1)]:
        limited = copy.deepcopy(request)
        limited['limits'][name] = maximum
        code, refusal = run(image, fixture, output, limited)
        assert code == 70 and refusal['outcome'] == 'refused', (name, code, refusal)
        results.append({'case': name, 'exit': code, 'refusal': refusal['code']})
    for field in ['expected_database_identity', 'expected_owner_identity']:
        wrong = copy.deepcopy(logs)
        wrong[field] = '09' * 32
        code, refusal = run(image, fixture, output, wrong)
        assert code == 70 and refusal['code'] == 'identity'
        results.append({'case': field, 'exit': code, 'refusal': refusal['code']})
    code, refusal = run(image, fixture, output, logs, forged_length)
    assert code == 70 and refusal['code'] == 'framing'
    results.append({'case': 'forged_u32_max_payload', 'exit': code, 'refusal': refusal['code']})
    code, refusal = run(image, fixture, output, request, extra=['--env', 'AWS_SECRET_ACCESS_KEY=synthetic-denial-case'])
    assert code == 70 and refusal['code'] == 'containment'
    results.append({'case': 'credential_environment_refused', 'exit': code, 'refusal': refusal['code']})
    assert seal(fixture) == initial
    (output / 'receipt.json').write_text(json.dumps({'passed': True, 'cases': results, 'source_unchanged': True}, indent=2) + '\n')
    print(json.dumps({'passed': True, 'cases': len(results), 'source_unchanged': True}))


if __name__ == '__main__':
    main()
