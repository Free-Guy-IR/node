import os
import sys
import tempfile
import unittest

scripts = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, scripts)

import xray_guard_replay as replay

patches = os.path.join(os.path.dirname(scripts), "patches")


def changed(path):
    return "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old\n+new\n" % (path, path, path, path)


def renamed(old, new):
    return "diff --git a/%s b/%s\nsimilarity index 100%%\nrename from %s\nrename to %s\n" % (old, new, old, new)


class Classification(unittest.TestCase):
    def test_a_changed_test_file_is_test_only(self):
        self.assertTrue(replay.test_only(changed("app/dns/dns_test.go")))
        self.assertTrue(replay.test_only(changed("testing/trustedheader/trustedheader.go")))

    def test_a_changed_product_file_is_a_target(self):
        self.assertFalse(replay.test_only(changed("app/dns/dnscommon.go")))
        self.assertFalse(replay.test_only(changed("app/dns/config.proto")))

    def test_a_test_file_parked_under_a_name_go_never_builds_is_test_only(self):
        self.assertTrue(replay.test_only(renamed("common/protocol/http/headers_test.go", "common/protocol/http/headers_test.go.upstream")))

    def test_a_product_file_parked_under_a_name_go_never_builds_is_a_target(self):
        self.assertFalse(replay.test_only(renamed("common/protocol/http/headers.go", "common/protocol/http/headers.go.upstream")))

    def test_a_test_file_renamed_into_a_product_file_is_a_target(self):
        self.assertFalse(replay.test_only(renamed("app/dns/dns_test.go", "app/dns/dns_fixture.go")))

    def test_a_test_file_renamed_to_a_name_ending_in_go_is_a_target(self):
        self.assertFalse(replay.test_only(renamed("app/dns/dns_test.go", "app/dns/dns_test.go.go")))

    def test_a_test_file_moved_somewhere_else_is_a_target(self):
        self.assertFalse(replay.test_only(renamed("app/dns/dns_test.go", "app/dns/fixtures/dns.txt")))

    def test_a_test_file_renamed_to_another_test_file_is_test_only(self):
        self.assertTrue(replay.test_only(renamed("app/dns/dns_test.go", "app/dns/dns_upstream_test.go")))


class Targets(unittest.TestCase):
    def write(self, directory, name, sections):
        with open(os.path.join(directory, name), "w") as patch:
            patch.write("Subject: fixture\n\n---\n" + "".join(sections))

    def test_parked_test_files_are_not_targets_and_their_product_files_are(self):
        with tempfile.TemporaryDirectory() as directory:
            self.write(directory, "xray-fixture.patch", [
                changed("common/protocol/http/headers.go"),
                renamed("common/protocol/http/headers_test.go", "common/protocol/http/headers_test.go.upstream"),
                changed("common/protocol/http/headers_fork_test.go"),
                renamed("common/protocol/http/old.go", "common/protocol/http/old.go.upstream"),
            ])
            self.write(directory, "other-fixture.patch", [changed("main.go")])
            self.assertEqual(replay.replay_targets(directory, None), [
                ("xray-fixture.patch", "common/protocol/http/headers.go"),
                ("xray-fixture.patch", "common/protocol/http/old.go.upstream"),
            ])

    def test_only_narrows_the_targets(self):
        with tempfile.TemporaryDirectory() as directory:
            self.write(directory, "xray-fixture.patch", [changed("a.go"), changed("b.go")])
            self.assertEqual(replay.replay_targets(directory, {"xray-fixture.patch:b.go"}), [("xray-fixture.patch", "b.go")])

    def test_the_shipped_patches_park_test_files_and_none_of_them_is_a_target(self):
        parked = []
        for name in sorted(os.listdir(patches)):
            if name.startswith("xray-") and name.endswith(".patch"):
                for section in replay.split_patch(os.path.join(patches, name))[1]:
                    old, new = replay.section_paths(section)
                    if old != new and old.endswith("_test.go") and not new.endswith(".go"):
                        parked.append((name, new))
        self.assertIn(("xray-xff-trusted-header.patch", "common/protocol/http/headers_test.go.upstream"), parked)
        self.assertIn(("xray-xff-trusted-header.patch", "transport/internet/grpc/encoding/remoteaddr_test.go.upstream"), parked)
        targets = replay.replay_targets(patches, None)
        self.assertEqual([entry for entry in parked if entry in targets], [])
        self.assertIn(("xray-xff-trusted-header.patch", "common/protocol/http/headers.go"), targets)
        self.assertIn(("xray-xff-trusted-header.patch", "transport/internet/grpc/encoding/remoteaddr.go"), targets)


if __name__ == "__main__":
    unittest.main()
