import importlib.util
import pathlib
import unittest


module_path = pathlib.Path(__file__).resolve().parents[2] / "scripts" / "debian-snapshot-sources.py"
spec = importlib.util.spec_from_file_location("debian_snapshot_sources", module_path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

BASE = """Types: deb
# http://snapshot.debian.org/archive/debian/20260918T000000Z
URIs: http://deb.debian.org/debian
Suites: bookworm bookworm-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
# http://snapshot.debian.org/archive/debian-security/20260918T000000Z
URIs: http://deb.debian.org/debian-security
Suites: bookworm-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
"""


class SnapshotSourcesTest(unittest.TestCase):
    def test_preserves_image_dates_suites_and_signatures(self):
        result = module.snapshot_sources(BASE)
        self.assertEqual(result, """Types: deb-src
URIs: https://snapshot.debian.org/archive/debian/20260918T000000Z/
Suites: bookworm bookworm-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
Check-Valid-Until: no

Types: deb-src
URIs: https://snapshot.debian.org/archive/debian-security/20260918T000000Z/
Suites: bookworm-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
Check-Valid-Until: no
""")

    def test_refuses_missing_snapshot_and_invalid_date(self):
        for content in ("Types: deb\n", BASE.replace("20260918", "20261399")):
            with self.subTest(content=content), self.assertRaises(ValueError):
                module.snapshot_sources(content)

    def test_refuses_foreign_repository_keyring_and_duplicate_fields(self):
        for content in (
            BASE.replace("deb.debian.org", "example.test"),
            BASE.replace("debian-archive-keyring.gpg", "untrusted.gpg"),
            BASE.replace("Suites: bookworm-security", "Suites: bookworm-security\nSuites: unstable"),
            BASE.replace("Components: main", "Components: main [trusted=yes]"),
        ):
            with self.subTest(content=content), self.assertRaises(ValueError):
                module.snapshot_sources(content)


if __name__ == "__main__":
    unittest.main()
