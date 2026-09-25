package scripts

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSuppressionArtifactWaitLeavesReportingBuffer(t *testing.T) {
	t.Parallel()
	var workflow struct {
		Jobs map[string]struct {
			TimeoutMinutes int `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	if got := workflow.Jobs["verify"].TimeoutMinutes; got != 65 {
		t.Fatalf("consumer timeout = %d, want 65 minutes for 60-minute shared wait and reporting buffer", got)
	}
}

func TestSuppressionArtifactWait(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	resolve := workflowStepByName(t, workflow.Jobs, "verify", "Resolve trusted ci artifact for this pull request head")
	for _, tc := range []struct{ name, want string }{
		{"late-success", "accepted:42:99"},
		{"prestart-success", "accepted:42:99"},
		{"prestart-failure", "completed with failure"},
		{"stale-failure", "accepted:42:99"},
		{"stale-cancelled", "accepted:42:99"},
		{"same-second-replacement", "accepted:42:99"},
		{"empty-run-association", "accepted:42:99"},
		{"empty-run-association-missing", "Timed out"},
		{"empty-run-association-failure", "Timed out"},
		{"stale-only", "Timed out"},
		{"same-second-ambiguous-failure", "Timed out"},
		{"failure", "completed with failure"},
		{"cancelled", "completed with cancelled"},
		{"timeout", "Timed out"},
		{"missing-run", "Timed out"},
		{"superseded", "superseded"},
		{"wrong-head", "Timed out"},
		{"wrong-pr", "Timed out"},
		{"wrong-base", "Timed out"},
		{"wrong-name", "Timed out"},
		{"expired", "Timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("node", "-e", suppressionArtifactWaitHarness)
			cmd.Env = append(os.Environ(), "WAIT_SCENARIO="+tc.name, "WAIT_SCRIPT="+resolve.With["script"])
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), tc.want) {
				t.Fatalf("artifact wait: err=%v; want %q; output: %s", err, tc.want, output)
			}
		})
	}
}

// Execute the workflow's actual resolver while advancing time without sleeping.
// Run metadata ties candidates to the exact PR/head/base; event time identifies
// the current producer, including when Actions creates it before this job starts.
const suppressionArtifactWaitHarness = `
const scenario = process.env.WAIT_SCENARIO;
const start = 1000000;
let now = start;
let polls = 0;
let pullGets = 0;
let associationLookups = 0;
let expectedDelay = 15000;
Date.now = () => now;
global.setTimeout = (callback, ms) => {
  if (ms !== expectedDelay || ++polls > 40) throw new Error('unexpected or unbounded polling delay: ' + ms);
  expectedDelay = Math.min(expectedDelay * 2, 2 * 60 * 1000);
  now += ms;
  callback();
};
const outputs = {};
const eventUpdatedAt = scenario === 'same-second-ambiguous-failure' ? start :
 scenario === 'same-second-replacement' ? start - 1000 : start - 90000;
const context = {repo: {owner: 'owner', repo: 'repo'}, payload: {pull_request: {
 number: 7, updated_at: new Date(eventUpdatedAt).toISOString(),
 head: {sha: 'expected', ref: 'branch', repo: {full_name: 'fork/repo'}},
 base: {sha: 'base', ref: 'main', repo: {full_name: 'owner/repo'}}
}}};
const core = {setOutput: (key, value) => { outputs[key] = value; }};
const github = {
 rest: {actions: {listWorkflowRuns: 'runs', listWorkflowRunArtifacts: 'artifacts'},
  repos: {listPullRequestsAssociatedWithCommit: 'associated'},
  pulls: {get: async ({pull_number}) => {
   pullGets++;
   if (pull_number !== 7) throw new Error('wrong PR');
   return {data: {head: {sha: scenario === 'superseded' ? 'new' : 'expected'}}};
  }}},
 paginate: async (method, args) => {
  if (args.owner !== 'owner' || args.repo !== 'repo') throw new Error('wrong repository');
  if (method === 'associated') {
   associationLookups++;
   if (args.commit_sha !== 'expected') throw new Error('wrong associated commit');
   if (scenario === 'empty-run-association-missing') return [];
   return [{number: 7, head: {sha: 'expected', ref: 'branch', repo: {full_name: 'fork/repo'}},
    base: {sha: 'base', ref: 'main', repo: {full_name: 'owner/repo'}}}];
  }
  if (method === 'runs') {
   if (args.workflow_id !== 'ci.yml' || args.event !== 'pull_request' || args.head_sha !== 'expected') {
    throw new Error('untrusted workflow query');
   }
   if (scenario === 'missing-run') return [];
   if (scenario.startsWith('prestart-')) {
    return [{id: 42, head_sha: 'expected', created_at: new Date(start - 60000).toISOString(),
     pull_requests: [{number: 7, head: {sha: 'expected'}, base: {sha: 'base'}}],
     updated_at: new Date(now).toISOString(), status: 'completed', conclusion: scenario === 'prestart-failure' ? 'failure' : 'success'}];
   }
   if (scenario === 'stale-only' || (scenario.startsWith('stale-') && polls === 0)) {
    return [{id: 41, head_sha: 'expected', created_at: new Date(start - 120000).toISOString(),
     pull_requests: [{number: 7, head: {sha: 'expected'}, base: {sha: 'base'}}],
     updated_at: new Date(start - 1).toISOString(),
     status: 'completed', conclusion: scenario.includes('cancelled') ? 'cancelled' : 'failure'}];
   }
   if (scenario === 'same-second-replacement') {
    return [
     {id: 41, head_sha: 'expected', created_at: new Date(start).toISOString(), status: 'completed', conclusion: 'cancelled',
      pull_requests: [{number: 7, head: {sha: 'expected'}, base: {sha: 'base'}}]},
     {id: 42, head_sha: 'expected', created_at: new Date(start).toISOString(), updated_at: new Date(now).toISOString(),
      status: now - start < 30 * 60 * 1000 ? 'in_progress' : 'completed', conclusion: 'success',
      pull_requests: [{number: 7, head: {sha: 'expected'}, base: {sha: 'base'}}]},
    ];
   }
   const pending = ['timeout', 'superseded'].includes(scenario) ||
    ((scenario === 'late-success' || scenario.startsWith('stale-')) && now - start < 30 * 60 * 1000);
   return [{id: 42, head_sha: scenario === 'wrong-head' ? 'other' : 'expected',
    created_at: new Date(start).toISOString(), updated_at: new Date(now).toISOString(), status: pending ? 'in_progress' : 'completed',
    pull_requests: scenario.startsWith('empty-run-association') ? [] :
     [{number: scenario === 'wrong-pr' ? 8 : 7, head: {sha: 'expected'}, base: {sha: scenario === 'wrong-base' ? 'old-base' : 'base'}}],
    conclusion: ['failure', 'cancelled', 'empty-run-association-failure', 'same-second-ambiguous-failure'].includes(scenario) ?
     (['empty-run-association-failure', 'same-second-ambiguous-failure'].includes(scenario) ? 'failure' : scenario) : 'success'}];
  }
  if (method !== 'artifacts' || args.run_id !== 42) throw new Error('wrong artifact query');
  return [{id: 99, name: scenario === 'wrong-name' ? 'pr-report-inputs-8' : 'pr-report-inputs-7',
   expired: scenario === 'expired'}];
 }
};
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const processStub = {env: {EXPECTED_HEAD_SHA: 'expected', JOB_START_MS: String(start)}};
new AsyncFunction('github', 'context', 'core', 'process', process.env.WAIT_SCRIPT)(github, context, core, processStub)
 .then(() => {
  if (pullGets !== 1 || associationLookups !== 1) throw new Error('PR identity APIs must be checked once, not on every poll: ' + pullGets + '/' + associationLookups);
  console.log('accepted:' + outputs['run-id'] + ':' + outputs['artifact-id']);
 })
 .catch(error => {
  const elapsed = now - start;
  const currentFailure = ['prestart-failure', 'failure', 'cancelled'].includes(scenario);
  if (scenario !== 'superseded' && !currentFailure && (elapsed < 60 * 60 * 1000 || elapsed >= 65 * 60 * 1000)) {
   throw new Error('timeout outside verification SLO/report buffer: ' + elapsed);
  }
  console.log(error.message);
 });
`
