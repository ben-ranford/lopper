#!/usr/bin/env sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v ruby >/dev/null 2>&1 || {
	printf 'ruby not found in PATH (required to parse GitHub workflow YAML for action pinning).\n' >&2
	exit 1
}

ruby <<'RUBY'
require "yaml"

def load_workflow(path)
	YAML.safe_load(File.read(path), aliases: true, filename: path)
end

paths = Dir[".github/workflows/*.{yml,yaml}"].sort
abort "No GitHub workflow files found under .github/workflows/*.yml or *.yaml." if paths.empty?

immutable_action_ref = /\A[^@\s]+@[0-9a-f]{40}\z/
errors = []

def validate_external_action_ref(errors, path, context, uses, immutable_action_ref)
	return if uses.nil? || uses.start_with?("./")
	return if uses.match?(immutable_action_ref)

	errors << "#{path}: #{context} uses #{uses.inspect}; pin external GitHub Actions to a 40-character commit SHA."
end

paths.each do |path|
	workflow = load_workflow(path)
	jobs = workflow.fetch("jobs", {})

	jobs.each do |job_name, job|
		job_uses = job["uses"]
		if job_uses && !job_uses.start_with?("./") && job_uses !~ immutable_action_ref
			errors << "#{path}: job #{job_name} uses #{job_uses.inspect}; external reusable workflows must be pinned to a 40-character commit SHA."
		end

		Array(job["steps"]).each do |step|
			step_name = step["name"] || "<unnamed step>"
			validate_external_action_ref(
				errors,
				path,
				"job #{job_name}, step #{step_name.inspect}",
				step["uses"],
				immutable_action_ref,
			)
		end
	end
end

action_paths = Dir.glob("**/action.{yml,yaml}", File::FNM_DOTMATCH)
	.reject { |path| path.start_with?(".git/") }
	.sort

action_paths.each do |path|
	action = load_workflow(path)
	runs = action.fetch("runs", {})
	next unless runs.is_a?(Hash) && runs["using"] == "composite"

	Array(runs["steps"]).each do |step|
		step_name = step["name"] || "<unnamed step>"
		validate_external_action_ref(
			errors,
			path,
			"composite step #{step_name.inspect}",
			step["uses"],
			immutable_action_ref,
		)
	end
end

unless errors.empty?
	warn "GitHub Actions pinning check failed:"
	errors.each { |error| warn "  - #{error}" }
	exit 1
end

puts "GitHub Actions pinning check passed."
RUBY
