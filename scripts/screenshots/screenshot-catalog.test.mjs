import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';

import { parse } from 'yaml';

const root = path.resolve(import.meta.dirname, '../..');

test('screenshots page uses only unannotated scenarios', async () => {
  const [catalogue, showcase] = await Promise.all([
    readFile(path.join(root, 'docs/_data/screenshots.yml'), 'utf8'),
    readFile(path.join(root, 'docs/screenshots/index.md'), 'utf8'),
  ]);
  const scenarios = new Map(parse(catalogue).map((scenario) => [scenario.id, scenario]));
  const ids = [...showcase.matchAll(/screenshot-pair\.html id="([^"]+)"/g)].map((match) => match[1]);

  for (const id of ids) {
    const scenario = scenarios.get(id);
    assert.ok(scenario, `Screenshots page references unknown scenario: ${id}`);
    assert.equal(scenario.annotations?.length ?? 0, 0, `Screenshots page scenario must not have annotations: ${id}`);
  }
});

test('guide screenshot includes reference catalogue scenarios', async () => {
  const [catalogue, ...guides] = await Promise.all([
    readFile(path.join(root, 'docs/_data/screenshots.yml'), 'utf8'),
    ...[
      'first-time-setup.md', 'browsing.md', 'downloading.md', 'shelves.md', 'account.md',
      'user-management.md', 'server-management.md', 'library-management.md', 'admin-cli.md',
    ].map((file) => readFile(path.join(root, 'docs/user-guide', file), 'utf8')),
  ]);
  const scenarios = new Set(parse(catalogue).map((scenario) => scenario.id));

  for (const guide of guides) {
    for (const match of guide.matchAll(/screenshot-pair\.html id="([^"]+)"/g)) {
      assert.ok(scenarios.has(match[1]), `Guide references unknown screenshot scenario: ${match[1]}`);
    }
  }
});

test('catalogue covers every rendered application page', async () => {
  const catalogue = await readFile(path.join(root, 'docs/_data/screenshots.yml'), 'utf8');
  const ids = new Set(parse(catalogue).map((scenario) => scenario.id));
  const required = [
    'login', 'first-admin-setup', 'forgot-password', 'reset-password-invalid', 'invite-accept-invalid',
    'library-grid', 'book-modal', 'authors', 'series', 'favourites-library', 'shelf-settings',
    'account-password', 'account-preferences', 'account-shelves',
    'admin-users', 'admin-shelves', 'admin-server', 'admin-tasks', 'admin-settings', 'admin-smtp',
    'enrichment-chaptarr', 'metadata-edit',
  ];

  for (const id of required) assert.ok(ids.has(id), `Missing screenshot coverage for application page: ${id}`);
});
