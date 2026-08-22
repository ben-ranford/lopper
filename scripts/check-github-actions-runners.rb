#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

def load_workflow(path)
	YAML.safe_load(File.read(path), aliases: true, filename: path)
end

allowed_runners = (ENV["APPROVED_GITHUB_RUNNERS"] || "ubuntu-latest,ubuntu-24.04-arm,macos-26,macos-26-intel")
	.split(",")
	.map(&:strip)
	.reject(&:empty?)

if allowed_runners.empty?
	warn "APPROVED_GITHUB_RUNNERS resolved to an empty runner allowlist."
	exit 1
end

paths = Dir[".github/workflows/*.{yml,yaml}"].sort
if paths.empty?
	warn "No GitHub workflow files found under .github/workflows/*.yml or *.yaml."
	exit 1
end

errors = []

def matrix_values(job, key)
	matrix = job.dig("strategy", "matrix")
	return [] unless matrix.is_a?(Hash)

	values = []
	raw_values = matrix[key]
	values.concat(Array(raw_values).compact) if raw_values

	Array(matrix["include"]).each do |entry|
		next unless entry.is_a?(Hash) && entry.key?(key)

		values << entry[key]
	end

	values.uniq
end

paths.each do |path|
	workflow = load_workflow(path)
	workflow.fetch("jobs", {}).each do |job_name, job|
		runs_on = job["runs-on"]
		next if runs_on.nil?

		case runs_on
		when String
			matrix_match = runs_on.match(/\A\$\{\{\s*matrix\.([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}\z/)
			if matrix_match
				key = matrix_match[1]
				values = matrix_values(job, key)
				if values.empty?
					errors << "#{path}: job #{job_name} uses #{runs_on.inspect}, but strategy.matrix.#{key} has no static values to validate."
				end
				values.each do |value|
					next if allowed_runners.include?(value)

					errors << "#{path}: job #{job_name} matrix.#{key} includes unapproved runner #{value.inspect}; allowed runners: #{allowed_runners.join(", ")}."
				end
			elsif !allowed_runners.include?(runs_on)
				errors << "#{path}: job #{job_name} uses unapproved runner #{runs_on.inspect}; allowed runners: #{allowed_runners.join(", ")}."
			end
		else
			errors << "#{path}: job #{job_name} uses unsupported runs-on form #{runs_on.inspect}; use an approved literal runner or a static matrix runner."
		end
	end
end

unless errors.empty?
	warn "GitHub Actions runner allowlist check failed:"
	errors.each { |error| warn "  - #{error}" }
	exit 1
end

puts "GitHub Actions runner allowlist check passed."
