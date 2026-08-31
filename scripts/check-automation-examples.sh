#!/usr/bin/env sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v ruby >/dev/null 2>&1 || {
	printf 'ruby not found in PATH (required to parse automation example YAML).\n' >&2
	exit 1
}

example=examples/lefthook.yml
if [ ! -f "$example" ]; then
	printf 'Missing %s checked-in automation example.\n' "$example" >&2
	exit 1
fi

ruby - "$example" <<'RUBY'
require "shellwords"
require "yaml"

example = ARGV.fetch(0)

def load_yaml(path)
	YAML.safe_load(File.read(path), aliases: true, filename: path)
end

def command_runs(config)
	runs = []
	return runs unless config.is_a?(Hash)

	config.each do |hook_name, hook_config|
		next unless hook_config.is_a?(Hash)

		commands = hook_config["commands"]
		next unless commands.is_a?(Hash)

		commands.each do |command_name, command_config|
			next unless command_config.is_a?(Hash)

			run = command_config["run"]
			next unless run.is_a?(String)

			runs << {
				hook: hook_name,
				command: command_name,
				run: run,
				conditional: conditionally_disabled?(hook_config) || conditionally_disabled?(command_config),
			}
		end
	end

	runs
end

def meaningful_condition_value?(value)
	case value
	when nil, false
		false
	when String
		!value.empty?
	when Array, Hash
		!value.empty?
	else
		true
	end
end

def conditionally_disabled?(config)
	%w[skip only root glob exclude files file_types].any? do |key|
		meaningful_condition_value?(config[key])
	end
end

def shell_segments(run)
	segments = []
	buffer = +""
	operator = nil
	single_quoted = false
	double_quoted = false
	escaped = false
	index = 0

	while index < run.length
		char = run[index]
		next_char = run[index + 1]

		if escaped
			buffer << char
			escaped = false
			index += 1
			next
		end

		if char == "\\" && !single_quoted
			buffer << char
			escaped = true
			index += 1
			next
		end

		if char == "'" && !double_quoted
			single_quoted = !single_quoted
			buffer << char
			index += 1
			next
		end

		if char == '"' && !single_quoted
			double_quoted = !double_quoted
			buffer << char
			index += 1
			next
		end

		if !single_quoted && !double_quoted && ((char == "&" && next_char == "&") || (char == "|" && next_char == "|"))
			segment = buffer.strip
			segments << { operator: operator, text: segment } unless segment.empty?
			operator = char + next_char
			buffer = +""
			index += 2
			next
		end

		buffer << char
		index += 1
	end

	segment = buffer.strip
	segments << { operator: operator, text: segment } unless segment.empty?
	segments
end

def shell_words(segment)
	Shellwords.split(segment)
rescue ArgumentError
	[]
end

def command_words(segment)
	shell_words(segment[:text])
end

def known_failure_command?(words)
	words == ["false"]
end

def shell_terminating_command?(words)
	%w[exit exec].include?(words[0])
end

def breaks_and_chain?(words)
	known_failure_command?(words) || shell_terminating_command?(words)
end

def fallback_masks_chain_success?(segments, index)
	later_segments = segments[(index + 1)..-1] || []
	later_segments.any? { |segment| segment[:operator] == "||" }
end

def skipped_by_known_failed_and_chain?(segments, index)
	segments[0...index].any? do |segment|
		breaks_and_chain?(command_words(segment))
	end && segment_and_chain?(segments[index])
end

def segment_and_chain?(segment)
	segment[:operator] == "&&"
end

def required_command_segment?(segments, index)
	segment = segments[index]
	return false if segment[:operator] == "||"
	return false if fallback_masks_chain_success?(segments, index)
	return false if skipped_by_known_failed_and_chain?(segments, index)

	true
end

def pre_commit_command_runs(runs, command_name)
	runs.select { |entry| entry[:hook] == "pre-commit" && entry[:command] == command_name }
end

def has_exact_command?(runs, command_name, words)
	runs.any? do |entry|
		segments = shell_segments(entry[:run])
		entry[:hook] == "pre-commit" &&
			entry[:command] == command_name &&
			!entry[:conditional] &&
			segments.each_with_index.any? do |segment, index|
				required_command_segment?(segments, index) && command_words(segment) == words
			end
	end
end

def parse_scalar_argument(words, index, flag)
	word = words[index]
	if word == flag
		value = words[index + 1]
		return nil if value.nil? || value.start_with?("-")

		return [value, index + 2]
	end

	prefix = "#{flag}="
	return [word[prefix.length, word.length], index + 1] if word.start_with?(prefix)

	nil
end

def lopper_json_report_arguments(words)
	expected_values = {
		top: "20",
		repo: ".",
		language: "all",
		format: "json",
		output: ".artifacts/lopper-pre-commit.json",
	}
	actual_values = Hash.new { |hash, key| hash[key] = [] }
	index = 4

	while index < words.length
		word = words[index]
		return nil if word == "--"

		case word
		when "--top", /\A--top=/
			parsed = parse_scalar_argument(words, index, "--top")
			return nil if parsed.nil?
			actual_values[:top] << parsed[0]
			index = parsed[1]
		when "--repo", /\A--repo=/
			parsed = parse_scalar_argument(words, index, "--repo")
			return nil if parsed.nil?
			actual_values[:repo] << parsed[0]
			index = parsed[1]
		when "--language", /\A--language=/
			parsed = parse_scalar_argument(words, index, "--language")
			return nil if parsed.nil?
			actual_values[:language] << parsed[0]
			index = parsed[1]
		when "--format", /\A--format=/
			parsed = parse_scalar_argument(words, index, "--format")
			return nil if parsed.nil?
			actual_values[:format] << parsed[0]
			index = parsed[1]
		when "--output", /\A--output=/
			parsed = parse_scalar_argument(words, index, "--output")
			return nil if parsed.nil?
			actual_values[:output] << parsed[0]
			index = parsed[1]
		when "-o"
			parsed = parse_scalar_argument(words, index, "-o")
			return nil if parsed.nil?
			actual_values[:output] << parsed[0]
			index = parsed[1]
		else
			return nil
		end
	end

	return nil unless expected_values.all? { |flag, value| actual_values[flag] == [value] }

	actual_values
end

def has_lopper_json_report?(runs)
	pre_commit_command_runs(runs, "lopper-json-report").any? do |entry|
		next false if entry[:conditional]

		segments = shell_segments(entry[:run])
		segments.each_with_index.any? do |segment, index|
			next false unless required_command_segment?(segments, index)

			words = command_words(segment)
			next false unless words[0, 4] == ["go", "run", "./cmd/lopper", "analyse"]

			!lopper_json_report_arguments(words).nil?
		end
	end
end

config = load_yaml(example)
runs = command_runs(config)
missing = []
missing << "automation integrity command" unless has_exact_command?(runs, "automation-integrity", ["make", "automation-integrity"])
missing << "lopper JSON report command" unless has_lopper_json_report?(runs)
missing << "mutation guard command" unless has_exact_command?(runs, "mutation-guard", ["git", "diff", "--exit-code", "--", ".", ":!.artifacts"])

unless missing.empty?
	warn "#{example} must preserve the automation example contract:"
	missing.each { |contract| warn "  - missing #{contract}" }
	exit 1
end

RUBY

printf 'Automation examples preserve JSON and mutation-guard contracts.\n'
