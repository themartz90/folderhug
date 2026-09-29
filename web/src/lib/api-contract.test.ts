import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { parse } from 'yaml';

interface Parameter { name: string; in: string; $ref?: string }
interface Operation {
  operationId: string;
  security?: Record<string, string[]>[];
  parameters?: Parameter[];
  'x-required-scopes'?: string[];
}
interface Schema {
  properties?: Record<string, Schema>;
  required?: string[];
  additionalProperties?: boolean;
}
interface Contract {
  openapi: string;
  paths: Record<string, Record<string, Operation> & { parameters?: Parameter[] }>;
  components: { schemas: Record<string, Schema>; parameters: Record<string, Parameter> };
}

const document = parse(readFileSync(new URL('../../../api/openapi.yaml', import.meta.url), 'utf8')) as Contract;
const verbs = new Set(['get', 'post', 'patch', 'put', 'delete', 'head']);

describe('API contract boundaries', () => {
  it('resolves every local reference and gives every operation a unique ID', () => {
    const refs = new Set<string>();
    function visit(value: unknown) {
      if (!value || typeof value !== 'object') return;
      for (const [key, child] of Object.entries(value)) {
        if (key === '$ref') refs.add(String(child));
        else visit(child);
      }
    }
    visit(document);
    for (const ref of refs) {
      expect(ref.startsWith('#/')).toBe(true);
      let value: unknown = document;
      for (const key of ref.slice(2).split('/')) {
        expect(value, ref).toBeTypeOf('object');
        value = (value as Record<string, unknown>)[key];
      }
      expect(value, ref).toBeDefined();
    }
    const ids = Object.values(document.paths).flatMap((path) =>
      Object.entries(path).flatMap(([method, op]) => verbs.has(method) && !Array.isArray(op) ? [op.operationId] : []),
    );
    expect(ids.every(Boolean)).toBe(true);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('declares every path parameter on every operation', () => {
    for (const [url, path] of Object.entries(document.paths)) {
      const needed = [...url.matchAll(/\{([^}]+)\}/g)].map((match) => match[1]);
      for (const [method, operation] of Object.entries(path)) {
        if (!verbs.has(method) || Array.isArray(operation)) continue;
        const parameters = [...(path.parameters ?? []), ...(operation.parameters ?? [])].map((parameter) =>
          parameter.$ref ? document.components.parameters[parameter.$ref.split('/').at(-1) ?? ''] : parameter,
        );
        for (const name of needed) {
          expect(parameters.some((parameter) => parameter?.in === 'path' && parameter.name === name), `${method} ${url}`).toBe(true);
        }
      }
    }
  });

  it('keeps credentials out of read responses and arbitrary paths out of download requests', () => {
    expect(document.components.schemas.Profile?.properties).not.toHaveProperty('credentials');
    expect(document.components.schemas.Profile?.properties).not.toHaveProperty('password');
    expect(document.components.schemas.Token?.properties).not.toHaveProperty('secret');
    const download = document.components.schemas.DownloadCreate;
    expect(download?.additionalProperties).toBe(false);
    expect(download?.required).toEqual(['profile_id', 'entry_id', 'destination_id']);
    expect(download?.properties).not.toHaveProperty('url');
    expect(download?.properties).not.toHaveProperty('path');
  });

  it('keeps profile mutations, token administration and completed-file deletion owner-only', () => {
    for (const [path, method] of [
      ['/profiles', 'post'], ['/profiles/{id}', 'patch'], ['/profiles/{id}', 'delete'],
      ['/tokens', 'get'], ['/tokens', 'post'], ['/tokens/{id}', 'delete'],
      ['/downloads/{id}/content', 'delete'],
    ]) {
      expect(document.paths[path ?? '']?.[method ?? '']?.security).toEqual([{ owner_session: [] }]);
    }
  });
});
