import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)));
const temporary = await mkdtemp(join(tmpdir(), 'http-wrapper-web-build-'));
const binary = join(temporary, process.platform === 'win32' ? 'web-fixture.exe' : 'web-fixture');
let child;
let stopping = false;

async function stop() {
  if (stopping) return;
  stopping = true;
  if (child && child.exitCode === null) {
    const current = child;
    // The Go fixture treats stdin EOF as shutdown and removes its runtime data.
    if (current.stdin) current.stdin.end();
    else current.kill('SIGTERM');
    const force = setTimeout(() => {
      if (current.exitCode !== null) return;
      if (process.platform === 'win32') {
        spawn('taskkill', ['/PID', String(current.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
      } else {
        try { process.kill(-current.pid, 'SIGKILL'); } catch { current.kill('SIGKILL'); }
      }
    }, 10_000);
    await new Promise((done) => current.once('exit', done));
    clearTimeout(force);
  }
  await rm(temporary, { recursive: true, force: true });
}

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, () => { void stop().then(() => process.exit(0)); });
}

async function command(program, args, cwd = root, input = 'ignore') {
  const current = spawn(program, args, { cwd, stdio: [input, 'inherit', 'inherit'], windowsHide: true, detached: process.platform !== 'win32' });
  child = current;
  const code = await new Promise((done, reject) => {
    current.once('error', (error) => { child = undefined; reject(error); });
    current.once('exit', done);
  });
  if (code !== 0 && !stopping) throw new Error(`${program} exited with ${code}`);
}

try {
  // Invoke the JS CLI directly so shutdown has no shell or npm grandchildren.
  await command(process.execPath, [join(root, 'web/node_modules/vite/bin/vite.js'), 'build'], join(root, 'web'));
  if (!stopping) await command('go', ['build', '-tags=webfixture', '-o', binary, './cmd/web-fixture']);
  if (!stopping) await command(binary, ['--runtime-dir', join(temporary, 'runtime')], root, 'pipe');
} finally {
  await stop();
}
