// Playwright force-terminates Windows process trees. Ask the fixture to close
// first, then wait for its supervisor to remove both runtime and build data.
export default async function teardown() {
  const control = 'http://127.0.0.1:4174';
  const headers = { 'X-Fixture-Control': 'synthetic-local-control' };
  const response = await fetch(`${control}/info`, { headers, signal: AbortSignal.timeout(2000) });
  if (!response.ok) throw new Error('Fixture cleanup could not identify its supervisor');
  const info = await response.json() as { supervisor_pid: number };
  for (const address of [control, 'http://127.0.0.1:4176']) {
    const shutdown = await fetch(`${address}/shutdown`, { method: 'POST', headers, signal: AbortSignal.timeout(2000) });
    if (shutdown.status !== 204) throw new Error('Fixture shutdown failed');
  }
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    try { process.kill(info.supervisor_pid, 0); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ESRCH') return;
      throw error;
    }
    await new Promise((done) => setTimeout(done, 50));
  }
  throw new Error('Fixture supervisor did not finish cleanup within 10 seconds');
}
