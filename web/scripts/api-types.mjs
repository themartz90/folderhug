import { mkdir, readFile, writeFile } from 'node:fs/promises';
import openapiTS, { astToString } from 'openapi-typescript';

const source = new URL('../../api/openapi.yaml', import.meta.url);
const destination = new URL('../src/lib/api.generated.ts', import.meta.url);
const generated = '// Generated from api/openapi.yaml. Run npm run api:generate to update.\n'
  + astToString(await openapiTS(source));

if (process.argv.includes('--check')) {
  const existing = await readFile(destination, 'utf8');
  if (existing.replaceAll('\r\n', '\n') !== generated.replaceAll('\r\n', '\n')) {
    console.error('API types differ from api/openapi.yaml. Run npm run api:generate.');
    process.exitCode = 1;
  }
} else {
  await mkdir(new URL('../src/lib/', import.meta.url), { recursive: true });
  await writeFile(destination, generated);
}
