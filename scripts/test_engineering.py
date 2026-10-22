"""Offline protocol/config checks; real deployments are covered by backend-lab."""
import importlib.util
import json
from pathlib import Path
import unittest

ROOT=Path(__file__).resolve().parent.parent
spec=importlib.util.spec_from_file_location("integration",ROOT/"scripts/verify-integrations.py")
integration=importlib.util.module_from_spec(spec); spec.loader.exec_module(integration)

class RequiredIntegrationTests(unittest.TestCase):
    def test_skip_missing_and_failure_are_never_pass(self):
        for action in ("skip","fail"):
            result=integration.classify(json.dumps({"Test":"required","Action":action}),{"required"})
            self.assertEqual(result["status"],"FAIL")
        self.assertEqual(integration.classify("",{"required"})["status"],"FAIL")
        self.assertEqual(integration.classify(json.dumps({"Test":"required","Action":"pass"}),{"required"})["status"],"PASS")
    def test_extra_failure_cannot_hide_behind_required_success(self):
        log='\n'.join(json.dumps({"Test":name,"Action":action}) for name,action in [("required","pass"),("other","fail")])
        self.assertEqual(integration.classify(log,{"required"})["status"],"FAIL")

class ContractInventoryTests(unittest.TestCase):
    def test_openapi_references_ids_and_path_parameters(self):
        paths=[ROOT/"docs/openapi.json"]
        sibling=ROOT.parent/("sql-sentinel" if ROOT.name=="AgentMesh" else "AgentMesh")/"docs/openapi.json"
        if sibling.exists(): paths.append(sibling)
        for path in paths:
            doc=json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(doc["openapi"],"3.0.3")
            ids=set()
            def refs(value):
                if isinstance(value,dict):
                    if "$ref" in value:
                        cur=doc
                        for part in value["$ref"].removeprefix("#/").split("/"):cur=cur[part]
                    for v in value.values():refs(v)
                elif isinstance(value,list):
                    for v in value:refs(v)
            refs(doc)
            for route,operations in doc["paths"].items():
                for operation in operations.values():
                    name=operation["operationId"]
                    self.assertNotIn(name,ids);ids.add(name)
                    expected={part[1:-1] for part in route.split('/') if part.startswith('{')}
                    found={x['name'] for x in operation.get('parameters',[]) if x['in']=='path' and x['required']}
                    self.assertEqual(expected,found)
                    self.assertTrue(any(code.startswith('2') for code in operation['responses']))

if __name__=="__main__": unittest.main()
