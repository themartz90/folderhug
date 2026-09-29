import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';

const staged = process.argv.includes('--staged');
const git = (...args) => execFileSync('git', args, { encoding: 'utf8', maxBuffer: 20 * 1024 * 1024 });
const files = [...new Set(git('ls-files', '-z', ...(staged ? [] : ['--cached', '--others', '--exclude-standard']))
  .split('\0').filter(Boolean))];
const names = new Set(files);
const failures = [];
const privateValues = existsSync('.env') ? readFileSync('.env', 'utf8').split(/\r?\n/)
  .map((line) => line.match(/^\s*[A-Z_]+\s*=\s*(.*?)\s*$/)?.[1]?.replace(/^(['"])(.*)\1$/, '$2'))
  .filter((value) => value && value.length >= 6) : [];
const allowedRoots = new Set(['.github', 'api', 'cmd', 'docs', 'examples', 'internal', 'scripts', 'tests', 'web']);
const allowedFiles = new Set(['.dockerignore', '.gitattributes', '.gitignore', '.node-version',
  'CONTRIBUTING.md', 'Dockerfile', 'LICENSE', 'README.md', 'SECURITY.md', 'go.mod', 'go.sum',
  'compose.yaml', 'compose.media.yaml', 'compose.images.yaml']);

for (const file of files) {
  if ((!file.includes('/') && !allowedFiles.has(file)) ||
      (file.includes('/') && !allowedRoots.has(file.split('/')[0])) ||
      /(^|\/)(AGENTS\.md|\.env(?:\..*)?|node_modules|dist|test-results|playwright-report|private|\.local)(\/|$)/.test(file) ||
      /\.(db(?:-wal|-shm)?|sqlite(?:-wal|-shm)?|log|part|pem|key|pfx|p12)$/i.test(file) ||
      /^docs\/(PROJECT_PLAN|EXECUTION_PLAN|FRONTEND_SPEC|DESIGN_PREVIEW|REVIEW_HANDOFF|C8_)/.test(file) ||
      /^web\/(src\/preview|design-tests|design\.html|.*\.design\.config\.ts)/.test(file)) {
    failures.push(`${file}: private, generated, or unexpected publication path`);
  }
  const content = staged ? execFileSync('git', ['show', `:${file}`], { maxBuffer: 20 * 1024 * 1024 }) : readFileSync(file);
  if (content.includes(0)) continue;
  const text = content.toString('utf8');
  const privateScanText = text.replaceAll('themartz90/folderhug', 'public-repository');
  if (privateValues.some((value) => privateScanText.includes(value))) failures.push(`${file}: matches private local configuration`);
  if (/-----BEGIN (?:RSA |OPENSSH |EC )?PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}/.test(text)) {
    failures.push(`${file}: possible embedded credential`);
  }
  if (!file.endsWith('.md')) continue;
  if (/\u2014/.test(text)) failures.push(`${file}: em dash in public documentation`);
  if (/\.local\/|private\/project|SSH_STAGING_SERVER|C8_REVIEW_REPORT|EXECUTION_PLAN\.md/.test(text)) {
    // Native development paths are public defaults, not private evidence.
    const withoutDefaults = text.replace(/\.local\/(?:dev|data|keys|downloads|cache|worker-key)(?:\b|\/)/g, '');
    if (/\.local\/|private\/project|SSH_STAGING_SERVER|C8_REVIEW_REPORT|EXECUTION_PLAN\.md/.test(withoutDefaults)) {
      failures.push(`${file}: internal documentation reference`);
    }
  }
  for (const match of text.matchAll(/!?\[[^\]]*\]\(([^\s)]+)(?:\s+"[^"]*")?\)/g)) {
    const href = match[1];
    if (/^(?:[a-z]+:|#|\/)/i.test(href)) continue;
    const target = path.posix.normalize(path.posix.join(path.posix.dirname(file), decodeURIComponent(href.split('#')[0])));
    if (!names.has(target) && !files.some((name) => name.startsWith(`${target}/`))) {
      failures.push(`${file}: missing published link target ${target}`);
    }
  }
}
if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exitCode = 1;
} else {
  console.log(`Publication checks passed for ${files.length} ${staged ? 'staged' : 'eligible'} files.`);
}
