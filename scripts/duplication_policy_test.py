"""Calibration and occurrence identity regressions for the reviewed clone ratchet."""

import copy
import datetime
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import duplication_policy as policy


def function(name, path='source.go', shape='a', start=1):
    return {'name': name, 'path': path, 'shape': shape, 'start': start, 'end': start + 10}


def pairs(*functions):
    return {tuple(sorted((policy.identity(left), policy.identity(right)))): sorted((left, right), key=policy.identity)
            for i, left in enumerate(functions) for right in functions[i+1:]}


class OccurrencePolicyTests(unittest.TestCase):
    def setUp(self):
        self.a, self.b, self.c = (function(name) for name in ('Original', 'Historical', 'New'))
        self.baseline = policy.propose_baseline(pairs(self.a, self.b))

    def test_new_member_of_historical_family_fails_without_percentage_dilution(self):
        current = pairs(self.a, self.b, self.c)
        report = policy.evaluate(current, self.baseline)
        self.assertEqual([entry['status'] for entry in report['findings']].count('violation'), 2)
        self.assertEqual(policy.evaluate(current, self.baseline), report)  # unrelated nonmatches never enter the denominator

    def test_historical_clones_and_line_movement_pass(self):
        moved = dict(self.a, start=100, end=110)
        self.assertEqual(policy.evaluate(pairs(moved, self.b), self.baseline)['findings'][0]['status'], 'historical')

    def test_rename_move_and_structural_edit_are_new_occurrences(self):
        for updated in (dict(self.a, name='Renamed'), dict(self.a, path='moved.go'), dict(self.a, shape='changed')):
            self.assertEqual(policy.evaluate(pairs(updated, self.b), self.baseline)['findings'][0]['status'], 'violation')

    def test_deletions_and_reductions_are_supported(self):
        report = policy.evaluate({}, self.baseline)
        self.assertEqual(report['findings'], [])
        self.assertEqual(len(report['removed_pairs']), 1)
        reduced = policy.propose_baseline({})
        self.assertEqual(policy.evaluate({}, reduced)['removed_pairs'], [])

    def test_reviewed_policy_allows_only_reductions(self):
        policy.validate_reduction(self.baseline, policy.propose_baseline({}))
        expanded = policy.propose_baseline(pairs(self.a, self.b, self.c))
        with self.assertRaisesRegex(policy.PolicyError, 'Baseline expansion'):
            policy.validate_reduction(self.baseline, expanded)
        proposed = copy.deepcopy(self.baseline)
        proposed['exceptions'] = [{'finding': 'new'}]
        with self.assertRaisesRegex(policy.PolicyError, 'Exception expansion'):
            policy.validate_reduction(self.baseline, proposed)
        proposed = copy.deepcopy(self.baseline)
        proposed['families'][0]['canonical_helper'] = policy.identity(self.a)
        with self.assertRaisesRegex(policy.PolicyError, 'helper changes'):
            policy.validate_reduction(self.baseline, proposed)

    def test_malformed_policy_cannot_be_a_clean_scan(self):
        for invalid in (None, {}, {'version': 2, 'families': [], 'exceptions': []},
                        {'version': 1, 'families': 'directory/*', 'exceptions': []},
                        {'version': 1, 'families': [{'members': ['directory/*'], 'canonical_helper': None}], 'exceptions': []}):
            with self.assertRaises(policy.PolicyError):
                policy.evaluate({}, invalid)

    def test_exception_is_exact_documented_and_expiring(self):
        current = pairs(self.a, self.c)
        key = policy.finding_id(next(iter(current)))
        exception = {'finding': key, 'owner': '@maintainer', 'rationale': 'different error contracts',
                     'review': '#review', 'expires': '2030-01-01'}
        self.baseline['exceptions'] = [exception]
        self.assertEqual(policy.evaluate(current, self.baseline, datetime.date(2026, 1, 1))['findings'][0]['status'], 'exception')
        self.assertEqual(policy.evaluate({}, self.baseline)['stale_exceptions'], [key])
        expired = datetime.date(2031, 1, 1)
        with self.assertRaisesRegex(policy.PolicyError, 'Expired'):
            policy.evaluate(current, self.baseline, expired)
        for field in exception:
            invalid = copy.deepcopy(self.baseline)
            invalid['exceptions'][0][field] = ''
            with self.assertRaises(policy.PolicyError):
                policy.evaluate(current, invalid)
        exception['finding'] = 'source/*'
        with self.assertRaises(policy.PolicyError):
            policy.evaluate(current, self.baseline)

    def test_reporting_and_baseline_are_order_independent(self):
        current = pairs(self.a, self.b, self.c)
        reversed_pairs = dict(reversed(list(current.items())))
        self.assertEqual(policy.propose_baseline(current), policy.propose_baseline(reversed_pairs))
        self.assertEqual(policy.evaluate(current, self.baseline), policy.evaluate(reversed_pairs, self.baseline))
        rendered = policy.render(policy.evaluate(current, self.baseline))
        self.assertIn('source.go:1 (New)', rendered)
        self.assertIn('not proof of semantic equivalence', rendered)

    def test_fragments_map_to_named_functions_and_ignore_tests_or_package_declarations(self):
        records = [(('source.go', 2, 4), ('copy.go', 2, 4)), (('source_test.go', 2, 4), ('copy.go', 2, 4))]
        current = policy.clone_pairs(records, [self.a, dict(self.b, path='copy.go')])
        self.assertEqual(len(current), 1)
        self.assertFalse(policy.clone_pairs([( ('source.go', 100, 110), ('copy.go', 100, 110))], [self.a, self.b]))

    def test_test_members_cannot_hide_production_members_in_detector_cycle(self):
        a, b, c, d = (('source.go', 1, 10), ('source_test.go', 1, 10), ('copy.go', 1, 10), ('copy_test.go', 1, 10))
        current = policy.clone_pairs([(a, b), (b, c), (c, d), (d, a)], [self.a, dict(self.b, path='copy.go')])
        self.assertEqual(len(current), 1)
        self.assertEqual(policy.evaluate(current, policy.propose_baseline({}))['findings'][0]['status'], 'violation')

    def test_go_ast_identity_calibration(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'calibration.go'
            source.write_text('''package calibration
func First(input int) int { if input < 2 { return input + 1 }; return input * 3 }
func Renamed(value int) int { if value < 2 { return value + 1 }; return value * 3 }
func Literals(value int) int { if value < 99 { return value + 8 }; return value * 42 }
func Different(value int) int { for value < 10 { value++ }; return value }
func ErrorSemantics(value int) int { if value > 2 { return value - 1 }; return value / 3 }
''')
            command = ['go', 'run', str(Path(__file__).with_name('duplication_index.go'))]
            result = subprocess.run(command, input=json.dumps([str(source)]), capture_output=True, text=True, check=True)
            indexed = json.loads(result.stdout)
            self.assertEqual(indexed[0]['shape'], indexed[1]['shape'])
            self.assertEqual(indexed[0]['shape'], indexed[2]['shape'])
            self.assertEqual(indexed[0]['shape'], indexed[4]['shape'])  # dupl ignores values; never claim equivalence
            self.assertNotEqual(indexed[0]['shape'], indexed[3]['shape'])
            self.assertEqual(indexed[0]['name'], 'First')
            source.write_text('\n\n' + source.read_text())
            shifted = subprocess.run(command, input=json.dumps([str(source)]), capture_output=True, text=True, check=True)
            moved = json.loads(shifted.stdout)
            self.assertEqual(moved[0]['shape'], indexed[0]['shape'])
            self.assertEqual(moved[0]['start'], indexed[0]['start'] + 2)
            source.write_text('package broken\nfunc (')
            self.assertNotEqual(subprocess.run(command, input=json.dumps([str(source)]), capture_output=True, text=True).returncode, 0)


if __name__ == '__main__':
    unittest.main()
