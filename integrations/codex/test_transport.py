from contextlib import closing
import http.client
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("pilot", Path(__file__).with_name("taaro-codex.py"))
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)


class TransportTest(unittest.TestCase):
    def test_server_origin(self):
        for address in ("http://taaro.example", "https://user@example.org",
                        "https://example.org?token=secret", "https://example.org/api",
                        "file:///tmp/test"):
            with self.subTest(address=address), self.assertRaises(pilot.PilotError):
                pilot.Client(address, "token")
        for address in ("http://127.0.0.1:8080", "http://[::1]"):
            self.assertIs(pilot.Client(address, "").connection, http.client.HTTPConnection)

    def test_https_uses_certificate_and_hostname_verification(self):
        client = pilot.Client("https://taaro.example.org", "token")
        with closing(client.connection(client.host, client.port)) as connection:
            self.assertTrue(connection._context.check_hostname)
            self.assertEqual(connection._context.verify_mode.name, "CERT_REQUIRED")
