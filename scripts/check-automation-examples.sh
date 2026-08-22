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
			runs << { hook: hook_name, command: command_name, run: run } if run.is_a?(String)
		end
	end

	runs
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
	words = shell_words(segment[:text])
	words = words.drop_while { |word| word.match?(/\A[A-Za-z_][A-Za-z0-9_]*=.*/)}
	words
end

def required_command_segment?(segment)
	segment[:operator] != "||"
end

def pre_commit_command_runs(runs, command_name)
	runs.select { |entry| entry[:hook] == "pre-commit" && entry[:command] == command_name }
end

def has_exact_command?(runs, command_name, words)
	runs.any? do |entry|
		entry[:hook] == "pre-commit" &&
			entry[:command] == command_name &&
			shell_segments(entry[:run]).any? do |segment|
				required_command_segment?(segment) && command_words(segment) == words
			end
	end
end

def has_lopper_json_report?(runs)
	pre_commit_command_runs(runs, "lopper-json-report").any? do |entry|
		shell_segments(entry[:run]).any? do |segment|
			next false unless required_command_segment?(segment)

			words = command_words(segment)
			next false unless words[0, 4] == ["go", "run", "./cmd/lopper", "analyse"]

			words.each_cons(2).include?(["--repo", "."]) &&
				words.each_cons(2).include?(["--language", "all"]) &&
				words.each_cons(2).include?(["--format", "json"]) &&
				words.each_cons(2).include?(["--output", ".artifacts/lopper-pre-commit.json"])
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
