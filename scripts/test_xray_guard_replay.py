import os
import sys
import tempfile
import unittest

scripts = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, scripts)

import xray_guard_replay as replay

patches = os.path.join(os.path.dirname(scripts), "patches")
dockerfile = os.path.join(os.path.dirname(scripts), "Dockerfile")


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

    def test_the_shipped_patches_park_no_test_file_and_none_of_their_test_files_is_a_target(self):
        parked = []
        tests = []
        for name in sorted(os.listdir(patches)):
            if name.startswith("xray-") and name.endswith(".patch"):
                for section in replay.split_patch(os.path.join(patches, name))[1]:
                    old, new = replay.section_paths(section)
                    if old != new and old.endswith("_test.go") and not new.endswith(".go"):
                        parked.append((name, new))
                    if replay.test_only(section):
                        tests.append((name, new))
        self.assertEqual(parked, [])
        self.assertIn(("xray-xff-trusted-header.patch", "transport/internet/grpc/encoding/remoteaddr_test.go"), tests)
        self.assertIn(("xray-xff-trusted-header.patch", "testing/trustedheader/trustedheader.go"), tests)
        targets = replay.replay_targets(patches, None)
        self.assertEqual([entry for entry in tests if entry in targets], [])
        self.assertIn(("xray-xff-trusted-header.patch", "common/protocol/http/headers.go"), targets)
        self.assertIn(("xray-xff-trusted-header.patch", "transport/internet/grpc/encoding/remoteaddr.go"), targets)


def buildinfo(deps):
    lines = "path\tgithub.com/xtls/xray-core/main\nmod\tgithub.com/xtls/xray-core\t(devel)\t\n"
    for dep in deps:
        lines += dep
    lines += "build\t-trimpath=true\nbuild\tCGO_ENABLED=0\n"
    return lines.encode()


def dep(path, version):
    return "dep\t%s\t%s\th1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n" % (path, version)


BROTLI = "github.com/andybalholm/brotli"
COMPRESS = "github.com/klauspost/compress"


class BinaryChecks(unittest.TestCase):
    def check(self, *copies):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "xray")
            with open(path, "wb") as binary:
                binary.write(b"\x7fELF\x02\x01\x01\x00\x00\x00")
                for deps in copies:
                    binary.write(b"\xff Go buildinf:\x08\x02\x00\x00" + buildinfo(deps) + b"\x00\x01\x02")
            return replay.check_binary(dockerfile, path)

    def versions(self, brotli, compress):
        deps = [dep(BROTLI, brotli), dep("github.com/google/btree", "v1.1.2"), dep(COMPRESS, compress)]
        return self.check(deps, deps)

    def test_the_dockerfile_checks_the_binary_its_go_build_writes(self):
        checks = replay.binary_checks(dockerfile)
        self.assertEqual(len(checks), 4)
        for check in checks:
            self.assertIn(replay.BUILT_BINARY, check)
        self.assertEqual([BROTLI.replace(".", "[.]") in check for check in checks], [True, True, False, False])
        self.assertEqual([COMPRESS.replace(".", "[.]") in check for check in checks], [False, False, True, True])

    def test_the_chain_ends_with_the_decompressor_patch_and_its_go_mod_checks(self):
        self.assertEqual(replay.chain_steps(dockerfile)[-3:], [
            "git apply --verbose /patches/xray-zstd-brotli-cert-decompression.patch",
            'grep -q "github.com/andybalholm/brotli v1.2.7 // indirect$" go.mod',
            'grep -q "github.com/klauspost/compress v1.20.2 // indirect$" go.mod',
        ])

    def test_versions_at_or_above_both_floors_pass(self):
        for brotli, compress in [
            ("v1.2.7", "v1.20.2"),
            ("v1.2.8", "v1.20.5"),
            ("v1.2.10", "v1.20.10"),
            ("v1.2.70", "v1.20.20"),
            ("v1.3.0", "v1.21.0"),
            ("v1.10.0", "v1.30.0"),
            ("v1.20.1", "v1.100.0"),
        ]:
            with self.subTest(brotli=brotli, compress=compress):
                self.assertIsNone(self.versions(brotli, compress))

    def test_a_version_below_either_floor_fails(self):
        for brotli, compress in [
            ("v1.0.6", "v1.17.4"),
            ("v1.2.6", "v1.20.2"),
            ("v1.1.0", "v1.20.2"),
            ("v1.2.7-rc.1", "v1.20.2"),
            ("v1.2.8-0.20261101000000-0123456789ab", "v1.20.2"),
            ("v0.9.9", "v1.20.2"),
            ("v1.2.7", "v1.20.1"),
            ("v1.2.7", "v1.19.9"),
            ("v1.2.7", "v1.2.2"),
            ("v1.2.7", "v1.20.2-rc.1"),
            ("v1.2.7", "v0.30.0"),
        ]:
            with self.subTest(brotli=brotli, compress=compress):
                self.assertIsNotNone(self.versions(brotli, compress))

    def test_a_missing_module_fails(self):
        self.assertIsNotNone(self.check([dep(COMPRESS, "v1.20.2")]))
        self.assertIsNotNone(self.check([dep(BROTLI, "v1.2.7")]))

    def test_one_copy_of_the_build_info_below_a_floor_fails(self):
        good = [dep(BROTLI, "v1.2.7"), dep(COMPRESS, "v1.20.2")]
        self.assertIsNone(self.check(good, good))
        self.assertIsNotNone(self.check(good, [dep(BROTLI, "v1.0.6"), dep(COMPRESS, "v1.20.2")]))
        self.assertIsNotNone(self.check(good, [dep(BROTLI, "v1.2.7"), dep(COMPRESS, "v1.17.4")]))

    def test_a_replaced_module_fails(self):
        replaced = "dep\t%s\tv1.2.7\n=>\t../brotli\t(devel)\t\n" % BROTLI
        self.assertIsNotNone(self.check([replaced, dep(COMPRESS, "v1.20.2")]))


if __name__ == "__main__":
    unittest.main()
