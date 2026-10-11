"""Exercise the real CLI, then act as a small deterministic local executor.

The executor is this fixture, not an autonomous feature claimed for Akasha.
It creates a temporary config and a loopback HTTP server, observes results,
records the report, captures the composition and checks its next invocation.
No model API, user project configuration, Docker or cloud service is touched.
"""
import argparse
import hashlib
import json
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--exe', default=str(ROOT / 'bin/akasha-ilang.exe'))
    args = parser.parse_args()
    executable = str(Path(args.exe).resolve())
    reports = ROOT / 'ilang/reports'
    reports.mkdir(parents=True, exist_ok=True)
    trace = []
    timings = []
    with tempfile.TemporaryDirectory(prefix='akasha-ilang-') as tmp:
        root = Path(tmp)
        data, project = root / 'data', root / 'work' / 'demo'
        project.mkdir(parents=True)

        def cli(command, *extra, fail=False):
            began = time.perf_counter()
            p = subprocess.run([executable, command, '--data', str(data), '--project', 'demo', *map(str, extra)], text=True, encoding='utf8', capture_output=True, timeout=30)
            timings.append({'command': command, 'milliseconds': round((time.perf_counter()-began)*1000, 2)})
            if fail:
                assert p.returncode != 0, p.stdout
                return
            assert p.returncode == 0, p.stderr
            value = json.loads(p.stdout)
            trace.append({'command': command, 'result': value})
            return value

        serial = 0
        def send(command, value, *extra, **kwargs):
            nonlocal serial
            serial += 1
            path = root / f'input-{serial}.json'
            path.write_text(json.dumps(value, ensure_ascii=False), encoding='utf8')
            return cli(command, '--file', path, *extra, **kwargs)

        cli('space-add', '--id', 'work', '--name', 'Work', '--root', project.parent)
        cli('project-add', '--space', 'work', '--id', 'demo', '--name', 'Demo', '--root', project)
        assert not cli('ilang-status')['enabled']
        definition = json.loads((Path(__file__).with_name('server_vide_h.json')).read_text(encoding='utf8'))
        send('ilang-define', definition, fail=True)
        cli('ilang-enable')
        server_version = send('ilang-define', definition)
        config = {
            'format': 'akasha.ilang/v1', 'name': 'local_configuration', 'description': 'Create the agreed local service configuration.',
            'parameters': definition['parameters'], 'prompt': 'Write local configuration in {{directory}} for loopback port {{port}}.',
            'checks': ['Configuration exists and contains the agreed port'], 'effects': ['project.write'],
        }
        config_version = send('ilang-define', config)
        composed = {
            'format': 'akasha.ilang/v1', 'name': 'video_service', 'description': 'Compose configuration, a server and integration checks.',
            'parameters': definition['parameters'], 'checks': ['Live service port matches the configuration'], 'effects': ['project.write', 'network'],
            'steps': [
                {'id': 'server', 'skill': {'name': 'server_vide_h', 'version': server_version['version']}, 'arguments': {'directory': {'input': 'directory'}, 'port': {'input': 'port'}}, 'depends_on': ['configuration']},
                {'id': 'configuration', 'skill': {'name': 'local_configuration', 'version': config_version['version']}, 'arguments': {'directory': {'input': 'directory'}, 'port': {'input': 'port'}}},
            ],
        }
        composition_version = send('ilang-define', composed)
        run = send('ilang-prepare', {'skill': {'name': 'video_service'}, 'goal': 'Создай video_service', 'issues': [{'id': 'exposure', 'question': 'Кому должен быть доступен сервер?'}]})
        assert run['session']['result']['status'] == 'needs_input'
        assert len(run['session']['result']['questions']) == 3
        assert 'master_prompt' not in run['session']['result'] and 'plan' not in run['session']['result']

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                body = json.dumps({'status': 'ok', 'port': self.server.server_port}).encode()
                self.send_response(200 if self.path == '/healthz' else 404)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *args):
                pass

        # Reserve a loopback port; do not serve until requirements are complete.
        httpd = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        port = httpd.server_port
        worker = None
        try:
            partial = send('ilang-answer', {'values': {'parameters.directory': str(project), 'parameters.port': str(port)}}, '--id', run['revision'])
            assert partial['session']['result']['status'] == 'needs_input'
            ready = send('ilang-answer', {'values': {'issues.exposure': 'Только локально, на 127.0.0.1'}}, '--id', partial['revision'])
            result = ready['session']['result']
            assert result['status'] == 'ready' and result['tokens'] <= 8000
            assert [s['id'] for s in result['plan']] == ['video_service.configuration', 'video_service.server', 'video_service._verify']
            assert result['external_api_calls'] == 0
            report = {'plan_hash': result['plan_hash'], 'analysis': 'Локальный исполнитель fixture создал конфигурацию, запустил HTTP-сервер, получил ответ /healthz и сверил порт. Это проверка цепочки CLI и протокола отчёта, не benchmark генерации кода моделью.', 'steps': []}
            for step in result['plan']:
                if step['id'].endswith('.configuration'):
                    config_path = project / 'server.json'
                    config_path.write_text(json.dumps({'port': port, 'listen': '127.0.0.1'}), encoding='utf8')
                    assert json.loads(config_path.read_text(encoding='utf8'))['port'] == port
                    evidence = 'server.json parsed; port equals explicitly supplied input'
                elif step['id'].endswith('.server'):
                    worker = threading.Thread(target=httpd.serve_forever, daemon=True)
                    worker.start()
                    with urlopen(f'http://127.0.0.1:{port}/healthz', timeout=5) as response:
                        assert response.status == 200
                        observed = json.load(response)
                    assert observed['status'] == 'ok'
                    evidence = 'Observed HTTP 200 with JSON status=ok from the local fixture server'
                else:
                    assert observed['port'] == json.loads(config_path.read_text(encoding='utf8'))['port']
                    evidence = 'Live HTTP response port equals port parsed from server.json'
                report['steps'].append({'id': step['id'], 'status': 'passed', 'checks': [{'check': c, 'passed': True, 'evidence': evidence} for c in step['checks']]})
            incomplete = json.loads(json.dumps(report))
            incomplete['steps'][0]['checks'] = []
            send('ilang-report', incomplete, '--id', ready['revision'], fail=True)
            done = send('ilang-report', report, '--id', ready['revision'])
            assert done['session']['result']['status'] == 'reported_complete'
            captured = cli('ilang-capture', '--id', done['revision'], '--name', 'my_video_stack')
            assert len(captured['definition']['parameters']) == 3, 'lost clarification from the dialogue'
            again = send('ilang-prepare', {'skill': {'name': 'my_video_stack'}, 'goal': 'Create another stack'})
            assert len(again['session']['result']['questions']) == 3 and again['session']['result']['status'] == 'needs_input'
            assert not cli('ilang-read', '--id', ready['revision'])['current']
            installed = cli('install-codex', '--out', root / 'isolated-skill')
            skill_commands = Path(installed['skill'], 'references/commands.md').read_text(encoding='utf8')
            assert 'ilang-prepare' in skill_commands and 'new_issues' in skill_commands
            cli('ilang-disable')
            cli('ilang-list', fail=True)
            summary = {'status': 'passed', 'binary_sha256': hashlib.sha256(Path(executable).read_bytes()).hexdigest(), 'ready_plan_steps': len(result['plan']), 'questions_before_plan': 3, 'questions_on_reuse': 3, 'master_prompt_tokens': result['tokens'], 'text_budget': 8000, 'tokenizer': result['tokenizer'], 'external_model_api_calls': 0, 'observed_checks': len(report['steps']), 'cloud_calls': 0, 'executor': 'deterministic Python loopback fixture; not Codex', 'timings': timings, 'checks': ['default off', 'pinned composition', 'all questions before plan', 'real local HTTP response', 'reject missing verification evidence', 'analysis tied to plan hash', 'capture recipe with fresh parameters and dialogue questions', 'historical revision marked non-current', 'generated command skill includes workflow', 'disable prevents further use']}
            (reports / 'cli-smoke.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2)+'\n', encoding='utf8')
            (reports / 'cli-trace.json').write_text(json.dumps(trace, ensure_ascii=False, indent=2)+'\n', encoding='utf8')
            (reports / 'example-master-prompt.md').write_text(result['master_prompt']+'\n', encoding='utf8')
            (reports / 'example-composition.json').write_text(json.dumps(composed, ensure_ascii=False, indent=2)+'\n', encoding='utf8')
            print(json.dumps(summary, ensure_ascii=False, indent=2))
        finally:
            if worker:
                httpd.shutdown()
                worker.join(timeout=5)
            httpd.server_close()


if __name__ == '__main__':
    main()
