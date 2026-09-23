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
		{"stale-failure", "accepted:42:99"},
		{"stale-cancelled", "accepted:42:99"},
		{"stale-cancelled-after-start", "accepted:42:99"},
		{"stale-only", "Timed out"},
		{"failure", "No completed"},
		{"cancelled", "No completed"},
		{"timeout", "Timed out"},
		{"missing-run", "Timed out"},
		{"superseded", "superseded"},
		{"wrong-head", "Timed out"},
		{"wrong-name", "No completed"},
		{"expired", "No completed"},
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
// Even failed CI advertises an artifact, so only successful runs may supply it.
const suppressionArtifactWaitHarness = `
const scenario = process.env.WAIT_SCENARIO;
const start = 1000000;
let now = start;
let polls = 0;
Date.now = () => now;
global.setTimeout = (callback, ms) => {
  if (ms !== 15000 || ++polls > 260) throw new Error('unbounded polling');
  now += ms;
  callback();
};
const outputs = {};
const context = {repo: {owner: 'owner', repo: 'repo'}, payload: {pull_request: {number: 7}}};
const core = {setOutput: (key, value) => { outputs[key] = value; }};
const github = {
 rest: {actions: {listWorkflowRuns: 'runs', listWorkflowRunArtifacts: 'artifacts'},
  pulls: {get: async ({pull_number}) => {
   if (pull_number !== 7) throw new Error('wrong PR');
   return {data: {head: {sha: scenario === 'superseded' && polls > 0 ? 'new' : 'expected'}}};
  }}},
 paginate: async (method, args) => {
  if (args.owner !== 'owner' || args.repo !== 'repo') throw new Error('wrong repository');
  if (method === 'runs') {
   if (args.workflow_id !== 'ci.yml' || args.event !== 'pull_request' || args.head_sha !== 'expected') {
    throw new Error('untrusted workflow query');
   }
   if (scenario === 'missing-run') return [];
   if (scenario === 'stale-only' || (scenario.startsWith('stale-') && polls === 0)) {
    return [{id: 41, head_sha: 'expected', created_at: new Date(start - 60000).toISOString(),
     updated_at: new Date(scenario === 'stale-cancelled-after-start' ? start + 1 : start - 1).toISOString(),
     status: 'completed', conclusion: scenario.includes('cancelled') ? 'cancelled' : 'failure'}];
   }
   const pending = ['timeout', 'superseded'].includes(scenario) ||
    ((scenario === 'late-success' || scenario.startsWith('stale-')) && now - start < 30 * 60 * 1000);
   return [{id: 42, head_sha: scenario === 'wrong-head' ? 'other' : 'expected',
    created_at: new Date(start).toISOString(), updated_at: new Date(now).toISOString(), status: pending ? 'in_progress' : 'completed',
    conclusion: ['failure', 'cancelled'].includes(scenario) ? scenario : 'success'}];
  }
  if (method !== 'artifacts' || args.run_id !== 42) throw new Error('wrong artifact query');
  return [{id: 99, name: scenario === 'wrong-name' ? 'pr-report-inputs-8' : 'pr-report-inputs-7',
   expired: scenario === 'expired'}];
 }
};
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const processStub = {env: {EXPECTED_HEAD_SHA: 'expected', JOB_START_MS: String(start)}};
new AsyncFunction('github', 'context', 'core', 'process', process.env.WAIT_SCRIPT)(github, context, core, processStub)
 .then(() => console.log('accepted:' + outputs['run-id'] + ':' + outputs['artifact-id']))
 .catch(error => {
  const elapsed = now - start;
  if (['timeout', 'wrong-head', 'missing-run', 'stale-only'].includes(scenario) && (elapsed < 60 * 60 * 1000 || elapsed >= 65 * 60 * 1000)) {
   throw new Error('timeout outside verification SLO/report buffer: ' + elapsed);
  }
  if (['failure', 'cancelled', 'expired', 'wrong-name'].includes(scenario) && polls !== 0) {
   throw new Error('terminal result was polled instead of failing closed');
  }
  console.log(error.message);
 });
`
