'use strict';

const assert = require('node:assert/strict');

const HEAD = 'a'.repeat(40);
const BASE = 'b'.repeat(40);
const MERGE = 'c'.repeat(40);
const API = 'https://api.github.com/repos/ben-ranford/lopper';
const WEB = 'https://github.com/ben-ranford/lopper';
const CREATED = '2026-10-01T01:00:00Z';
const FINISHED = '2026-10-01T02:00:00Z';
const REPO = { id: 1155023607, full_name: 'ben-ranford/lopper', name: 'lopper', url: API };
const CI_ID = 232814257;
const WINDOWS_ID = 354077369;
const ARTIFACT_ID = 9876543210;
const PATHS = ['.github/workflows/ci.yml', '.github/workflows/ci-tests.yml', '.github/workflows/windows-runtime.yml'];
const JOBS = [
  ['verify-checks', 'ubuntu-latest'], ['publish-pr-reports', 'ubuntu-latest'],
  ['verification checks (rolling)', 'ubuntu-latest'], ['verify-tests / tests', 'ubuntu-latest'],
  ['verify-rolling-tests / tests', 'ubuntu-latest'], ['regression-proof-windows', 'windows-latest'],
  ['verify', 'ubuntu-latest'], ['verify (rolling)', 'ubuntu-latest'],
  ['os-smoke (ubuntu-latest)', 'ubuntu-latest'], ['os-smoke (macos-26)', 'macos-26'],
  ['vscode-smoke (ubuntu-latest)', 'ubuntu-latest'], ['vscode-smoke (macos-26)', 'macos-26'],
  ['homebrew-tap-verify', 'ubuntu-latest'],
];

function run(workflow, id = workflow === CI_ID ? 100 : 200) {
  const ci = workflow === CI_ID;
  return {
    id, run_number: id, run_attempt: 1, workflow_id: workflow,
    name: ci ? 'ci' : 'windows runtime', path: ci ? PATHS[0] : PATHS[2],
    event: 'pull_request', head_sha: HEAD, head_branch: 'feature',
    repository: { ...REPO }, head_repository: { ...REPO },
    url: `${API}/actions/runs/${id}`, html_url: `${WEB}/actions/runs/${id}`,
    created_at: CREATED, updated_at: FINISHED, status: 'completed', conclusion: 'success',
    pull_requests: [{
      id: 300, number: 1777, url: `${API}/pulls/1777`,
      head: { sha: HEAD, ref: 'feature', repo: { ...REPO } },
      base: { sha: BASE, ref: 'main', repo: { ...REPO } },
    }],
    referenced_workflows: ci ? [{ path: `ben-ranford/lopper/${PATHS[1]}@${MERGE}`, sha: MERGE, ref: 'refs/pull/1777/merge' }] : [],
  };
}

function jobsFor(selected) {
  const names = selected.workflow_id === CI_ID ? [...JOBS, [`suppression-artifact-${ARTIFACT_ID}`, 'ubuntu-latest']] : [['runtime-cancellation', 'windows-latest']];
  return names.map(([name, label], index) => ({
    id: selected.id * 100 + index, run_id: selected.id, run_attempt: selected.run_attempt,
    workflow_name: selected.name, head_sha: HEAD, head_branch: 'feature', name,
    run_url: selected.url, url: `${API}/actions/jobs/${selected.id * 100 + index}`,
    html_url: `${WEB}/actions/runs/${selected.id}/job/${selected.id * 100 + index}`,
    status: 'completed', conclusion: 'success', labels: [label],
    runner_id: 100000 + index, runner_group_id: 0, runner_name: `Hosted Agent ${index}`,
    created_at: CREATED, started_at: CREATED, completed_at: FINISHED,
  }));
}

function harness(options = {}) {
  const runs = options.runs ?? [run(CI_ID), run(WINDOWS_ID)];
  const requests = [];
  const contents = [];
  const pull = {
    id: 300, number: 1777, state: 'open', draft: false,
    head: { sha: HEAD, ref: 'feature', repo: { ...REPO } },
    base: { sha: BASE, ref: 'main', repo: { ...REPO } },
  };
  const input = {
    owner: 'ben-ranford', repo: 'lopper', pullNumber: 1777,
    headSHA: HEAD, baseSHA: BASE, baseRef: 'main', trustedPolicySHA: BASE,
    ciNotBefore: '2026-10-01T00:59:59Z',
    github: { rest: {
      pulls: { get: async () => ({ data: options.pull ? options.pull(structuredClone(pull)) : pull }) },
      repos: { getContent: async (args) => {
        contents.push(args);
        const data = { type: 'file', path: args.path, sha: String(PATHS.indexOf(args.path) + 4).repeat(40) };
        return { data: options.source ? options.source(data, args) : data };
      } },
      git: { getCommit: async (args) => {
        assert.equal(args.commit_sha, MERGE);
        const data = { sha: MERGE, parents: [{ sha: BASE }, { sha: HEAD }] };
        return { data: options.merge ? options.merge(data) : data };
      } },
    } },
    fetchImpl: async (address, init) => {
      const url = new URL(address);
      requests.push({ url, init });
      assert.equal(url.origin, 'https://api.github.com');
      let data;
      const workflow = /^\/repos\/ben-ranford\/lopper\/actions\/workflows\/(\d+)\/runs$/.exec(url.pathname);
      const jobs = /^\/repos\/ben-ranford\/lopper\/actions\/runs\/(\d+)\/jobs$/.exec(url.pathname);
      if (workflow) {
        assert.equal(url.searchParams.get('event'), 'pull_request');
        assert.equal(url.searchParams.get('head_sha'), HEAD);
        const selected = runs.filter((candidate) => candidate.workflow_id === Number(workflow[1]));
        const offset = (Number(url.searchParams.get('page')) - 1) * 100;
        data = { total_count: selected.length, workflow_runs: selected.slice(offset, offset + 100) };
      } else if (jobs) {
        assert.equal(url.searchParams.get('filter'), 'all');
        const selected = runs.find((candidate) => candidate.id === Number(jobs[1]));
        const listed = options.jobs ? options.jobs(jobsFor(selected), selected) : jobsFor(selected);
        const offset = (Number(url.searchParams.get('page')) - 1) * 100;
        data = { total_count: listed.length, jobs: listed.slice(offset, offset + 100) };
      } else {
        const selected = runs.find((candidate) => url.pathname === `/repos/ben-ranford/lopper/actions/runs/${candidate.id}`);
        assert.ok(selected, url.pathname);
        data = options.reread ? options.reread(structuredClone(selected)) : selected;
      }
      if (options.response) data = options.response(structuredClone(data), url);
      return new Response(JSON.stringify(data), { headers: { 'content-type': 'application/json' } });
    },
  };
  return { input, requests, contents };
}

module.exports = { HEAD, BASE, MERGE, API, WEB, CREATED, FINISHED, REPO, CI_ID, WINDOWS_ID, ARTIFACT_ID, PATHS, JOBS, run, jobsFor, harness };
