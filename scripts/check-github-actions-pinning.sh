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

paths = Dir[".github/workflows/*.yml"].sort
abort "No GitHub workflow files found under .github/workflows/*.yml." if paths.empty?

immutable_action_ref = /\A[^@\s]+@[0-9a-f]{40}\z/
errors = []

paths.each do |path|
	workflow = YAML.load_file(path)
	jobs = workflow.fetch("jobs", {})

	jobs.each do |job_name, job|
		job_uses = job["uses"]
		if job_uses && !job_uses.start_with?("./") && job_uses !~ immutable_action_ref
			errors << "#{path}: job #{job_name} uses #{job_uses.inspect}; external reusable workflows must be pinned to a 40-character commit SHA."
		end

		Array(job["steps"]).each do |step|
			uses = step["uses"]
			next if uses.nil? || uses.start_with?("./")
			next if uses.match?(immutable_action_ref)

			step_name = step["name"] || "<unnamed step>"
			errors << "#{path}: job #{job_name}, step #{step_name.inspect} uses #{uses.inspect}; pin external GitHub Actions to a 40-character commit SHA."
		end
	end
end

unless errors.empty?
	warn "GitHub Actions pinning check failed:"
	errors.each { |error| warn "  - #{error}" }
	exit 1
end

puts "GitHub Actions pinning check passed."
RUBY
