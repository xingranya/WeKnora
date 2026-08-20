import base64
import unittest

from docreader.limits import (
    ImageBudget,
    ParseCancelledError,
    ResourceLimitError,
    file_payload_limit,
    grpc_message_limit_bytes,
    iter_limited_image_data,
    validate_request_limits,
)


class DocReaderLimitsTest(unittest.TestCase):
    def test_grpc_limit_converts_configured_mib_to_bytes(self):
        self.assertEqual(grpc_message_limit_bytes(50), 50 * 1024 * 1024)
        self.assertEqual(grpc_message_limit_bytes(0), 0)

    def test_file_payload_reserves_one_mib_for_protobuf(self):
        max_message_bytes = 50 * 1024 * 1024
        self.assertEqual(file_payload_limit(max_message_bytes), 49 * 1024 * 1024)
        validate_request_limits(
            49 * 1024 * 1024,
            max_message_bytes,
            max_message_bytes,
        )

        with self.assertRaises(ResourceLimitError):
            validate_request_limits(
                49 * 1024 * 1024 + 1,
                max_message_bytes,
                max_message_bytes,
            )

    def test_image_limits_cover_count_single_and_total_bytes(self):
        encoded = base64.b64encode(b"abc").decode("ascii")

        with self.assertRaises(ResourceLimitError):
            list(
                iter_limited_image_data(
                    {"a.png": encoded, "b.png": encoded},
                    max_count=1,
                    max_image_bytes=3,
                    max_total_bytes=6,
                )
            )

        with self.assertRaises(ResourceLimitError):
            list(
                iter_limited_image_data(
                    {"a.png": encoded},
                    max_count=1,
                    max_image_bytes=2,
                    max_total_bytes=6,
                )
            )

        with self.assertRaises(ResourceLimitError):
            list(
                iter_limited_image_data(
                    {"a.png": encoded, "b.png": encoded},
                    max_count=2,
                    max_image_bytes=3,
                    max_total_bytes=5,
                )
            )

    def test_image_iteration_checks_cancellation_before_decode(self):
        images = {"a.png": base64.b64encode(b"abc").decode("ascii")}
        with self.assertRaises(ParseCancelledError):
            list(
                iter_limited_image_data(
                    images,
                    max_count=1,
                    max_image_bytes=3,
                    max_total_bytes=3,
                    is_cancelled=lambda: True,
                )
            )
        self.assertIn("a.png", images)

    def test_valid_image_is_decoded_once(self):
        encoded = base64.b64encode(b"image-bytes").decode("ascii")
        decoded = list(
            iter_limited_image_data(
                {"cover.png": encoded},
                max_count=1,
                max_image_bytes=32,
                max_total_bytes=32,
            )
        )
        self.assertEqual(decoded, [("cover.png", b"image-bytes")])

    def test_image_budget_counts_all_sources_together(self):
        budget = ImageBudget(max_count=2, max_image_bytes=4, max_total_bytes=5)
        budget.add_bytes(b"abc")
        with self.assertRaises(ResourceLimitError):
            budget.add_bytes(b"def")

        self.assertEqual(budget.count, 1)
        self.assertEqual(budget.total_bytes, 3)

    def test_image_preflight_rejects_total_before_mutating_source_map(self):
        images = {
            "a.png": base64.b64encode(b"abc").decode("ascii"),
            "b.png": base64.b64encode(b"def").decode("ascii"),
        }
        with self.assertRaises(ResourceLimitError):
            list(
                iter_limited_image_data(
                    images,
                    max_count=2,
                    max_image_bytes=3,
                    max_total_bytes=5,
                )
            )
        self.assertEqual(set(images), {"a.png", "b.png"})


if __name__ == "__main__":
    unittest.main()
