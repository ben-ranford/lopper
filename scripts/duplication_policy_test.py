"""Zero-pair enforcement and unchanged occurrence identity calibration."""

import copy
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


def empty_policy():
    return {'version': 1, 'families': [], 'exceptions': []}


def historical_policy(current):
    return dict(empty_policy(), families=[{'members': list(pair), 'canonical_helper': None} for pair in current])


class OccurrencePolicyTests(unittest.TestCase):
    def setUp(self):
        self.a, self.b, self.c = (function(name) for name in ('Original', 'Historical', 'New'))
        self.baseline = historical_policy(pairs(self.a, self.b))

    def test_historical_allowances_are_rejected_even_if_exact_and_unchanged(self):
        with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
            policy.evaluate(pairs(self.a, self.b), self.baseline)

    def test_initial_seed_cannot_authorize_existing_pairs(self):
        with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
            policy.validate_initial_baseline(pairs(self.a, self.b), self.baseline)

    def test_approved_base_allowance_is_rejected_when_candidate_is_empty(self):
        with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
            policy.validate_reduction(self.baseline, empty_policy())

    def test_every_pair_blocks_without_percentage_dilution(self):
        current = pairs(self.a, self.b, self.c)
        report = policy.evaluate(current, empty_policy())
        self.assertEqual(len(report['findings']), 3)
        self.assertTrue(all(entry['status'] == 'violation' for entry in report['findings']))
        self.assertEqual(policy.evaluate(current, empty_policy()), report)
        self.assertEqual(report['stale_exceptions'], [])
        self.assertEqual(report['removed_pairs'], [])
        self.assertTrue(all(entry['canonical_helper'] is None for entry in report['findings']))

    def test_all_occurrences_and_reintroductions_remain_blocking(self):
        for updated in (self.a, dict(self.a, start=100, end=110), dict(self.a, name='Renamed'),
                        dict(self.a, path='moved.go'), dict(self.a, shape='changed')):
            with self.subTest(updated=updated):
                report = policy.evaluate(pairs(updated, self.b), empty_policy())
                self.assertEqual([entry['status'] for entry in report['findings']], ['violation'])
        self.assertEqual(policy.evaluate({}, empty_policy())['findings'], [])
        self.assertEqual(len(policy.evaluate(pairs(self.a, self.b), empty_policy())['findings']), 1)

    def test_empty_policy_installation_does_not_suppress_initial_findings(self):
        current = pairs(self.a, self.b)
        policy.validate_initial_baseline(current, empty_policy())
        self.assertEqual(policy.evaluate(current, empty_policy())['findings'][0]['status'], 'violation')
        policy.validate_initial_baseline({}, empty_policy())
        policy.validate_reduction(empty_policy(), empty_policy())
        self.assertEqual(policy.evaluate({}, empty_policy())['findings'], [])

    def test_nonempty_base_and_candidate_policies_fail_even_without_findings(self):
        exception = {'finding': policy.finding_id(next(iter(pairs(self.a, self.b)))),
                     'rationale': 'different error contracts', 'owner': '@maintainer',
                     'review': '#approved', 'expires': '2030-01-01'}
        policies = [self.baseline, dict(empty_policy(), exceptions=[exception]),
                    dict(empty_policy(), exceptions=[dict(exception, expires='2000-01-01')])]
        for proposed in policies:
            with self.subTest(proposed=proposed):
                with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
                    policy.evaluate({}, proposed)
                with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
                    policy.validate_reduction(empty_policy(), proposed)
                with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
                    policy.validate_reduction(proposed, empty_policy())
                with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
                    policy.validate_initial_baseline({}, proposed)
        approved_helper = copy.deepcopy(self.baseline)
        approved_helper['families'][0]['canonical_helper'] = policy.identity(self.a)
        with self.assertRaisesRegex(policy.PolicyError, 'not permitted'):
            policy.evaluate(pairs(self.a, self.b), approved_helper)

    def test_malformed_policy_cannot_be_a_clean_scan(self):
        invalid = [None, {}, dict(empty_policy(), version=2), dict(empty_policy(), version=True),
                   dict(empty_policy(), version=1.0), dict(empty_policy(), families='directory/*'),
                   dict(empty_policy(), exceptions={}), dict(empty_policy(), extra=[])]
        for proposed in invalid:
            with self.subTest(proposed=proposed), self.assertRaises(policy.PolicyError):
                policy.evaluate({}, proposed)

    def test_reporting_is_complete_and_order_independent(self):
        current = pairs(self.a, self.b, self.c)
        report = policy.evaluate(current, empty_policy())
        self.assertEqual(report, policy.evaluate(dict(reversed(list(current.items()))), empty_policy()))
        rendered = policy.render(report)
        for finding in report['findings']:
            self.assertIn(finding['id'], rendered)
            for fn in finding['functions']:
                self.assertIn(f"{fn['path']}:{fn['start']} ({fn['name']})", rendered)
        self.assertIn('not proof of semantic equivalence', rendered)
        self.assertIn('Production clone pairs: 3; violations: 3', rendered)

    def test_checked_in_policy_has_no_historical_allowances(self):
        baseline = Path(__file__).resolve().parents[1] / '.github/duplication-baseline.json'
        self.assertEqual(json.loads(baseline.read_text()), empty_policy())

    def test_fragments_map_to_named_functions_and_ignore_tests_or_package_declarations(self):
        records = [(('source.go', 2, 4), ('copy.go', 2, 4)), (('source_test.go', 2, 4), ('copy.go', 2, 4))]
        current = policy.clone_pairs(records, [self.a, dict(self.b, path='copy.go')])
        self.assertEqual(len(current), 1)
        self.assertFalse(policy.clone_pairs([( ('source.go', 100, 110), ('copy.go', 100, 110))], [self.a, self.b]))

    def test_test_members_cannot_hide_production_members_in_detector_cycle(self):
        a, b, c, d = (('source.go', 1, 10), ('source_test.go', 1, 10), ('copy.go', 1, 10), ('copy_test.go', 1, 10))
        current = policy.clone_pairs([(a, b), (b, c), (c, d), (d, a)], [self.a, dict(self.b, path='copy.go')])
        self.assertEqual(len(current), 1)
        self.assertEqual(policy.evaluate(current, empty_policy())['findings'][0]['status'], 'violation')

    def test_receiver_change_is_a_new_occurrence(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'receiver.go'
            value_method = 'package calibration\ntype Item struct{}\nfunc (item Item) Value() int { return 1 }\n'
            source.write_text(value_method)
            command = ['go', 'run', str(Path(__file__).with_name('duplication_index.go'))]
            arguments = {'input': json.dumps([str(source)]), 'capture_output': True, 'text': True, 'check': True}
            original = json.loads(subprocess.run(command, **arguments).stdout)[0]
            historical = function('Historical')
            baseline = empty_policy()

            source.write_text(value_method.replace('(item Item)', '(item *Item)'))
            updated = json.loads(subprocess.run(command, **arguments).stdout)[0]
            self.assertEqual(original['name'], 'Item.Value')
            self.assertEqual(updated['name'], original['name'])
            self.assertNotEqual(updated['shape'], original['shape'])
            self.assertNotEqual(policy.identity(updated), policy.identity(original))
            report = policy.evaluate(pairs(updated, historical), baseline)
            self.assertEqual(report['findings'][0]['status'], 'violation')

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
