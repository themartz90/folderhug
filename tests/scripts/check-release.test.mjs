import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { validateRelease } from '../../scripts/check-release.mjs';

function fixture(t, version = 'dev') {
 const root = mkdtempSync(path.join(tmpdir(), 'folderhug-release-'));
 t.after(() => rmSync(root, { recursive: true, force: true }));
 const dir = path.join(root, 'internal/releaseinfo/highlights');
 mkdirSync(dir, { recursive: true });
 writeFileSync(path.join(root, 'VERSION'), `${version}\n`);
 const notes = { version, auto_show: version !== 'dev', summary: 'Changes in this build.', highlights: ['A change.'] };
 const save = () => writeFileSync(path.join(dir, `${version}.json`), JSON.stringify(notes));
 save();
 return { root, notes, save };
}

test('development and release metadata validate with exact tag', t => {
 for (const version of ['dev', 'v1.2.3', 'v1.2.3-rc.10']) {
  const { root } = fixture(t, version);
  assert.equal(validateRelease(root), version);
  if (version !== 'dev') assert.equal(validateRelease(root, version), version);
 }
});
test('rejects invalid version, mismatched tag and missing release notes', t => {
 for (const invalid of ['v01.2.3', 'v1.2.3-rc.0', 'v1.2.3\nignored']) {
  const { root } = fixture(t, invalid.replaceAll('\n',''));
  writeFileSync(path.join(root, 'VERSION'), invalid);
  assert.throws(() => validateRelease(root), /VERSION/);
 }
 const { root } = fixture(t, 'v1.2.3');
 assert.throws(() => validateRelease(root, 'v1.2.4'), /tag/);
 rmSync(path.join(root, 'internal/releaseinfo/highlights/v1.2.3.json'));
 assert.throws(() => validateRelease(root), /highlights/);
});
test('rejects malformed records and text bounds', t => {
 for (const mutate of [n => n.version='v9.0.0', n => n.auto_show='yes', n => n.summary='a'.repeat(401), n => n.summary=' ', n => n.highlights=[], n => n.highlights=['a'.repeat(241)], n => n.highlights=Array(9).fill('a'), n => n.highlights=['line\nbreak']]) {
  const { root, notes, save } = fixture(t, 'v1.2.3');
  mutate(notes); save();
  assert.throws(() => validateRelease(root), /highlights/);
 }
});
test('development preview cannot auto-open and Unicode limits count characters', t => {
 const { root, notes, save } = fixture(t);
 notes.auto_show=true; save();
 assert.throws(() => validateRelease(root), /highlights/);
 notes.auto_show=false; notes.summary='é'.repeat(400); save();
 assert.equal(validateRelease(root), 'dev');
});
