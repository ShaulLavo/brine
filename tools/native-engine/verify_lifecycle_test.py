import copy
import importlib.util
import pathlib
import unittest

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('verify_lifecycle', HERE / 'verify_lifecycle.py')
lifecycle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lifecycle)
SCHEMA = {'sections': [
    {'Typespace': {'types': [{'Product': {'elements': [{'name': 'id'}, {'name': 'sequence'}]}}, {'U64': []}]}},
    {'Types': [{'source_name': 'SequenceState', 'ty': 0}, {'source_name': 'SchemaMarker', 'ty': 1}]},
    {'Tables': [{'source_name': 'sequence_state', 'product_type_ref': 0, 'primary_key': [0, 1],
                'indexes': [{'algorithm': {'BTree': [0, 1]}}]},
                {'source_name': 'schema_marker', 'product_type_ref': 1, 'primary_key': [0]}]},
    {'ExplicitNames': {'entries': [{'Table': {'source_name': 'sequence_state', 'canonical_name': 'sequence_state'}},
                                  {'Table': {'source_name': 'schema_marker', 'canonical_name': 'schema_marker'}}]}},
    {'Reducers': [{'source_name': 'init', 'params': ['id', 'sequence']}, {'source_name': 'append', 'params': []}]}
]}


def at_path(value, path):
    for component in path:
        value = value[component]
    return value


class SchemaOrderTest(unittest.TestCase):
    def test_only_unordered_declaration_permutations_compare_equal(self):
        permuted = copy.deepcopy(SCHEMA)
        permuted['sections'][1]['Types'].reverse()
        permuted['sections'][2]['Tables'].reverse()
        permuted['sections'][3]['ExplicitNames']['entries'].reverse()
        original = copy.deepcopy(permuted)
        self.assertEqual(lifecycle.normalize_schema_declaration_order(SCHEMA),
                         lifecycle.normalize_schema_declaration_order(permuted))
        self.assertEqual(permuted, original)

    def test_changed_declared_values_still_differ(self):
        paths = [('sections', 1, 'Types', 0, 'ty'),
                 ('sections', 2, 'Tables', 0, 'product_type_ref'),
                 ('sections', 3, 'ExplicitNames', 'entries', 0, 'Table', 'canonical_name')]
        expected = lifecycle.normalize_schema_declaration_order(SCHEMA)
        for path in paths:
            changed = copy.deepcopy(SCHEMA)
            at_path(changed, path[:-1])[path[-1]] = 'changed'
            self.assertNotEqual(expected, lifecycle.normalize_schema_declaration_order(changed))

    def test_positional_collections_and_duplicate_counts_still_differ(self):
        paths = [('sections', 0, 'Typespace', 'types'),
                 ('sections', 0, 'Typespace', 'types', 0, 'Product', 'elements'),
                 ('sections', 2, 'Tables', 0, 'primary_key'),
                 ('sections', 2, 'Tables', 0, 'indexes', 0, 'algorithm', 'BTree'),
                 ('sections', 4, 'Reducers'), ('sections', 4, 'Reducers', 0, 'params')]
        expected = lifecycle.normalize_schema_declaration_order(SCHEMA)
        for path in paths:
            changed = copy.deepcopy(SCHEMA)
            at_path(changed, path).reverse()
            self.assertNotEqual(expected, lifecycle.normalize_schema_declaration_order(changed))
        changed = copy.deepcopy(SCHEMA)
        changed['sections'][1]['Types'].append(changed['sections'][1]['Types'][0])
        self.assertNotEqual(expected, lifecycle.normalize_schema_declaration_order(changed))


if __name__ == '__main__':
    unittest.main()
