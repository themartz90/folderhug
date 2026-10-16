import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const versionPattern = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.[1-9][0-9]*)?$/;
const validVersion = value => typeof value === 'string' && versionPattern.test(value) && !value.includes('\n');
const validText = (value, limit) => typeof value === 'string' && value.trim().length > 0 && [...value].length <= limit && !/[\p{Cc}\p{Cs}]/u.test(value);

export function validateRelease(root = '.', expectedTag) {
 const raw = readFileSync(path.join(root, 'VERSION'), 'utf8');
 const version = raw.replace(/\r?\n$/, '');
 if (version !== 'dev' && !validVersion(version)) throw new Error('VERSION must be dev, vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N.');
 if (expectedTag !== undefined && (!validVersion(expectedTag) || version !== expectedTag)) throw new Error('VERSION does not match the requested release tag.');
 const dir = path.join(root, 'internal/releaseinfo/highlights');
 let found = false;
 for (const file of readdirSync(dir)) {
  if (!file.endsWith('.json')) continue;
  const tag = file.slice(0,-5);
  let record;
  try { record = JSON.parse(readFileSync(path.join(dir,file), 'utf8')); } catch { throw new Error(`${file}: invalid release highlights JSON.`); }
  if ((tag !== 'dev' && !validVersion(tag)) || !record || Array.isArray(record) ||
      Object.keys(record).sort().join(',') !== 'auto_show,highlights,summary,version' ||
      record.version !== tag || typeof record.auto_show !== 'boolean' || (tag === 'dev' && record.auto_show) ||
      !validText(record.summary,400) || !Array.isArray(record.highlights) || record.highlights.length < 1 || record.highlights.length > 8 ||
      !record.highlights.every(value => validText(value,240))) throw new Error(`${file}: invalid release highlights fields or text bounds.`);
  if (tag === version) found = true;
 }
 if (!found) throw new Error(`Missing matching release highlights for ${version}.`);
 return version;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
 try {
  if (process.argv.length > 3) throw new Error('Usage: node scripts/check-release.mjs [expected-tag]');
  console.log(`Release metadata validated: ${validateRelease('.',process.argv[2])}`);
 } catch (error) { console.error(error.message); process.exitCode = 1; }
}
